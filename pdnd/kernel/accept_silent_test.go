package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// ============================================================
//  静默连接不得堵住 Accept
// ============================================================

// 回归：VMess+TLS 曾在 acceptLoop 里同步握手，读超时 10 秒。一个只建 TCP、
// 不发 ClientHello 的连接就能让整个入站 10 秒内接不进任何新连接——公网上
// 扫描器每秒都在干这件事。这里先占住一条静默连接，再要求第二个正常 TLS
// 客户端在 1 秒内完成握手；VLESS、Trojan 走同一张表，防止退回去。
func TestTLSInboundSilentConnDoesNotBlockAccept(t *testing.T) {
	for _, protocol := range []string{"vmess", "vless", "trojan"} {
		t.Run(protocol, func(t *testing.T) {
			addr, adapter := startSilentTestTLSInbound(t, protocol)

			silent, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer silent.Close()

			start := time.Now()
			raw, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			client := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec -- 测试证书是临时生成的。
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			err = client.HandshakeContext(context.Background())
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("静默连接在前时第二个 TLS 握手失败（耗时 %v）：%v", elapsed, err)
			}
			if elapsed > time.Second {
				t.Fatalf("静默连接在前时第二个 TLS 握手耗时 %v，应远小于 10 秒握手超时", elapsed)
			}

			// 握手中的连接也归 Close 管：Close 必须立刻关掉它，而不是等它的
			// 握手超时自己到点。
			closed := make(chan error, 1)
			go func() { closed <- adapter.Close() }()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("Close 被握手中的静默连接拖住")
			}
			_ = silent.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := silent.Read(make([]byte, 1)); err == nil {
				t.Fatal("Close 之后静默连接仍可读")
			} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("Close 没有关掉握手中的静默连接")
			}
		})
	}
}

func startSilentTestTLSInbound(t *testing.T, protocol string) (string, Adapter) {
	t.Helper()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	port := reserveTCPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: protocol, Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "tcp", "tls": true, "cert_path": certPath, "key_path": keyPath,
	}}}
	adapter, err := NewDefaultAdapterRegistry().New(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return fmt.Sprintf("127.0.0.1:%d", port), adapter
}

// REALITY 握手 worker 有 15 秒截止时间，但 Close 不能等它到点：握手中的原始
// 连接要被 Close 关掉，Accept 立刻返回，静默连接也立刻看到 EOF。
func TestRealityListenerCloseKillsHandshakingConn(t *testing.T) {
	// 伪装目标收下连接但不说话：REALITY 握手一开始就会连它，连不上会立刻
	// 失败，测不到「握手卡住」。
	dest, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	var destConns []net.Conn
	var destMu sync.Mutex
	go func() {
		for {
			c, err := dest.Accept()
			if err != nil {
				return
			}
			destMu.Lock()
			destConns = append(destConns, c)
			destMu.Unlock()
		}
	}()
	defer func() {
		destMu.Lock()
		defer destMu.Unlock()
		for _, c := range destConns {
			_ = c.Close()
		}
	}()
	var key [32]byte
	var shortID [8]byte
	listener, err := ListenReality("tcp", "127.0.0.1:0", RealityServerConfig{
		Dest:        dest.Addr().String(),
		ServerNames: map[string]bool{"example.com": true},
		PrivateKey:  key[:],
		ShortIDs:    map[[8]byte]bool{shortID: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 1)
	listener.SetHandshakeErrorHandler(func(_ net.Addr, err error) { reported <- err })
	silent, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		listener.mu.RLock()
		n := len(listener.handshaking)
		listener.mu.RUnlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("静默连接没有进入握手 worker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept()
		accepted <- err
	}()
	start := time.Now()
	_ = listener.Close()
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("Close 之后 Accept 应返回错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 之后 Accept 被握手中的连接拖住")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close 到 Accept 返回用了 %v", elapsed)
	}
	_ = silent.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := silent.Read(make([]byte, 1)); err == nil {
		t.Fatal("Close 之后静默连接仍可读")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("Close 没有关掉握手中的静默连接")
	}
	select {
	case err := <-reported:
		t.Fatalf("Close 打断的握手不该当作握手失败上报：%v", err)
	default:
	}
}

// serverTLSHandshake 自带截止时间：对端只连不说话时按 timeout 失败，不会无限挂着。
func TestServerTLSHandshakeTimesOutOnSilentPeer(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	config, _, err := loadInboundTLSConfig(map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	start := time.Now()
	_, err = serverTLSHandshake(context.Background(), server, config, 100*time.Millisecond)
	if err == nil {
		t.Fatal("静默对端的握手应当失败")
	}
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("握手错误 = %v，期望超时", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("握手用了 %v 才超时", elapsed)
	}
}

// 握手成功后截止时间必须清掉，否则会话会在 10 秒后被莫名掐断。
func TestServerTLSHandshakeClearsDeadline(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	config, _, err := loadInboundTLSConfig(map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath})
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		c := tls.Client(client, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec -- 测试证书是临时生成的。
		if c.Handshake() == nil {
			time.Sleep(300 * time.Millisecond)
			_, _ = c.Write([]byte("x"))
		}
	}()
	tlsConn, err := serverTLSHandshake(context.Background(), server, config, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := tlsConn.Read(buf); err != nil {
		t.Fatalf("握手后超过原截止时间的读失败：%v", err)
	}
}

func TestInboundHTTPServerBoundsHandshake(t *testing.T) {
	server := newInboundHTTPServer(nil, 64<<10)
	if server.ReadHeaderTimeout != inboundHandshakeTimeout {
		t.Fatalf("ReadHeaderTimeout = %v，期望 %v（net/http 拿它给 TLS 握手限时）", server.ReadHeaderTimeout, inboundHandshakeTimeout)
	}
	if server.ReadTimeout != 0 || server.WriteTimeout != 0 {
		t.Fatal("ReadTimeout / WriteTimeout 会掐断长流，不能设")
	}
}
