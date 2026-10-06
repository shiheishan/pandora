// [INPUT]: 依赖 profiling.go 的 Start、Server、requireLoopback、newMux，依赖 net/http 作客户端
// [OUTPUT]: 对外提供 TestStartIsOffForEmptyAddr、TestStartRejectsNonLoopbackBind、TestStartServesPprofOnLoopback、TestDefaultServeMuxIsNotTheDiagnosticRouter
// [POS]: platform/profiling 的行为测试：空地址不监听、非回环绑定被拒（直接验 requireLoopback，不真绑全部网卡）、回环端口能取到 /debug/pprof/ 且 Close 后端口释放

package profiling

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestStartIsOffForEmptyAddr(t *testing.T) {
	s, err := Start("", quietLog())
	if err != nil || s != nil {
		t.Fatalf("empty addr must leave pprof off, got %v, %v", s, err)
	}
	s.Close() // nil 上的 Close 必须是空操作，调用方无条件 defer
	if s.Addr() != "" {
		t.Fatal("a disabled server has no address")
	}
}

func TestStartRejectsNonLoopbackBind(t *testing.T) {
	// 不真去绑全部网卡（开发机的防火墙会弹窗），直接验 Start 用的那道闸
	for _, addr := range []net.Addr{
		&net.TCPAddr{IP: net.IPv4zero, Port: 6060},
		&net.TCPAddr{IP: net.IPv6unspecified, Port: 6060},
		&net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 6060},
		&net.UnixAddr{Name: "/tmp/pprof.sock", Net: "unix"},
	} {
		err := requireLoopback(addr)
		if err == nil || !strings.Contains(err.Error(), "回环") {
			t.Fatalf("%s must be rejected with a reason, got %v", addr, err)
		}
	}
	for _, addr := range []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 6060},
		&net.TCPAddr{IP: net.ParseIP("127.8.9.10"), Port: 6060},
		&net.TCPAddr{IP: net.IPv6loopback, Port: 6060},
	} {
		if err := requireLoopback(addr); err != nil {
			t.Fatalf("%s is loopback: %v", addr, err)
		}
	}
}

func TestStartServesPprofOnLoopback(t *testing.T) {
	s, err := Start("127.0.0.1:0", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap?debug=1", "/debug/pprof/cmdline"} {
		resp, err := client.Get("http://" + s.Addr() + path)
		if err != nil {
			s.Close()
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			s.Close()
			t.Fatalf("GET %s = %d with %d bytes", path, resp.StatusCode, len(body))
		}
	}
	// 诊断端口只有 pprof，没有任何别的路由
	resp, err := client.Get("http://" + s.Addr() + "/v1/me")
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("non-pprof path = %d, want 404", resp.StatusCode)
		}
	}

	addr := s.Addr()
	s.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port must be released after Close: %v", err)
	}
	ln.Close()
}

// 引入 net/http/pprof 会顺手往 DefaultServeMux 注册 /debug/pprof/。网关从不用它
// （server 包拒绝 nil Handler），这里钉住诊断端口自己的 mux 不是它。
func TestDefaultServeMuxIsNotTheDiagnosticRouter(t *testing.T) {
	if newMux() == http.DefaultServeMux {
		t.Fatal("the diagnostic router must be a private mux")
	}
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("private mux must serve the pprof index, got %d", rec.Code)
	}
}
