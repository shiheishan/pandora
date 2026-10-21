package kernel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// 入站 HTTP 的空闲回收（inboundHTTPIdleTimeout）：握完不开流的 h2 连接、等下一个请求的
// HTTP/1 keep-alive 连接到时被关；有活动流时不计时。这些用例改包级变量，不能 t.Parallel。

const idleTestTimeout = 200 * time.Millisecond

func shortenInboundHTTPIdle(t *testing.T) {
	t.Helper()
	old := inboundHTTPIdleTimeout
	inboundHTTPIdleTimeout = idleTestTimeout
	t.Cleanup(func() { inboundHTTPIdleTimeout = old })
}

// startIdleTestGRPC 起一个 TLS gRPC 入站；流的处理函数一直读到流结束，不主动收尾。
func startIdleTestGRPC(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert := probeTestCert(t, "localhost")
	srv, err := serveNativeGRPC(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}}), "/svc/Tun", "", 16<<20,
		func(_ context.Context, c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// openH2Client 握 TLS、发客户端前言与 SETTINGS，交回帧读写器。
func openH2Client(t *testing.T, addr string) (*tls.Conn, *http2.Framer) {
	t.Helper()
	conn := dialH2TLS(t, addr)
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	fr := http2.NewFramer(conn, conn)
	if err := fr.WriteSettings(); err != nil {
		t.Fatal(err)
	}
	return conn, fr
}

// readH2Until 读帧直到 stop 返回真、读出错或到 within；顺手回服务端的 SETTINGS。
// 交回让 stop 为真的那一帧（没有则 nil）与读错误。
func readH2Until(conn net.Conn, fr *http2.Framer, within time.Duration, stop func(http2.Frame) bool) (http2.Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(within))
	for {
		f, err := fr.ReadFrame()
		if err != nil {
			return nil, err
		}
		if s, ok := f.(*http2.SettingsFrame); ok && !s.IsAck() {
			if err := fr.WriteSettingsAck(); err != nil {
				return nil, err
			}
		}
		if stop(f) {
			return f, nil
		}
	}
}

func isGoAway(f http2.Frame) bool { _, ok := f.(*http2.GoAwayFrame); return ok }

// requireH2IdleClose 要求连接在空闲上限附近被关：先收到 GOAWAY(NO_ERROR) 或直接 EOF。
func requireH2IdleClose(t *testing.T, name string, conn net.Conn, fr *http2.Framer, idleSince time.Time) {
	t.Helper()
	f, err := readH2Until(conn, fr, 3*time.Second, isGoAway)
	if err == nil {
		if code := f.(*http2.GoAwayFrame).ErrCode; code != http2.ErrCodeNo {
			t.Fatalf("%s：空闲关闭的 GOAWAY 错误码=%v，要 NO_ERROR", name, code)
		}
		_, err = readH2Until(conn, fr, 3*time.Second, func(http2.Frame) bool { return false })
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("%s：空闲连接没有被服务端关闭：%v", name, err)
	}
	if elapsed := time.Since(idleSince); elapsed < idleTestTimeout/2 {
		t.Fatalf("%s：%v 就被关了，早于空闲上限 %v，关闭另有原因", name, elapsed, idleTestTimeout)
	}
}

func TestInboundH2IdleConnWithoutStreamsClosed(t *testing.T) {
	shortenInboundHTTPIdle(t)
	naiveAddr, _ := startProbeInbound(t, "naive", nil, nil)
	for name, addr := range map[string]string{"grpc": startIdleTestGRPC(t), "naive": naiveAddr} {
		start := time.Now()
		conn, fr := openH2Client(t, addr)
		requireH2IdleClose(t, name, conn, fr, start)
	}
}

func TestInboundH2ActiveStreamOutlivesIdleTimeout(t *testing.T) {
	shortenInboundHTTPIdle(t)
	conn, fr := openH2Client(t, startIdleTestGRPC(t))
	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	for _, f := range [][2]string{{":method", "POST"}, {":scheme", "https"}, {":path", "/svc/Tun"}, {":authority", "localhost"},
		{"content-type", "application/grpc"}, {"te", "trailers"}} {
		_ = enc.WriteField(hpack.HeaderField{Name: f[0], Value: f[1]})
	}
	if err := fr.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := readH2Until(conn, fr, 2*time.Second, func(f http2.Frame) bool {
		h, ok := f.(*http2.HeadersFrame)
		return ok && h.StreamID == 1
	}); err != nil {
		t.Fatalf("等流 1 的响应头：%v", err)
	}
	// 流开着、两边都不发数据，熬过几倍空闲上限：不许收到 GOAWAY，也不许断。
	f, err := readH2Until(conn, fr, 4*idleTestTimeout, isGoAway)
	if f != nil || !isTimeoutErr(err) {
		t.Fatalf("有活动流时连接被关了：帧=%v 错误=%v", f, err)
	}
	if err := fr.WritePing(false, [8]byte{'a', 'l', 'i', 'v', 'e'}); err != nil {
		t.Fatal(err)
	}
	if _, err := readH2Until(conn, fr, 2*time.Second, func(f http2.Frame) bool {
		if isGoAway(f) {
			t.Fatal("有活动流时收到 GOAWAY")
		}
		p, ok := f.(*http2.PingFrame)
		return ok && p.IsAck()
	}); err != nil {
		t.Fatalf("熬过空闲上限后 PING 没有回应：%v", err)
	}
	// 流结束后空闲才开始计时，到时照常关。
	if err := fr.WriteRSTStream(1, http2.ErrCodeCancel); err != nil {
		t.Fatal(err)
	}
	requireH2IdleClose(t, "流结束后", conn, fr, time.Now())
}

func TestInboundHTTP1KeepAliveIdleClosed(t *testing.T) {
	shortenInboundHTTPIdle(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := serveNativeWebSocket(ln, "/ws", "", func() context.Context { return context.Background() }, func(_ context.Context, c net.Conn) { _ = c.Close() })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, "GET /other HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.Close {
		t.Fatalf("第一个响应就要求关连接，测不到 keep-alive：%s", resp.Status)
	}
	start := time.Now()
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("keep-alive 空闲连接没有被服务端关闭：%v", err)
	}
	if elapsed := time.Since(start); elapsed < idleTestTimeout/2 {
		t.Fatalf("%v 就被关了，早于空闲上限 %v", elapsed, idleTestTimeout)
	}
}
