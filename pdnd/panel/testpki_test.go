package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/test/bufconn"
)

// testPKI 是测试用的面板 CA：照契约用 ECDSA P-256 自签根，签面板服务端证书与节点客户端证书。
type testPKI struct {
	caCert      *x509.Certificate
	caKey       *ecdsa.PrivateKey
	caPEM       []byte
	fingerprint string
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Pandora Node CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testPKI{
		caCert:      cert,
		caKey:       key,
		caPEM:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		fingerprint: CAFingerprint(cert),
	}
}

var testSerial atomic.Int64

func nextSerial() *big.Int { return big.NewInt(100 + testSerial.Add(1)) }

// serverCert 签一张面板服务端证书（SAN 是 hosts，IP 或域名），握手时发 [叶子, CA]。
func (p *testPKI) serverCert(t *testing.T, hosts ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: nextSerial(),
		Subject:      pkix.Name{CommonName: "pandora panel"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, p.caCert.Raw}, PrivateKey: key}
}

// signCSR 按契约签节点客户端证书：URI SAN spiffe://pandora/server/<serverID>，EKU clientAuth。
// 返回叶子的 PEM（客户端证书链只有叶子）。
func (p *testPKI) signCSR(t *testing.T, csrDER []byte, serverID string, notAfter time.Time) []byte {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	uri, _ := url.Parse("spiffe://pandora/server/" + serverID)
	tmpl := &x509.Certificate{
		SerialNumber: nextSerial(),
		Subject:      pkix.Name{CommonName: serverID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, csr.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// serverTLS 是假面板的 TLS 配置：TLS 1.3，客户端证书可选（Enroll 不带、Session 带）。
func (p *testPKI) serverTLS(t *testing.T, hosts ...string) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(p.caCert)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{p.serverCert(t, hosts...)},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    pool,
	}
}

// startFakeGRPC 在 bufconn 上起一个假面板（enroll、control 可为 nil），返回拨号函数。
func startFakeGRPC(t *testing.T, cfg *tls.Config, enroll nodev1.EnrollmentServiceServer, control nodev1.ControlServiceServer) Dialer {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
	if enroll != nil {
		nodev1.RegisterEnrollmentServiceServer(srv, enroll)
	}
	if control != nil {
		nodev1.RegisterControlServiceServer(srv, control)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }
}

// peerLeaf 取会话对端（节点）的客户端证书叶子；没带证书返回 nil。
func peerLeaf(ctx context.Context) *x509.Certificate {
	pr, ok := peer.FromContext(ctx)
	if !ok {
		return nil
	}
	info, ok := pr.AuthInfo.(credentials.TLSInfo)
	if !ok || len(info.State.PeerCertificates) == 0 {
		return nil
	}
	return info.State.PeerCertificates[0]
}
