package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Identity 是磁盘上的节点身份（一台服务器一个）。证书与 CA 可在会话里续期替换，读写并发安全。
type Identity struct {
	dir      string
	serverID string
	mu       sync.RWMutex
	cert     *tls.Certificate // Leaf 已填
	caPEM    []byte
	caPool   *x509.CertPool
}

// LoadIdentity 从目录加载身份；先完成半截的 staged 替换。
func LoadIdentity(dir string) (*Identity, error) {
	if err := promoteStaged(dir); err != nil {
		return nil, err
	}
	serverIDBytes, err := os.ReadFile(filepath.Join(dir, serverIDFile))
	if err != nil {
		return nil, wrapMissing(err, serverIDFile)
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, certFile))
	if err != nil {
		return nil, wrapMissing(err, certFile)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, caFile))
	if err != nil {
		return nil, wrapMissing(err, caFile)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, wrapMissing(err, keyFile)
	}
	serverID := strings.TrimSpace(string(serverIDBytes))
	cert, pool, err := validateIdentity(serverID, certPEM, caPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &Identity{
		dir:      dir,
		serverID: serverID,
		cert:     cert,
		caPEM:    append([]byte(nil), caPEM...),
		caPool:   pool,
	}, nil
}

func wrapMissing(err error, name string) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", name, os.ErrNotExist)
	}
	return err
}

// Dir 返回身份目录路径。
func (id *Identity) Dir() string { return id.dir }

// ServerID 返回面板分配的服务器 UUID。
func (id *Identity) ServerID() string { return id.serverID }

// NotAfter 返回当前客户端证书叶子的 NotAfter。
func (id *Identity) NotAfter() time.Time {
	id.mu.RLock()
	defer id.mu.RUnlock()
	if id.cert == nil || id.cert.Leaf == nil {
		return time.Time{}
	}
	return id.cert.Leaf.NotAfter
}

// Certificate 返回当前客户端证书（含私钥）。
func (id *Identity) Certificate() *tls.Certificate {
	id.mu.RLock()
	defer id.mu.RUnlock()
	return id.cert
}

// ClientTLSConfig 构造连面板的 mTLS 客户端配置。
// 每次建流现取一份：续期换了 CA 之后，下一条流按新 CA 校验面板。
func (id *Identity) ClientTLSConfig(serverName string) *tls.Config {
	id.mu.RLock()
	pool := id.caPool
	id.mu.RUnlock()
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: serverName,
		RootCAs:    pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			id.mu.RLock()
			defer id.mu.RUnlock()
			return id.cert, nil
		},
	}
}

// Replace 用新证书链与 CA 替换磁盘与内存中的客户端材料（不动 server_id）。
func (id *Identity) Replace(certChainPEM, caPEM, keyPEM []byte) error {
	cert, pool, err := validateIdentity(id.serverID, certChainPEM, caPEM, keyPEM)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		certFile: certChainPEM,
		caFile:   caPEM,
		keyFile:  keyPEM,
	}
	if err := stageAndPromote(id.dir, files); err != nil {
		return err
	}
	id.mu.Lock()
	id.cert = cert
	id.caPEM = append([]byte(nil), caPEM...)
	id.caPool = pool
	id.mu.Unlock()
	return nil
}

// WriteFileAtomic 原子写文件：同目录临时文件写入、fsync、rename，再 fsync 目录。
// 读者要么看到旧文件、要么看到完整的新文件，断电也不会留下半截。身份目录与节点的落盘缓存都走它。
func WriteFileAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(body); err != nil {
		cleanup()
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		err = d.Sync()
		_ = d.Close()
		if err != nil && !(runtime.GOOS == "windows" && os.IsPermission(err)) {
			return err
		}
	}
	return nil
}

// validateIdentity 核对一套身份：私钥与叶子配对、叶子由 caPEM 里的 CA 签发且可作客户端证书、
// URI SAN 是 spiffe://pandora/server/<serverID>。
func validateIdentity(serverID string, certChainPEM, caPEM, keyPEM []byte) (*tls.Certificate, *x509.CertPool, error) {
	if _, err := uuid.Parse(serverID); err != nil {
		return nil, nil, fmt.Errorf("server_id 不是合法 UUID: %w", err)
	}
	cert, err := tls.X509KeyPair(certChainPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("证书与私钥不匹配: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	cert.Leaf = leaf

	pool := x509.NewCertPool()
	rest := caPEM
	var caCount int
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, err
		}
		if c.IsCA {
			pool.AddCert(c)
			caCount++
		}
	}
	if caCount == 0 {
		return nil, nil, errors.New("ca.pem 里没有 CA 证书")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, nil, fmt.Errorf("客户端证书校验失败: %w", err)
	}
	want := "spiffe://pandora/server/" + serverID
	for _, u := range leaf.URIs {
		if u.String() == want {
			return &cert, pool, nil
		}
	}
	return nil, nil, fmt.Errorf("叶子证书缺少 URI SAN %q", want)
}

// stageAndPromote 崩溃安全地整套换上身份文件：先把整套写进 next/，写完再放 ready 标记，
// 然后逐个 rename 到位。任何时刻崩溃，下次 LoadIdentity 要么丢掉半截的 next/（没有 ready），
// 要么把剩下的文件继续换上（有 ready），不会出现新证书配旧私钥。
func stageAndPromote(dir string, files map[string][]byte) error {
	next := filepath.Join(dir, nextDir)
	_ = os.RemoveAll(next)
	if err := os.MkdirAll(next, 0o700); err != nil {
		return err
	}
	for name, body := range files {
		if err := WriteFileAtomic(filepath.Join(next, name), body, 0o600); err != nil {
			return err
		}
	}
	if err := WriteFileAtomic(filepath.Join(next, readyMarker), []byte("1\n"), 0o600); err != nil {
		return err
	}
	return promoteStaged(dir)
}

var promoteOrder = []string{keyFile, certFile, caFile, serverIDFile}

// promoteStaged 完成（或丢弃）上次没做完的 stageAndPromote，见上。
func promoteStaged(dir string) error {
	next := filepath.Join(dir, nextDir)
	readyPath := filepath.Join(next, readyMarker)
	if _, err := os.Stat(readyPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, err2 := os.Stat(next); err2 == nil {
				return os.RemoveAll(next)
			}
			return nil
		}
		return err
	}
	for _, name := range promoteOrder {
		src := filepath.Join(next, name)
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		dst := filepath.Join(dir, name)
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	_ = os.Remove(readyPath)
	if err := os.RemoveAll(next); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		err = d.Sync()
		_ = d.Close()
		if err != nil && !(runtime.GOOS == "windows" && os.IsPermission(err)) {
			return err
		}
	}
	return nil
}

// newKeyAndCSR 生成 P-256 私钥 PEM 与 CSR DER；serverID 写入 CSR 的 CN（接入时可为空）。
func newKeyAndCSR(serverID string) (keyPEM []byte, csrDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	tmpl := x509.CertificateRequest{
		Subject: pkixName(serverID),
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, csrDER, nil
}

func pkixName(serverID string) pkix.Name {
	var n pkix.Name
	if serverID != "" {
		n.CommonName = serverID
	}
	return n
}
