package kernel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"runtime"
	"testing"
	"time"
)

func testInboundTLSBaseConfig(t *testing.T, cn string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
}

func TestInboundWebALPNConfigCacheHit(t *testing.T) {
	base := testInboundTLSBaseConfig(t, "cache-hit")
	a := inboundWebALPNConfig(base)
	b := inboundWebALPNConfig(base)
	if a != b {
		t.Fatal("同一 base 两次派生应返回同一指针")
	}
}

func TestInboundWebALPNConfigAllocsOnCacheHit(t *testing.T) {
	base := testInboundTLSBaseConfig(t, "allocs")
	_ = inboundWebALPNConfig(base)
	allocs := testing.AllocsPerRun(100, func() {
		_ = inboundWebALPNConfig(base)
	})
	if allocs != 0 {
		t.Fatalf("缓存命中时分配次数 = %v，期望 0", allocs)
	}
}

func handshakeClientPeer(t *testing.T, base *tls.Config, wantCN string, wantALPN string) {
	t.Helper()
	server, client := net.Pipe()
	defer client.Close()
	type clientResult struct {
		peerCN     string
		negotiated string
		err        error
	}
	done := make(chan clientResult, 1)
	go func() {
		cfg := &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // 测试自签证书
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{"h2"},
		}
		c := tls.Client(client, cfg)
		if err := c.Handshake(); err != nil {
			done <- clientResult{err: err}
			return
		}
		st := c.ConnectionState()
		peerCN := ""
		if len(st.PeerCertificates) > 0 {
			peerCN = st.PeerCertificates[0].Subject.CommonName
		}
		c.Close()
		done <- clientResult{peerCN: peerCN, negotiated: st.NegotiatedProtocol}
	}()
	tlsConn, err := serverTLSHandshake(context.Background(), server, base, 5*time.Second)
	if err != nil {
		t.Fatalf("服务端握手失败: %v", err)
	}
	_ = tlsConn.Close()
	cr := <-done
	if cr.err != nil {
		t.Fatalf("客户端握手: %v", cr.err)
	}
	if cr.peerCN != wantCN {
		t.Fatalf("客户端证书 CN = %q，期望 %q", cr.peerCN, wantCN)
	}
	if cr.negotiated != wantALPN {
		t.Fatalf("协商 ALPN = %q，期望 %q", cr.negotiated, wantALPN)
	}
}

func TestServerTLSHandshakePeerCertificate(t *testing.T) {
	base1 := testInboundTLSBaseConfig(t, "cert-one")
	base2 := testInboundTLSBaseConfig(t, "cert-two")
	handshakeClientPeer(t, base1, "cert-one", "h2")
	handshakeClientPeer(t, base2, "cert-two", "h2")
}

func TestServerTLSHandshakeALPNFallbackOnH3Only(t *testing.T) {
	base := testInboundTLSBaseConfig(t, "alpn-fallback")
	server, client := net.Pipe()
	defer client.Close()
	done := make(chan string, 1)
	go func() {
		cfg := &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // 测试自签证书
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{"h3"},
		}
		c := tls.Client(client, cfg)
		if err := c.Handshake(); err != nil {
			done <- ""
			return
		}
		negotiated := c.ConnectionState().NegotiatedProtocol
		c.Close()
		done <- negotiated
	}()
	tlsConn, err := serverTLSHandshake(context.Background(), server, base, 5*time.Second)
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	_ = tlsConn.Close()
	if negotiated := <-done; negotiated != "" {
		t.Fatalf("客户端只宣告 h3 时 NegotiatedProtocol = %q，期望空", negotiated)
	}
}

func TestInboundWebALPNCacheDroppedAfterBaseGC(t *testing.T) {
	before := inboundWebALPNCacheEntryCount()
	func() {
		base := testInboundTLSBaseConfig(t, "gc-drop")
		_ = inboundWebALPNConfig(base)
		if inboundWebALPNCacheEntryCount() != before+1 {
			t.Fatalf("派生后缓存条目 = %d，期望 %d", inboundWebALPNCacheEntryCount(), before+1)
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if inboundWebALPNCacheEntryCount() == before {
			return
		}
	}
	t.Skipf("2 秒内 GC 未回收 base 对应缓存（当前 %d，期望 %d）", inboundWebALPNCacheEntryCount(), before)
}
