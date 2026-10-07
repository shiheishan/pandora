package certstore

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 测试夹具全是虚构数据：自建 CA、example.com 主机名、固定的假 UUID。
const (
	testHost     = "node.example.com"
	testServerID = "0193f0a0-1111-7000-8000-00000000c0de"
	testCertID   = "0193f0a0-2222-7000-8000-00000000c0de"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "certstore test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

type testLeaf struct {
	chainPEM []byte
	keyPEM   []byte
	keyDER   []byte // PKCS#8
	fp       string // 叶子 DER 的 SHA-256 十六进制
}

// issue 签一张叶子证书；notAfter 为零时默认 1 小时后过期。
func (ca *testCA) issue(t *testing.T, notAfter time.Time) testLeaf {
	t.Helper()
	if notAfter.IsZero() {
		notAfter = time.Now().Add(time.Hour)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: testHost},
		DNSNames:     []string{testHost},
		NotBefore:    time.Now().Add(-2 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})...)
	sum := sha256.Sum256(der)
	return testLeaf{
		chainPEM: chain,
		keyPEM:   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		keyDER:   keyDER,
		fp:       hex.EncodeToString(sum[:]),
	}
}

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// bumpMTime 把文件 mtime 推到一个明确不同的时刻，避免文件系统时间粒度让轮询看不出变化。
func bumpMTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// newFileRoot 建一个充当 /etc/pandora-native/certs 的临时目录，返回它与它旁边的「目录外」。
func newFileRoot(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "certs")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return root, outside
}

// tlsServer 起一个真实的 tls 监听，GetCertificate 来自仓库；每个连接按行回显。
func tlsServer(t *testing.T, get func(*tls.ClientHelloInfo) (*tls.Certificate, error)) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// dialLeaf 握手并返回叶子证书指纹。serverName 为空时模拟 IP 直连（不发 SNI），这时只能跳过主机名校验，
// 但仍用 VerifyConnection 校验证书链来自测试 CA。
func dialLeaf(t *testing.T, addr string, ca *testCA, serverName string) (*tls.Conn, string) {
	t.Helper()
	cfg := &tls.Config{RootCAs: ca.pool, ServerName: serverName, MinVersion: tls.VersionTLS12}
	if serverName == "" {
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			opts := x509.VerifyOptions{Roots: ca.pool, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
	}
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sum := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].Raw)
	return conn, hex.EncodeToString(sum[:])
}

// echo 在已建立的连接上收发一次，证明连接仍然可用。
func echo(t *testing.T, conn *tls.Conn, msg string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write on established connection: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read on established connection: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

// tls13Hello 是直接调 GetCertificate 回调时用的空 ClientHello（SNI 为空）。
var tls13Hello = tls.ClientHelloInfo{}
