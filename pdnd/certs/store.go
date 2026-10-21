package certs

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

type accountFile struct {
	DirectoryURL string `json:"directory_url"`
	URI          string `json:"uri"`
	KeyPEM       string `json:"key_pem"` // 账户私钥 PEM
}

type certMeta struct {
	Identifier string    `json:"identifier"`
	Profile    string    `json:"profile,omitempty"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
}

type storedCert struct {
	meta    certMeta
	tlsCert *tls.Certificate // tls.X509KeyPair 的结果，Leaf 已解析
	leaf    *x509.Certificate
}

type store struct{ dir string }

// bundleFile 是每个证书目录里私钥加证书链的那个文件（私钥在前）
const bundleFile = "bundle.pem"

// 不许以点开头：挡住 "." / ".." 这类会落到上级目录的名字
var dirNameSafe = regexp.MustCompile(`^[a-z0-9_][a-z0-9._-]{0,199}$`)

func newStore(dir string) (*store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("证书落盘目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建证书根目录 %s 失败: %w", dir, err)
	}
	certsDir := filepath.Join(dir, "certs")
	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建证书目录 %s 失败: %w", certsDir, err)
	}
	return &store{dir: dir}, nil
}

func (s *store) accountPath() string { return filepath.Join(s.dir, "account.json") }

func (s *store) certsRoot() string { return filepath.Join(s.dir, "certs") }

func (s *store) certDir(id string) string {
	return filepath.Join(s.certsRoot(), dirName(id))
}

func (s *store) loadAccount() (*accountFile, error) {
	path := s.accountPath()
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取账户文件 %s 失败: %w", path, err)
	}
	var a accountFile
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("解析账户文件 %s 失败: %w", path, err)
	}
	return &a, nil
}

func (s *store) saveAccount(a *accountFile) error {
	body, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("序列化账户数据失败: %w", err)
	}
	path := s.accountPath()
	if err := writeFileAtomic(path, body); err != nil {
		return fmt.Errorf("写入账户文件 %s 失败: %w", path, err)
	}
	return nil
}

func (s *store) loadCerts() (certs map[string]*storedCert, skipped []string) {
	certs = make(map[string]*storedCert)
	root := s.certsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return certs, nil
		}
		return certs, []string{fmt.Sprintf("%s: %v", root, err)}
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		sub := ent.Name()
		sc, err := s.loadCertDir(sub)
		if err != nil {
			skipped = append(skipped, sub)
			continue
		}
		id := core.NormalizeCertIdentifier(sc.meta.Identifier)
		certs[id] = sc
	}
	return certs, skipped
}

func (s *store) loadCertDir(sub string) (*storedCert, error) {
	dir := filepath.Join(s.certsRoot(), sub)
	metaPath := filepath.Join(dir, "meta.json")
	bundlePath := filepath.Join(dir, bundleFile)

	metaBody, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", metaPath, err)
	}
	var meta certMeta
	if err := json.Unmarshal(metaBody, &meta); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", metaPath, err)
	}
	id := core.NormalizeCertIdentifier(meta.Identifier)
	if id == "" {
		return nil, fmt.Errorf("%s 中标识符为空", metaPath)
	}
	if dirName(id) != sub {
		return nil, fmt.Errorf("目录名 %s 与标识符 %q 不符", sub, id)
	}

	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", bundlePath, err)
	}

	// X509KeyPair 从证书参数里只取 CERTIFICATE 块、从私钥参数里只取私钥块，同一份 bundle 传两次即可
	sc, err := parseStoredCert(id, bundle, bundle, meta)
	if err != nil {
		return nil, err
	}
	return sc, nil
}

func parseStoredCert(id string, certPEM, keyPEM []byte, meta certMeta) (*storedCert, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("证书与私钥不配对: %w", err)
	}
	leaf := pair.Leaf
	if leaf == nil {
		if len(pair.Certificate) == 0 {
			return nil, errors.New("证书链为空")
		}
		leaf, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("解析叶子证书失败: %w", err)
		}
		pair.Leaf = leaf
	}
	if !leafContainsIdentifier(leaf, id) {
		return nil, fmt.Errorf("叶子证书的 SAN 不包含标识符 %q", id)
	}
	meta.Identifier = id
	meta.NotBefore = leaf.NotBefore
	meta.NotAfter = leaf.NotAfter
	return &storedCert{meta: meta, tlsCert: &pair, leaf: leaf}, nil
}

func leafContainsIdentifier(leaf *x509.Certificate, id string) bool {
	id = core.NormalizeCertIdentifier(id)
	if ip := net.ParseIP(id); ip != nil {
		for _, x := range leaf.IPAddresses {
			if x.Equal(ip) {
				return true
			}
		}
		return false
	}
	want := strings.ToLower(id)
	for _, name := range leaf.DNSNames {
		if strings.EqualFold(name, want) {
			return true
		}
	}
	return false
}

func (s *store) saveCert(id string, certPEM, keyPEM []byte, meta certMeta) (*storedCert, error) {
	id = core.NormalizeCertIdentifier(id)
	if id == "" {
		return nil, errors.New("证书标识符不能为空")
	}
	sc, err := parseStoredCert(id, certPEM, keyPEM, meta)
	if err != nil {
		return nil, err
	}
	dir := s.certDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建证书目录 %s 失败: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("设置证书目录 %s 权限失败: %w", dir, err)
	}

	bundlePath := filepath.Join(dir, bundleFile)
	metaPath := filepath.Join(dir, "meta.json")

	// meta 先写：读盘时只取它的标识符与 profile（同一标识符每次都一样），到期时间以 bundle 里的叶子证书为准，新 meta 配旧 bundle 也成立。
	// 私钥与证书链合在一个文件里一次 rename，续期中途被杀也不会留下不配对的一对（certs 审查 S5）。
	metaBody, err := json.Marshal(sc.meta)
	if err != nil {
		return nil, fmt.Errorf("序列化元数据失败: %w", err)
	}
	if err := writeFileAtomic(metaPath, metaBody); err != nil {
		return nil, fmt.Errorf("写入 %s 失败: %w", metaPath, err)
	}
	bundle := make([]byte, 0, len(keyPEM)+len(certPEM)+1)
	bundle = append(bundle, keyPEM...)
	if len(keyPEM) > 0 && keyPEM[len(keyPEM)-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	bundle = append(bundle, certPEM...)
	if err := writeFileAtomic(bundlePath, bundle); err != nil {
		return nil, fmt.Errorf("写入 %s 失败: %w", bundlePath, err)
	}
	return sc, nil
}

func (s *store) remove(id string) error {
	id = core.NormalizeCertIdentifier(id)
	dir := s.certDir(id)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("删除证书目录 %s 失败: %w", dir, err)
	}
	return nil
}

func (s *store) purge() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("清空证书目录 %s 失败: %w", s.dir, err)
	}
	return nil
}

func dirName(id string) string {
	id = core.NormalizeCertIdentifier(id)
	name := strings.ReplaceAll(id, ":", "_")
	if dirNameSafe.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(id))
	return "h-" + hex.EncodeToString(sum[:])[:16]
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".acme-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	return nil
}
