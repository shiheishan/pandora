// Package certstore 是 pdnd 进程内的证书仓库。
//
// 所有 TLS 入站都经 Resolve 拿到 GetCertificate 回调，每次握手现读一次原子指针：
// 替换证书只换指针，新握手立即用新叶子证书，已建立的连接不受影响。
// 证书有两种来源：
//   - ManagedSource：面板下发的托管证书，命名空间是证书包里签过名的 server_id，
//     落在 /var/lib/pandora-native/panels/<server_id>/certs/<cert_id>/v<version>/；
//   - FileSource：管理员手放在 /etc/pandora-native/certs/ 下的证书文件，命名空间 file，按 mtime/size 轮询热更新。
//
// 任何来源读到坏证书（坏 PEM、私钥不配对、已过期、路径逃逸）都保留旧证书，只记错误。
package certstore

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Kind 是证书来源。
type Kind string

const (
	KindManaged Kind = "managed"
	KindFile    Kind = "file"
)

// FileNamespace 是 file 模式证书的命名空间。托管证书的命名空间是 UUID，不会与它相撞。
const FileNamespace = "file"

// 错误码：心跳上报给面板，面板按码出文案。
const (
	ErrCodeBadPEM        = "bad_pem"
	ErrCodeKeyMismatch   = "key_mismatch"
	ErrCodeExpired       = "expired"
	ErrCodeReadFailed    = "read_failed"
	ErrCodePathEscape    = "path_escape"
	ErrCodePersistFailed = "persist_failed"
	ErrCodeRemoved       = "removed"
)

var fileIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Key 定位仓库里的一张证书。
//   - 托管证书：Namespace = 签过名的 server_id，ID = cert_id，都是规范小写 UUID。
//     不能用 tenant_id 当命名空间：各面板的默认租户 ID 可能相同。
//   - file 模式：Namespace = "file"，ID = hex(sha256(cert_path + "|" + key_path))。
type Key struct {
	Namespace string
	ID        string
}

// ManagedKey 构造并校验托管证书的键。
func ManagedKey(serverID, certID string) (Key, error) {
	k := Key{Namespace: serverID, ID: certID}
	if _, err := k.Kind(); err != nil {
		return Key{}, err
	}
	if k.Namespace == FileNamespace {
		return Key{}, errors.New("certstore: server_id must be a canonical lowercase UUID")
	}
	return k, nil
}

// FileKey 构造 file 模式证书的键。路径先按 filepath.Clean 规范化。
func FileKey(certPath, keyPath string) Key {
	sum := sha256.Sum256([]byte(certPath + "|" + keyPath))
	return Key{Namespace: FileNamespace, ID: hex.EncodeToString(sum[:])}
}

// Kind 校验键的形状并返回来源。键的两部分都会拼进磁盘路径或日志，所以严格校验。
func (k Key) Kind() (Kind, error) {
	if k.Namespace == FileNamespace {
		if !fileIDPattern.MatchString(k.ID) {
			return "", errors.New("certstore: file certificate id must be 64 lowercase hex characters")
		}
		return KindFile, nil
	}
	if err := validateUUID("server_id", k.Namespace); err != nil {
		return "", err
	}
	if err := validateUUID("cert_id", k.ID); err != nil {
		return "", err
	}
	return KindManaged, nil
}

func (k Key) String() string { return k.Namespace + "/" + k.ID }

func validateUUID(field, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return fmt.Errorf("certstore: %s must be a canonical lowercase UUID", field)
	}
	return nil
}

// Status 是一张证书的元数据快照，供心跳上报。
type Status struct {
	Source            Kind
	Namespace         string
	ID                string
	Version           uint64 // 托管证书的版本号；file 模式为 0
	CertPath          string // 仅 file 模式
	KeyPath           string // 仅 file 模式
	FingerprintSHA256 string // 叶子证书 DER 的 SHA-256，小写十六进制
	ChainSHA256       string // fullchain PEM 字节的 SHA-256，标准 base64（与证书包 chain_sha256 同口径）
	NotAfter          time.Time
	ErrorCode         string
	Error             string
	UpdatedAt         time.Time
}

// loaded 是一张自检通过、正在服务的证书。整体换指针，读者看到的永远是一致的一份。
type loaded struct {
	cert        *tls.Certificate
	leaf        *x509.Certificate
	version     uint64
	chainSHA256 string
	fingerprint string
	notAfter    time.Time
}

// slot 一旦建立就不从 map 里删除：Resolve 返回的闭包持有 slot 指针，
// 删了再建会让旧闭包永远指着失效对象。移除证书只把指针清空（握手 fail closed）。
type slot struct {
	cur    atomic.Pointer[loaded]
	status atomic.Pointer[Status]
}

// Store 是进程内证书仓库，并发安全。
type Store struct {
	mu    sync.RWMutex
	slots map[Key]*slot
	now   func() time.Time
}

// New 建一个空仓库。
func New() *Store {
	return &Store{slots: make(map[Key]*slot), now: time.Now}
}

func (s *Store) slotFor(k Key, create bool) *slot {
	s.mu.RLock()
	sl := s.slots[k]
	s.mu.RUnlock()
	if sl != nil || !create {
		return sl
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sl = s.slots[k]; sl == nil {
		sl = &slot{}
		s.slots[k] = sl
	}
	return sl
}

// put 原子替换证书，并写入对应的元数据。
func (s *Store) put(k Key, l *loaded, base Status) {
	sl := s.slotFor(k, true)
	st := base
	st.Namespace, st.ID = k.Namespace, k.ID
	st.Version = l.version
	st.FingerprintSHA256 = l.fingerprint
	st.ChainSHA256 = l.chainSHA256
	st.NotAfter = l.notAfter
	st.UpdatedAt = s.now().UTC()
	sl.cur.Store(l)
	sl.status.Store(&st)
}

// fail 记录一次失败：保留旧证书与旧证书的元数据（版本、指纹、NotAfter），只换错误。
// 还没有任何证书时登记一个只有错误的状态，心跳照样能报出来。
func (s *Store) fail(k Key, base Status, code string, err error) {
	sl := s.slotFor(k, true)
	st := base
	if prev := sl.status.Load(); prev != nil && sl.cur.Load() != nil {
		st = *prev
	}
	st.Namespace, st.ID = k.Namespace, k.ID
	st.ErrorCode = code
	st.Error = err.Error()
	st.UpdatedAt = s.now().UTC()
	sl.status.Store(&st)
}

// remove 清空证书指针：此后经该键的握手一律失败。
func (s *Store) remove(k Key) {
	sl := s.slotFor(k, false)
	if sl == nil {
		return
	}
	sl.cur.Store(nil)
	st := Status{Namespace: k.Namespace, ID: k.ID}
	if prev := sl.status.Load(); prev != nil {
		st = *prev
	}
	st.ErrorCode = ErrCodeRemoved
	st.Error = "certificate removed from store"
	st.UpdatedAt = s.now().UTC()
	sl.status.Store(&st)
}

// namespaceKeys 列出某命名空间下所有登记过的键。
func (s *Store) namespaceKeys(ns string) []Key {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Key
	for k := range s.slots {
		if k.Namespace == ns {
			out = append(out, k)
		}
	}
	return out
}

// Current 返回键当前持有的证书；没有时返回 nil。
func (s *Store) Current(k Key) *tls.Certificate {
	if sl := s.slotFor(k, false); sl != nil {
		if l := sl.cur.Load(); l != nil {
			return l.cert
		}
	}
	return nil
}

// StatusOf 返回键当前的元数据。
func (s *Store) StatusOf(k Key) (Status, bool) {
	sl := s.slotFor(k, false)
	if sl == nil {
		return Status{}, false
	}
	st := sl.status.Load()
	if st == nil {
		return Status{}, false
	}
	return *st, true
}

// Resolve 返回给 tls.Config.GetCertificate 用的回调（设计稿里的 Getter）。
// 键不存在或还没有可用证书时返回错误，调用方据此在 Validate 阶段就失败，而不是等到握手时才发现。
// 回调不看 ClientHello：SNI 为空（IP 直连）也回同一张证书；SNI 是否匹配交给客户端校验。
func (s *Store) Resolve(k Key) (func(*tls.ClientHelloInfo) (*tls.Certificate, error), error) {
	if _, err := k.Kind(); err != nil {
		return nil, err
	}
	sl := s.slotFor(k, false)
	if sl == nil || sl.cur.Load() == nil {
		return nil, fmt.Errorf("certstore: certificate %s is not ready", k)
	}
	name := k.String()
	return func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if l := sl.cur.Load(); l != nil {
			return l.cert, nil
		}
		return nil, fmt.Errorf("certstore: certificate %s is no longer available", name)
	}, nil
}

// Snapshot 返回全部证书的元数据，按键排序，已移除的不列。
func (s *Store) Snapshot() []Status {
	type pair struct {
		key Key
		sl  *slot
	}
	s.mu.RLock()
	pairs := make([]pair, 0, len(s.slots))
	for k, sl := range s.slots {
		pairs = append(pairs, pair{k, sl})
	}
	s.mu.RUnlock()
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key.String() < pairs[j].key.String() })
	out := make([]Status, 0, len(pairs))
	for _, p := range pairs {
		st := p.sl.status.Load()
		if st == nil || st.ErrorCode == ErrCodeRemoved {
			continue
		}
		out = append(out, *st)
	}
	return out
}

// Digest 是「哪些证书、哪个版本、哪个指纹正在服务」的摘要。
// 后续阶段把它并进签名配置失败台账的键：缺的证书到位后摘要变化，失败的发布会被重新尝试。
func (s *Store) Digest() string {
	var b strings.Builder
	for _, st := range s.Snapshot() {
		b.WriteString(st.Namespace + "/" + st.ID + "\x00")
		b.WriteString(strconv.FormatUint(st.Version, 10) + "\x00" + st.FingerprintSHA256 + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
