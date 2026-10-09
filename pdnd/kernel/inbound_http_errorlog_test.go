package kernel

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

// panic 不能被吞，地址截网段后按 Error 记；其余对端噪声丢弃。
func TestInboundHTTPErrorLogPanicAndNoise(t *testing.T) {
	var out bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(os.Stderr)
	})
	l := inboundHTTPErrorLog(connErrorReporter{protocol: "naive"})
	l.Printf("http: panic serving 198.51.100.23:40000: boom\ngoroutine 1 [running]:")
	l.Printf("http2: server: error reading preface from client 198.51.100.23:40000: EOF")
	l.Printf("http: TLS handshake error from 198.51.100.23:40000: EOF")
	text := out.String()
	if !strings.Contains(text, "level=ERROR") || !strings.Contains(text, "boom") {
		t.Fatalf("panic 没按 Error 记：%q", text)
	}
	if strings.Contains(text, "198.51.100.23") || !strings.Contains(text, "198.51.100.0/24") {
		t.Fatalf("panic 日志的对端地址没截网段：%q", text)
	}
	if strings.Count(text, "\n") != strings.Count(text, "level=ERROR") {
		t.Fatalf("对端噪声不该进日志：%q", text)
	}
}
