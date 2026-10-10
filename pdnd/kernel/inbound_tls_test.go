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
	"weak"
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
	if _, err := serverTLSHandshake(context.Background(), server, base, 5*time.Second); err != nil {
		t.Fatalf("服务端握手失败: %v", err)
	}
	// net.Pipe 不带缓冲：tls.Conn.Close 要写 close_notify，客户端不读就卡到写截止
	// （原写法每次握手白等 5 秒）。直接关底层连接，客户端的 Close 随之返回。
	_ = server.Close()
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
	if _, err := serverTLSHandshake(context.Background(), server, base, 5*time.Second); err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	_ = server.Close()
	if negotiated := <-done; negotiated != "" {
		t.Fatalf("客户端只宣告 h3 时 NegotiatedProtocol = %q，期望空", negotiated)
	}
}

// base 被回收后缓存条目随之删除（runtime.AddCleanup）。只盯本测试的那个键：
// 按总条目数判会被别的测试留下、此时恰好被回收的条目干扰。cleanup 在后台
// goroutine 里跑，每轮 GC 后让出一会儿。
func TestInboundWebALPNCacheDroppedAfterBaseGC(t *testing.T) {
	var key weak.Pointer[tls.Config]
	func() {
		base := testInboundTLSBaseConfig(t, "gc-drop")
		key = weak.Make(base)
		_ = inboundWebALPNConfig(base)
		if _, ok := inboundWebALPNCache.Load(key); !ok {
			t.Fatal("派生后缓存里没有这个 base")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		if _, ok := inboundWebALPNCache.Load(key); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("base 回收 5 秒后缓存条目仍在")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
