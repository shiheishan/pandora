package tuic

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

	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

// 认证前的资源放大（Pandora 改动的守卫）：只完成 TLS 握手、不认证的客户端，
// 在认证超时（3 秒）之前狂开单向流与双向流，服务端不能为每条流起 goroutine、
// 分配读缓冲。改前每条流一个 goroutine，单向流再加 32KB 缓冲；流数上限是
// 1<<60，等于没有上限。

type testServerTLS struct{ std *tls.Config }

func (c *testServerTLS) ServerName() string                 { return c.std.ServerName }
func (c *testServerTLS) SetServerName(v string)             { c.std.ServerName = v }
func (c *testServerTLS) NextProtos() []string               { return c.std.NextProtos }
func (c *testServerTLS) SetNextProtos(v []string)           { c.std.NextProtos = v }
func (c *testServerTLS) STDConfig() (*tls.Config, error)    { return c.std, nil }
func (c *testServerTLS) Client(net.Conn) (aTLS.Conn, error) { return nil, net.ErrClosed }
func (c *testServerTLS) Clone() aTLS.Config                 { return &testServerTLS{std: c.std.Clone()} }
func (c *testServerTLS) Start() error                       { return nil }
func (c *testServerTLS) Close() error                       { return nil }
func (c *testServerTLS) Server(net.Conn) (aTLS.Conn, error) { return nil, net.ErrClosed }

func testCertificate(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tuic.test"}, DNSNames: []string{"tuic.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

var testUserUUID = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

const testUserPassword = "tuic-test-password"

func startTestService(t testing.TB, handler ServiceHandler) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc, err := NewService[string](ServiceOptions{
		Context: ctx, Logger: logger.NOP(),
		TLSConfig:   &testServerTLS{std: &tls.Config{Certificates: []tls.Certificate{testCertificate(t)}, NextProtos: []string{"h3"}, MinVersion: tls.VersionTLS13}},
		AuthTimeout: 3 * time.Second, Handler: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.UpdateUsers([]string{"user"}, [][16]byte{testUserUUID}, []string{testUserPassword})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(pc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close(); _ = pc.Close() })
	return pc.LocalAddr().String()
}

func serverResources() (goroutines int, heap uint64) {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return runtime.NumGoroutine(), ms.HeapInuse + ms.StackInuse
}

// 恶意客户端：握手后不认证，3 秒认证窗口内各开至多 attempts 条单向流
// （只写命令头 Packet）与双向流（写 Connect 头和目标地址），看服务端多出的
// goroutine 与堆。
func TestPreAuthStreamFloodDoesNotAmplify(t *testing.T) {
	addr := startTestService(t, &countingHandler{})
	baseG, baseHeap := serverResources()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")

	const attempts = 4000
	connect := append([]byte{Version, CommandConnect}, mustAddr(t, "203.0.113.1:80")...)
	uni, bidi := 0, 0
	for i := 0; i < attempts; i++ {
		s, err := conn.OpenUniStream()
		if err != nil {
			break
		}
		if _, err := s.Write([]byte{Version, CommandPacket}); err != nil {
			break
		}
		uni++
	}
	for i := 0; i < attempts; i++ {
		s, err := conn.OpenStream()
		if err != nil {
			break
		}
		if _, err := s.Write(connect); err != nil {
			break
		}
		bidi++
	}
	time.Sleep(time.Second)
	g, heap := serverResources()
	extraG := g - baseG
	extraHeap := int64(heap) - int64(baseHeap)
	t.Logf("开出单向流 %d、双向流 %d；服务端多出 goroutine %d、堆+栈 %.1f MB", uni, bidi, extraG, float64(extraHeap)/(1<<20))
	if extraG > 64 {
		t.Fatalf("认证前每条流起了 goroutine：多出 %d 个", extraG)
	}
	if extraHeap > 16<<20 {
		t.Fatalf("认证前堆+栈多出 %.1f MB", float64(extraHeap)/(1<<20))
	}
	// 认证超时到点，连接被关。
	select {
	case <-conn.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("不认证的连接在认证超时后仍未被关")
	}
}

func mustAddr(t testing.TB, s string) []byte {
	t.Helper()
	buffer := make([]byte, 0, 32)
	w := &sliceWriter{b: buffer}
	if err := AddressSerializer.WriteAddrPort(w, M.ParseSocksaddr(s)); err != nil {
		t.Fatal(err)
	}
	return w.b
}

type sliceWriter struct{ b []byte }

func (w *sliceWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// holdingHandler 收下 TCP 连接、不关（模拟长连接），供测试按需关。
type holdingHandler struct {
	countingHandler
	conns chan net.Conn
}

func (h *holdingHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	h.conns <- conn
}

// sing-quic（sing-box 1.13 同版本）客户端在流上限处的行为：OpenStream 不阻塞，
// 到上限立即报错；上限以内的并发 TCP 连接全部可用；关掉一条之后额度回来。
func TestClientStreamLimitBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	handler := &holdingHandler{conns: make(chan net.Conn, serverMaxIncomingStreams+8)}
	addr := startTestService(t, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := NewClient(ClientOptions{
		Context: ctx, Dialer: N.SystemDialer, ServerAddress: M.ParseSocksaddr(addr),
		TLSConfig: &testServerTLS{std: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}},
		UUID:      testUserUUID, Password: testUserPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := M.ParseSocksaddr("203.0.113.1:80")
	clientConns := make([]net.Conn, 0, serverMaxIncomingStreams)
	defer func() {
		for _, c := range clientConns {
			_ = c.Close()
		}
	}()
	for i := 0; i < serverMaxIncomingStreams; i++ {
		c, err := client.DialConn(ctx, destination)
		if err != nil {
			t.Fatalf("第 %d 条 TCP 连接打不开：%v", i+1, err)
		}
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatalf("第 %d 条写请求失败：%v", i+1, err)
		}
		clientConns = append(clientConns, c)
	}
	serverConns := make([]net.Conn, 0, serverMaxIncomingStreams)
	for len(serverConns) < serverMaxIncomingStreams {
		select {
		case c := <-handler.conns:
			serverConns = append(serverConns, c)
		case <-ctx.Done():
			t.Fatalf("服务端只收到 %d 条", len(serverConns))
		}
	}
	defer func() {
		for _, c := range serverConns {
			_ = c.Close()
		}
	}()
	over, err := client.DialConn(ctx, destination)
	if err == nil {
		_ = over.Close()
		t.Fatal("超过上限的 OpenStream 应立即报错（客户端不等额度）")
	}
	t.Logf("第 %d 条：%v", serverMaxIncomingStreams+1, err)
	// 关掉一条（两端都关，流才算结束），额度回来后能再开。
	_ = clientConns[0].Close()
	_ = serverConns[0].Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := client.DialConn(ctx, destination)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("关掉一条后额度没有回来：%v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
