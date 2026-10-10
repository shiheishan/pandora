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
	return startTestServiceWithLogger(t, handler, logger.NOP())
}

func startTestServiceWithLogger(t testing.TB, handler ServiceHandler, log logger.Logger) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc, err := NewService[string](ServiceOptions{
		Context: ctx, Logger: log,
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

func dialUnauthenticated(t testing.TB, addr string) *quic.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

// 恶意客户端（review-r3 #3 拆开的三个用例之一）：握手后不认证，只开双向流、每条写
// Connect 头和目标地址。守两层保护：每连接双向流上限（客户端最多开出 1024 条，
// 以前 1<<60 等于不限）与「认证前不 Accept 双向流」（以前每条流一个 goroutine 等认证）。
// 阈值写死数字，不引用常量。连接要撑到认证超时（3 秒）才被关，不能提前结束让量到
// 的占用平凡为 0。
func TestPreAuthBidiStreamFlood(t *testing.T) {
	addr := startTestService(t, &countingHandler{})
	baseG, baseHeap := serverResources()
	conn := dialUnauthenticated(t, addr)
	defer conn.CloseWithError(0, "")
	start := time.Now()
	connect := append([]byte{Version, CommandConnect}, mustAddr(t, "203.0.113.1:80")...)
	opened := 0
	for i := 0; i < 4000; i++ {
		s, err := conn.OpenStream()
		if err != nil {
			break
		}
		if _, err := s.Write(connect); err != nil {
			break
		}
		opened++
	}
	time.Sleep(500 * time.Millisecond)
	g, heap := serverResources()
	extraG, extraHeap := g-baseG, int64(heap)-int64(baseHeap)
	t.Logf("开出双向流 %d；服务端多出 goroutine %d、堆+栈 %.1f MB", opened, extraG, float64(extraHeap)/(1<<20))
	if conn.Context().Err() != nil {
		t.Fatalf("连接 %v 就被关了，早于认证超时：量到的占用不算数", time.Since(start))
	}
	if opened > 1024 {
		t.Fatalf("认证前开出 %d 条双向流，超过每连接 1024", opened)
	}
	if extraG > 16 {
		t.Fatalf("认证前为双向流起了 goroutine：多出 %d 个", extraG)
	}
	if extraHeap > 16<<20 {
		t.Fatalf("认证前堆+栈多出 %.1f MB", float64(extraHeap)/(1<<20))
	}
	select {
	case <-conn.Context().Done():
		if elapsed := time.Since(start); elapsed < 2500*time.Millisecond {
			t.Fatalf("连接 %v 被关，早于认证超时（3 秒）", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("不认证的连接在认证超时后仍未被关")
	}
}

// 只开单向流（review-r3 #3）：每条写 Packet 命令头、不带 FIN。认证前就在收流的
// goroutine 里逐条读头、暂存，不为每条流起 goroutine、不借 32KB 缓冲（以前每条流
// 各一个 goroutine 加 32KB 等认证）。暂存超过 64 条即关连接。
func TestPreAuthUniStreamFlood(t *testing.T) {
	addr := startTestService(t, &countingHandler{})
	baseG, baseHeap := serverResources()
	conn := dialUnauthenticated(t, addr)
	defer conn.CloseWithError(0, "")
	opened := 0
	for i := 0; i < 1000; i++ {
		s, err := conn.OpenUniStream()
		if err != nil {
			break
		}
		if _, err := s.Write([]byte{Version, CommandPacket}); err != nil {
			break
		}
		opened++
	}
	time.Sleep(300 * time.Millisecond)
	g, heap := serverResources()
	extraG, extraHeap := g-baseG, int64(heap)-int64(baseHeap)
	t.Logf("开出单向流 %d；服务端多出 goroutine %d、堆+栈 %.1f MB", opened, extraG, float64(extraHeap)/(1<<20))
	if extraG > 16 {
		t.Fatalf("认证前为单向流起了 goroutine：多出 %d 个", extraG)
	}
	if extraHeap > 8<<20 {
		t.Fatalf("认证前堆+栈多出 %.1f MB", float64(extraHeap)/(1<<20))
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("认证前暂存超过 64 条的连接 1 秒内没被关")
	}
}

// 大编号的 STREAM 帧（review-r3 #3）：QUIC 里开第 N 号流会隐式打开它之前的全部
// 流。客户端连开 N 条流、只在最后一条上写数据，服务端收到的就是一个编号 4(N-1)
// 的 STREAM 帧。流上限是 1024 时客户端开不到这么多；上限是 1<<60 时服务端要为
// 隐式打开的每条流建对象。量服务端堆。
func TestPreAuthHighStreamIDFrame(t *testing.T) {
	addr := startTestService(t, &countingHandler{})
	conn := dialUnauthenticated(t, addr)
	defer conn.CloseWithError(0, "")
	const n = 50000
	streams := make([]*quic.Stream, 0, 1100)
	opened := 0
	var last *quic.Stream
	for i := 0; i < n; i++ {
		s, err := conn.OpenStream()
		if err != nil {
			break
		}
		last = s
		opened++
		if len(streams) < cap(streams) {
			streams = append(streams, s)
		}
	}
	if last == nil {
		t.Fatal("一条流也开不出")
	}
	// 客户端一侧开好的流对象先放掉，堆里主要剩服务端的。
	streams = nil
	_, baseHeap := serverResources()
	if _, err := last.Write([]byte{Version, CommandConnect}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_, heap := serverResources()
	extraHeap := int64(heap) - int64(baseHeap)
	t.Logf("客户端开出 %d 条流，在最后一条上写数据；服务端堆+栈多出 %.1f MB", opened, float64(extraHeap)/(1<<20))
	if opened > 1024 {
		t.Fatalf("认证前开出 %d 条流，超过每连接 1024", opened)
	}
	if extraHeap > 4<<20 {
		t.Fatalf("一个大编号 STREAM 帧让服务端堆+栈多出 %.1f MB", float64(extraHeap)/(1<<20))
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

// 认证前暂存的单向流有上限：带 FIN 的流读完就把额度还给对端，不设上限时暂存表能
// 在认证超时之前涨到任意长。超出上限立即关连接，不等认证超时。
func TestPreAuthParkedUniStreamsAreCapped(t *testing.T) {
	addr := startTestService(t, &countingHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseWithError(0, "")
	start := time.Now()
	for i := 0; i < 256; i++ {
		s, err := conn.OpenUniStream()
		if err != nil {
			break
		}
		_, _ = s.Write([]byte{Version, CommandPacket})
		_ = s.Close()
	}
	select {
	case <-conn.Context().Done():
		t.Logf("暂存超限后 %v 被关", time.Since(start))
	case <-time.After(time.Second):
		t.Fatal("认证前暂存超限的连接 1 秒内没被关（只等到了认证超时）")
	}
}
