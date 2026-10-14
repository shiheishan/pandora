package hysteria2

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

// 认证前的资源放大（review-r3 #4，Pandora 改动的守卫）：hy2 是 HTTP/3 服务端，
// 认证前的请求按伪装站点处理。上游由 http3 每接一条双向流就起一个 goroutine
// 读请求头，HEADERS 帧声明多长就先分配多大（至多 1MB），又没有认证超时：只完成
// 握手、不认证的客户端开满流、每条只发半个 HEADERS，每条连接就能无限期挂住约
// 1032 个 goroutine 与数 MB 内存，连接数又不受约束。

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
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hy2.test"}, DNSNames: []string{"hy2.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type nopHandler struct{}

func (nopHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	_ = conn.Close()
}

func (nopHandler) NewPacketConnectionEx(_ context.Context, conn N.PacketConn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	_ = conn.Close()
}

const testAuthPassword = "hy2-test-password"

// preAuthTestOptions 是认证前约束的测试参数，零值用服务缺省。
type preAuthTestOptions struct {
	headerTimeout time.Duration
	idleTimeout   time.Duration
	masquerade    http.Handler
	handler       ServerHandler
}

func startPreAuthTestService(t testing.TB, opts preAuthTestOptions) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if opts.handler == nil {
		opts.handler = nopHandler{}
	}
	svc, err := NewService[string](ServiceOptions{
		Context: ctx, Logger: logger.NOP(),
		TLSConfig:            &testServerTLS{std: &tls.Config{Certificates: []tls.Certificate{testCertificate(t)}, MinVersion: tls.VersionTLS13}},
		PreAuthHeaderTimeout: opts.headerTimeout, PreAuthIdleTimeout: opts.idleTimeout,
		MasqueradeHandler: opts.masquerade, Handler: opts.handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.UpdateUsers([]string{"user"}, []string{testAuthPassword})
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

// dialPreAuth 建一条只握手、不认证的连接。不登记 Cleanup：测试要能放掉引用，
// 量连接关掉之后的堆。
func dialPreAuth(t testing.TB, addr string) *quic.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func serverResources() (goroutines int, heap uint64) {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return runtime.NumGoroutine(), ms.HeapInuse + ms.StackInuse
}

// openHalfHeaders 开至多 n 条双向流，每条只写 header（半个 HEADERS 帧），返回开出的条数。
func openHalfHeaders(conn *quic.Conn, n int, header []byte) int {
	opened := 0
	for i := 0; i < n; i++ {
		s, err := conn.OpenStream()
		if err != nil {
			break
		}
		if _, err := s.Write(header); err != nil {
			break
		}
		opened++
	}
	return opened
}

// 多条连接各开满只发半个 HEADERS 的流（审查员探针 TestProbeHy2PreAuthMultiConn
// 的正式版）：认证前每条连接同时在途的请求至多几十条，goroutine 不随流数涨；
// 读请求头到限时只拒那条流（REQUEST_INCOMPLETE），连接还在；此后没有在途请求，
// 空闲到点才关连接，goroutine 与堆回落。
func TestPreAuthHalfHeadersBoundedAndReclaimed(t *testing.T) {
	const headerTimeout = 1500 * time.Millisecond
	const idleTimeout = 2 * time.Second
	addr := startPreAuthTestService(t, preAuthTestOptions{headerTimeout: headerTimeout, idleTimeout: idleTimeout})
	baseG, baseHeap := serverResources()
	const conns = 4
	// HEADERS 帧头，声明 100 字节却只发 2 个：服务端读请求头的一方一直等。
	half := []byte{0x01, 0x40, 0x64, 0x00, 0x00}
	clients := make([]*quic.Conn, conns)
	firstStreams := make([]*quic.Stream, conns)
	opened := 0
	start := time.Now()
	for i := range clients {
		clients[i] = dialPreAuth(t, addr)
		s, err := clients[i].OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(half); err != nil {
			t.Fatal(err)
		}
		firstStreams[i] = s
		opened += 1 + openHalfHeaders(clients[i], 1100, half)
	}
	time.Sleep(300 * time.Millisecond)
	g, heap := serverResources()
	extraG, extraHeap := g-baseG, int64(heap)-int64(baseHeap)
	t.Logf("%d 条连接共开出 %d 条流；服务端多出 goroutine %d、堆+栈 %.1f MB（%v）", conns, opened, extraG, float64(extraHeap)/(1<<20), time.Since(start))
	if time.Since(start) >= headerTimeout {
		t.Fatalf("开流用了 %v，量不到读请求头限时之前的占用", time.Since(start))
	}
	// 每条连接认证前同时在途至多几十条请求，另有控制流与 QPACK 流各一两个
	// goroutine：4 条连接合计不超过 4×64。改前每条连接约 1032 个。
	if extraG > 4*64 {
		t.Fatalf("认证前多出 goroutine %d 个", extraG)
	}
	if extraHeap > 32<<20 {
		t.Fatalf("认证前堆+栈多出 %.1f MB", float64(extraHeap)/(1<<20))
	}
	// 读请求头到限时：每条连接最先开的那条流被拒（REQUEST_INCOMPLETE），连接仍在。
	for i, s := range firstStreams {
		_ = s.SetReadDeadline(start.Add(headerTimeout + 2*time.Second))
		var one [1]byte
		_, err := s.Read(one[:])
		var streamErr *quic.StreamError
		if !errors.As(err, &streamErr) || streamErr.ErrorCode != quic.StreamErrorCode(http3.ErrCodeRequestIncomplete) {
			t.Fatalf("连接 %d：只发半个请求头的流到读头限时应被拒（REQUEST_INCOMPLETE），得到 %v", i, err)
		}
	}
	rejectedAt := time.Since(start)
	if rejectedAt < headerTimeout-200*time.Millisecond {
		t.Fatalf("读请求头限时 %v，流在 %v 就被拒", headerTimeout, rejectedAt)
	}
	for i, c := range clients {
		if c.Context().Err() != nil {
			t.Fatalf("连接 %d：读请求头超时只该拒那条流，连接却在 %v 被关", i, time.Since(start))
		}
	}
	// 没有在途请求之后空闲到点关连接：不早于 读头限时 + 空闲时限。
	for i, c := range clients {
		select {
		case <-c.Context().Done():
		case <-time.After(headerTimeout + idleTimeout + 3*time.Second - time.Since(start)):
			t.Fatal("不认证、无在途请求的连接空闲到点仍未被关")
		}
		if i == 0 {
			if closedAt := time.Since(start); closedAt < headerTimeout+idleTimeout-300*time.Millisecond {
				t.Fatalf("连接在 %v 被关，早于 读头限时 %v + 空闲时限 %v", closedAt, headerTimeout, idleTimeout)
			}
		}
		clients[i] = nil // 放掉客户端一侧的流对象，堆里只剩服务端的
	}
	firstStreams = nil
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, heap = serverResources()
		if g-baseG <= 8 && int64(heap)-int64(baseHeap) <= 8<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("空闲关连接后仍多出 goroutine %d、堆+栈 %.1f MB", g-baseG, float64(int64(heap)-int64(baseHeap))/(1<<20))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// HEADERS 帧声明的长度在认证前受限：上游按声明长度先分配（至多 1MB）再读，64 条
// 只发帧头的流就是 64MB。
func TestPreAuthOversizedHeadersRejected(t *testing.T) {
	addr := startPreAuthTestService(t, preAuthTestOptions{})
	baseG, baseHeap := serverResources()
	conn := dialPreAuth(t, addr)
	defer conn.CloseWithError(0, "")
	// HEADERS 帧头，长度 1048576（4 字节 varint）。
	huge := []byte{0x01, 0x80, 0x10, 0x00, 0x00}
	opened := openHalfHeaders(conn, 64, huge)
	time.Sleep(300 * time.Millisecond)
	g, heap := serverResources()
	extraHeap := int64(heap) - int64(baseHeap)
	t.Logf("开出 %d 条声明 1MB 的 HEADERS；服务端多出 goroutine %d、堆+栈 %.1f MB", opened, g-baseG, float64(extraHeap)/(1<<20))
	if extraHeap > 16<<20 {
		t.Fatalf("认证前按声明长度分配：堆+栈多出 %.1f MB", float64(extraHeap)/(1<<20))
	}
}

// 认证前同时在途的请求超过上限时，多出的流被拒（REQUEST_REJECTED，客户端可重试），
// 不是排队、也不关连接；已在途的那些结束后名额回来。
func TestPreAuthInflightRequestsCapped(t *testing.T) {
	addr := startPreAuthTestService(t, preAuthTestOptions{})
	conn := dialPreAuth(t, addr)
	defer conn.CloseWithError(0, "")
	half := []byte{0x01, 0x40, 0x64, 0x00, 0x00}
	streams := make([]*quic.Stream, 0, 64)
	for i := 0; i < 64; i++ {
		s, err := conn.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(half); err != nil {
			t.Fatal(err)
		}
		streams = append(streams, s)
	}
	var rejectedCount atomic.Int32
	var wg sync.WaitGroup
	for _, s := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			var one [1]byte
			_, err := s.Read(one[:])
			var streamErr *quic.StreamError
			if errors.As(err, &streamErr) && streamErr.ErrorCode == quic.StreamErrorCode(http3.ErrCodeRequestRejected) {
				rejectedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	rejected := int(rejectedCount.Load())
	t.Logf("64 条半个 HEADERS 的流，被拒 %d 条", rejected)
	if rejected < 64-48 || rejected > 64-16 {
		t.Fatalf("被拒 %d 条，期望在途上限在 16–48 之间", rejected)
	}
	if conn.Context().Err() != nil {
		t.Fatal("超出在途上限不应关连接")
	}
}
