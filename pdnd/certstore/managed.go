package certstore

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultManagedRoot 是托管证书的落盘根目录，在 systemd 的 StateDirectory 内，
	// ProtectSystem=strict 下可写，UMask=0077。
	DefaultManagedRoot = "/var/lib/pandora-native/panels"
	// RetiredRetention 是证书不再出现在最新证书包里之后，磁盘与内存保留它的时长。
	RetiredRetention = 7 * 24 * time.Hour

	currentFile   = "current.json"
	retiredFile   = "retired_at"
	chainFile     = "fullchain.pem"
	privKeyFile   = "privkey.pem"
	metaFile      = "meta.json"
	maxSmallFile  = 64 << 10
	versionPrefix = "v"
)

// ManagedVersion 是一张托管证书的一个版本，来自验签、解密后的证书包。
type ManagedVersion struct {
	ServerID        string // 证书包里签过名的 server_id，即命名空间
	CertID          string
	Version         uint64
	ChainPEM        []byte // fullchain PEM，叶子证书在前
	PrivateKeyPKCS8 []byte // PKCS#8 DER，即证书包 HPKE 解密出的明文
}

// currentPointer 是 current.json：指向当前服务的版本目录，原子 rename 更新。
type currentPointer struct {
	Version     uint64 `json:"version"`
	ChainSHA256 string `json:"chain_sha256"`
}

// versionMeta 是版本目录里的 meta.json，只放公开信息，便于排障。
type versionMeta struct {
	ServerID          string    `json:"server_id"`
	CertID            string    `json:"cert_id"`
	Version           uint64    `json:"version"`
	ChainSHA256       string    `json:"chain_sha256"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"`
	NotAfter          time.Time `json:"not_after"`
	InstalledAt       time.Time `json:"installed_at"`
}

// ManagedSource 管托管证书的落盘、启动加载与清理。
//
// 目录布局：<root>/<server_id>/certs/<cert_id>/
//
//	current.json               指向当前版本
//	v<version>/fullchain.pem   0600
//	v<version>/privkey.pem     0600（本地明文，和 certbot 一样；加密密钥就在同一块盘上，再加一层没有意义）
//	v<version>/meta.json       0600
//	retired_at                 不在最新证书包里的起始时间，满 7 天整目录删除
//
// 目录一律 0700。
type ManagedSource struct {
	root  string
	store *Store
	now   func() time.Time
	mu    sync.Mutex // 串行化写盘与清理
}

// NewManagedSource 建托管来源；root 通常是 DefaultManagedRoot，测试注入临时目录。
func NewManagedSource(root string, store *Store) *ManagedSource {
	return &ManagedSource{root: filepath.Clean(root), store: store, now: time.Now}
}

func (m *ManagedSource) certDir(serverID, certID string) string {
	return filepath.Join(m.root, serverID, "certs", certID)
}

// Install 自检并安装一个版本：私钥与叶子证书配对、没过期，版本不回退；
// 先原子落盘，再原子替换内存指针。自检失败保留旧证书并记错误。
// 落盘失败时内存照样替换（新握手立即用上），状态记 persist_failed，重启会回到盘上的旧版本。
func (m *ManagedSource) Install(v ManagedVersion) error {
	k, err := ManagedKey(v.ServerID, v.CertID)
	if err != nil {
		return err
	}
	if v.Version == 0 || v.Version > math.MaxInt64 {
		return errors.New("certstore: version must be a positive PostgreSQL bigint")
	}
	base := Status{Source: KindManaged}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: v.PrivateKeyPKCS8})
	l, err := parseKeyPair(v.ChainPEM, keyPEM, m.now())
	if err != nil {
		m.store.fail(k, base, errorCode(err), err)
		return fmt.Errorf("certstore: reject %s v%d: %w", k, v.Version, err)
	}
	l.version = v.Version

	m.mu.Lock()
	defer m.mu.Unlock()
	if sl := m.store.slotFor(k, false); sl != nil {
		if cur := sl.cur.Load(); cur != nil {
			if v.Version < cur.version {
				return fmt.Errorf("certstore: refuse to roll %s back from v%d to v%d", k, cur.version, v.Version)
			}
			if v.Version == cur.version && l.chainSHA256 != cur.chainSHA256 {
				return fmt.Errorf("certstore: %s v%d already installed with a different chain", k, v.Version)
			}
		}
	}
	if err := m.persist(k, v, keyPEM, l); err != nil {
		m.store.put(k, l, base)
		m.store.fail(k, base, ErrCodePersistFailed, err)
		return fmt.Errorf("certstore: persist %s v%d: %w", k, v.Version, err)
	}
	m.store.put(k, l, base)
	return nil
}

// persist 先在 cert 目录下写临时目录（文件逐个 fsync、目录 fsync），rename 成 v<version>，
// fsync cert 目录，最后原子替换 current.json。任何一步失败，current.json 仍指向旧版本。
func (m *ManagedSource) persist(k Key, v ManagedVersion, keyPEM []byte, l *loaded) error {
	dir := m.certDir(k.Namespace, k.ID)
	if err := ensurePrivateDir(m.root, dir); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, tempPrefix+"v*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.Chmod(tmp, 0o700); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(versionMeta{
		ServerID: k.Namespace, CertID: k.ID, Version: v.Version, ChainSHA256: l.chainSHA256,
		FingerprintSHA256: l.fingerprint, NotAfter: l.notAfter, InstalledAt: m.now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	for name, body := range map[string][]byte{chainFile: v.ChainPEM, privKeyFile: keyPEM, metaFile: meta} {
		if err := writeFileAtomic(filepath.Join(tmp, name), body, 0o600); err != nil {
			return err
		}
	}
	final := filepath.Join(dir, versionPrefix+strconv.FormatUint(v.Version, 10))
	// 同版本重装（修盘）：旧目录先挪开，新目录就位后再删，current.json 始终指向一个完整目录名。
	var aside string
	if _, err := os.Lstat(final); err == nil {
		aside = filepath.Join(dir, tempPrefix+"old-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		if err := os.Rename(final, aside); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	committed = true
	if aside != "" {
		_ = os.RemoveAll(aside)
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	ptr, err := json.Marshal(currentPointer{Version: v.Version, ChainSHA256: l.chainSHA256})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, currentFile), ptr, 0o600)
}

// Load 启动时扫描磁盘，按 current.json 加载全部托管证书；面板不可达也能起来。
// 单张证书坏了不影响其他证书，错误汇总返回，同时记进各自的状态。
// 已过期的证书照样加载（启动时没有更旧的可退），状态记 expired，交给心跳上报。
func (m *ManagedSource) Load() error {
	servers, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var errs []error
	for _, s := range servers {
		if !s.IsDir() || validateUUID("server_id", s.Name()) != nil {
			continue
		}
		certs, err := os.ReadDir(filepath.Join(m.root, s.Name(), "certs"))
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		for _, c := range certs {
			if !c.IsDir() || validateUUID("cert_id", c.Name()) != nil {
				continue
			}
			if err := m.loadOne(Key{Namespace: s.Name(), ID: c.Name()}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *ManagedSource) loadOne(k Key) error {
	base := Status{Source: KindManaged}
	fail := func(code string, err error) error {
		m.store.fail(k, base, code, err)
		return fmt.Errorf("certstore: load %s: %w", k, err)
	}
	dir := m.certDir(k.Namespace, k.ID)
	ptr, err := readPointer(dir)
	if err != nil {
		return fail(ErrCodeReadFailed, err)
	}
	vdir := filepath.Join(dir, versionPrefix+strconv.FormatUint(ptr.Version, 10))
	chain, err := readSmallFile(filepath.Join(vdir, chainFile), maxPEMSize)
	if err != nil {
		return fail(ErrCodeReadFailed, err)
	}
	keyPEM, err := readSmallFile(filepath.Join(vdir, privKeyFile), maxPEMSize)
	if err != nil {
		return fail(ErrCodeReadFailed, err)
	}
	if ChainSHA256(chain) != ptr.ChainSHA256 {
		return fail(ErrCodeReadFailed, errors.New("fullchain.pem does not match current.json chain_sha256"))
	}
	// 传零时刻跳过过期检查：启动时没有更旧的可退，过期的也先加载，下面单独记 expired。
	l, err := parseKeyPair(chain, keyPEM, time.Time{})
	if err != nil {
		return fail(errorCode(err), err)
	}
	l.version = ptr.Version
	m.store.put(k, l, base)
	if !m.now().Before(l.notAfter) {
		m.store.fail(k, base, ErrCodeExpired, fmt.Errorf("leaf certificate expired at %s", l.notAfter.Format(time.RFC3339)))
	}
	return nil
}

func readPointer(dir string) (currentPointer, error) {
	var ptr currentPointer
	body, err := readSmallFile(filepath.Join(dir, currentFile), maxSmallFile)
	if err != nil {
		return ptr, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ptr); err != nil {
		return ptr, fmt.Errorf("invalid %s: %w", currentFile, err)
	}
	if ptr.Version == 0 || ptr.Version > math.MaxInt64 || ptr.ChainSHA256 == "" {
		return ptr, fmt.Errorf("invalid %s", currentFile)
	}
	return ptr, nil
}

// readSmallFile 读一个不超过 limit 字节的普通文件（拒绝符号链接与特殊文件）。
func readSmallFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return body, nil
}

// Cleanup 按最新证书包整理一个命名空间：
//   - 每张证书只保留 current.json 指向的版本目录，删掉旧版本与崩溃遗留的临时目录；
//   - 不在 present 里的证书记下 retired_at，满 RetiredRetention 后删整个证书目录并清空内存指针；
//     重新出现在包里就撤销 retired_at。
//
// current.json 读不出来的证书目录不动，宁可多占盘也不误删正在服务的版本。
func (m *ManagedSource) Cleanup(serverID string, present map[string]bool) error {
	if err := validateUUID("server_id", serverID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	certsDir := filepath.Join(m.root, serverID, "certs")
	certs, err := os.ReadDir(certsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := m.now().UTC()
	var errs []error
	for _, c := range certs {
		if !c.IsDir() || validateUUID("cert_id", c.Name()) != nil {
			continue
		}
		k := Key{Namespace: serverID, ID: c.Name()}
		dir := filepath.Join(certsDir, c.Name())
		if !present[c.Name()] {
			expired, err := m.markRetired(dir, now)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if expired {
				if err := os.RemoveAll(dir); err != nil {
					errs = append(errs, err)
					continue
				}
				m.store.remove(k)
				if err := syncDir(certsDir); err != nil {
					errs = append(errs, err)
				}
				continue
			}
		} else if err := os.Remove(filepath.Join(dir, retiredFile)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		if err := pruneVersions(dir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// markRetired 首次发现证书不在包里时写 retired_at；返回是否已满保留期。
func (m *ManagedSource) markRetired(dir string, now time.Time) (bool, error) {
	path := filepath.Join(dir, retiredFile)
	body, err := readSmallFile(path, maxSmallFile)
	if err == nil {
		since, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(body)))
		if perr == nil {
			return !now.Before(since.Add(RetiredRetention)), nil
		}
		// 标记文件坏了：重写成现在，重新计时（保守，不直接删）。
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, writeFileAtomic(path, []byte(now.Format(time.RFC3339Nano)+"\n"), 0o600)
}

// pruneVersions 删除非当前版本目录与临时遗留；读不到 current.json 时什么都不删。
func pruneVersions(dir string) error {
	ptr, err := readPointer(dir)
	if err != nil {
		return nil
	}
	keep := versionPrefix + strconv.FormatUint(ptr.Version, 10)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	removed := false
	var errs []error
	for _, e := range entries {
		name := e.Name()
		stale := strings.HasPrefix(name, tempPrefix)
		if !stale && e.IsDir() && name != keep && strings.HasPrefix(name, versionPrefix) {
			_, perr := strconv.ParseUint(strings.TrimPrefix(name, versionPrefix), 10, 64)
			stale = perr == nil
		}
		if !stale {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = true
	}
	if removed {
		errs = append(errs, syncDir(dir))
	}
	return errors.Join(errs...)
}

// RemoveNamespace 在整个面板绑定解除时删除该命名空间的全部托管证书（盘上与内存）。
func (m *ManagedSource) RemoveNamespace(serverID string) error {
	if err := validateUUID("server_id", serverID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.store.namespaceKeys(serverID) {
		m.store.remove(k)
	}
	if err := os.RemoveAll(filepath.Join(m.root, serverID)); err != nil {
		return err
	}
	if err := syncDir(m.root); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
