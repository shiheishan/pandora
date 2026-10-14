package kernel

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/aegispanel/nodeagent/core"
)

// Naive 的 TLS 由 net/http 握手：握手失败要走 OnConnError（限流、截网段），
// 不能经标准库 log 落下完整对端 IP。
func TestNaiveTLSHandshakeFailureGoesToConnError(t *testing.T) {
	var stdlog bytes.Buffer
	log.SetOutput(&stdlog)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	spec := InboundSpec{Config: core.InboundConfig{Tag: "naive-errlog", Protocol: "naive", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath}}}
	adapterValue, err := newNaiveAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []ConnError
	hooks := AdapterHooks{DataPlane: &vlessTestPlane{}, OnConnError: func(ev ConnError) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapterValue.Start(ctx, spec, hooks); err != nil {
		t.Fatal(err)
	}
	defer adapterValue.Close()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Read(make([]byte, 512))
	_ = conn.Close()

	ok, why := waitFor(3*time.Second, func() (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range got {
			if ev.Stage == StageTLSHandshake && ev.Protocol == "naive" && ev.Remote != nil {
				return true, ""
			}
		}
		return false, fmt.Sprintf("收到 %+v", got)
	})
	if !ok {
		t.Fatalf("TLS 握手失败没到 OnConnError：%s", why)
	}
	if strings.Contains(stdlog.String(), "127.0.0.1") {
		t.Fatalf("握手失败经标准库 log 落下了完整对端 IP：%q", stdlog.String())
	}
}

// captureSlog 把 slog 默认 logger 换成写进缓冲的文本 handler，测试结束还原。
func captureSlog(t *testing.T) *syncBuffer {
	t.Helper()
	out := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(out, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(os.Stderr)
	})
	return out
}

// syncBuffer 是可并发读写的缓冲：服务端 goroutine 写日志，测试同时轮询。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 白名单反过来：只丢明确的对端噪声，panic 按 Error、其余按 WARN 记下；
// 行内 ip:port（含 [IPv6]:port）一律截到网段，完整地址不出现在日志里。
func TestInboundHTTPErrorLogLines(t *testing.T) {
	const v4, v6 = "198.51.100.23:40000", "[2001:db8:1:2::10]:40000"
	cases := []struct {
		line  string
		level string // "" 表示丢弃
		want  string
	}{
		{"http: panic serving " + v4 + ": boom\ngoroutine 1 [running]:", "ERROR", "198.51.100.0/24"},
		{"http2: panic serving " + v6 + ": boom\ngoroutine 1 [running]:", "ERROR", "2001:db8:1::/48"},
		{"http: Accept error: accept tcp 0.0.0.0:443: accept4: too many open files; retrying in 5ms", "WARN", "too many open files"},
		{"http: superfluous response.WriteHeader call from main.handler (x.go:1)", "WARN", "superfluous"},
		{"http2: server: error reading preface from client " + v4 + ": bogus greeting", "", ""},
		{"http2: server connection error from " + v6 + ": connection error: PROTOCOL_ERROR", "", ""},
		{"http2: server closing client connection: read tcp " + v4 + ": i/o timeout", "", ""},
		{"timeout waiting for SETTINGS frames from " + v4, "", ""},
		{"timeout waiting for PING response", "", ""},
		{"http2: received GOAWAY [FrameHeader GOAWAY len=8], starting graceful shutdown", "", ""},
		{"http: TLS handshake error from " + v4 + ": remote error: tls: bad certificate", "", ""},
	}
	for _, tc := range cases {
		out := captureSlog(t)
		inboundHTTPErrorLog(connErrorReporter{protocol: "naive"}).Print(tc.line)
		text := out.String()
		if tc.level == "" {
			if text != "" {
				t.Errorf("对端噪声不该进日志：%q → %q", tc.line, text)
			}
			continue
		}
		if !strings.Contains(text, "level="+tc.level) || !strings.Contains(text, tc.want) {
			t.Errorf("%q 应按 %s 记下且含 %q，得到 %q", tc.line, tc.level, tc.want, text)
		}
		for _, full := range []string{"198.51.100.23", "2001:db8:1:2::10", ":40000"} {
			if strings.Contains(text, full) {
				t.Errorf("日志里出现完整地址 %q：%q", full, text)
			}
		}
	}
}

// h2c 上 handler panic 也要记下（x/net/http2 的写法是「http2: panic serving」），
// 对端地址截网段。
func TestInboundHTTPServerLogsH2CPanic(t *testing.T) {
	out := captureSlog(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newInboundHTTPServer(h2c.NewHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("device-wiring-h2c-panic")
	}), &http2.Server{}), 64<<10)
	go func() { _ = server.Serve(ln) }()
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	if resp, err := client.Get("http://" + ln.Addr().String() + "/"); err == nil {
		_ = resp.Body.Close()
	}
	ok, why := waitFor(3*time.Second, func() (bool, string) {
		text := out.String()
		return strings.Contains(text, "level=ERROR") && strings.Contains(text, "device-wiring-h2c-panic"), text
	})
	if !ok {
		t.Fatalf("h2c 上的 handler panic 没记下：%q", why)
	}
	if text := out.String(); !strings.Contains(text, "127.0.0.0/24") || strings.Contains(text, "127.0.0.1:") {
		t.Fatalf("panic 日志的对端地址没截网段：%q", text)
	}
}

// temporaryAcceptListener 第一次 Accept 返回可重试的错误（fd 耗尽），之后返回已关闭。
type temporaryAcceptListener struct {
	net.Listener
	calls int
}

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string {
	return "accept tcp 127.0.0.1:443: accept4: too many open files"
}
func (temporaryAcceptError) Timeout() bool   { return false }
func (temporaryAcceptError) Temporary() bool { return true }

func (l *temporaryAcceptListener) Accept() (net.Conn, error) {
	l.calls++
	if l.calls == 1 {
		return nil, temporaryAcceptError{}
	}
	return nil, net.ErrClosed
}

// fd 耗尽时 net/http 写「http: Accept error: … too many open files; retrying」：
// 这是入站停止接客的唯一线索，必须按 WARN 记下。
func TestInboundHTTPServerLogsAcceptError(t *testing.T) {
	out := captureSlog(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	server := newInboundHTTPServer(http.NotFoundHandler(), 64<<10)
	_ = server.Serve(&temporaryAcceptListener{Listener: ln})
	if text := out.String(); !strings.Contains(text, "level=WARN") || !strings.Contains(text, "too many open files") {
		t.Fatalf("Accept error 没按 WARN 记下：%q", text)
	}
}
