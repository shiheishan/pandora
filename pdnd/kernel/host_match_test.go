package kernel

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestHostMatchesIgnoresPortButNotHostname(t *testing.T) {
	cases := []struct {
		request, want string
		ok            bool
	}{
		{"node.example.com", "node.example.com", true},
		{"node.example.com:8443", "node.example.com", true},
		{"NODE.example.com:443", "node.example.com", true},
		{" node.example.com:80 ", "node.example.com", true},
		{"[2001:db8::1]:8443", "2001:db8::1", true},
		{"[2001:db8::1]", "2001:db8::1", true},
		{"[2001:db8::1]:8443", "[2001:db8::1]", true},
		{"198.51.100.7:10080", "198.51.100.7", true},
		// 没配 host：不检查
		{"anything.example:1", "", true},
		{"", "", true},
		// 配了 host：主机名不同一律拒，端口去掉也救不回来
		{"cdn.example.com:443", "node.example.com", false},
		{"node.example.com.evil.example:443", "node.example.com", false},
		{"", "node.example.com", false},
		{"[2001:db8::2]:443", "2001:db8::1", false},
	}
	for _, c := range cases {
		if got := requestHostMatches(c.request, c.want); got != c.ok {
			t.Errorf("requestHostMatches(%q, %q) = %v, want %v", c.request, c.want, got, c.ok)
		}
	}
}

func TestHTTPUpgradeAcceptsHostWithPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://node.example.com:8443/up", nil)
	req.Host = "node.example.com:8443"
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	if !validHTTPUpgradeRequest("/up", "node.example.com", req) {
		t.Fatal("httpupgrade rejected a Host that only differs by port")
	}
	req.Host = "cdn.example.com:8443"
	if validHTTPUpgradeRequest("/up", "node.example.com", req) {
		t.Fatal("httpupgrade accepted a foreign Host")
	}
}

func TestXHTTPServerAcceptsHostWithPort(t *testing.T) {
	server := XHTTPServer{Config: XHTTPConfig{Host: "node.example.com", UplinkHTTPMethod: http.MethodPost},
		Handler: func(context.Context, XHTTPSession) error { return nil }}
	for _, c := range []struct {
		host     string
		rejected bool
	}{{"node.example.com:8443", false}, {"cdn.example.com:8443", true}} {
		req := httptest.NewRequest(http.MethodPut, "http://"+c.host+"/x", nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		// 方法不对时回 405：能走到这一步说明 Host 检查已经放行
		hostRejected := rec.Code == http.StatusNotFound
		if hostRejected != c.rejected {
			t.Fatalf("host %q: status=%d body=%q, rejected=%v want %v", c.host, rec.Code, rec.Body.String(), hostRejected, c.rejected)
		}
	}
}

// 真起一个 WebSocket / gRPC 入站，用带端口的 Host 发请求：以前这里一律 404。
func TestNativeHTTPFrontEndsAcceptHostWithPort(t *testing.T) {
	type frontEnd struct {
		name  string
		serve func(net.Listener) (*http.Server, error)
		req   string
	}
	ctx := func() context.Context { return context.Background() }
	onConn := func(context.Context, net.Conn) {}
	fronts := []frontEnd{
		{"websocket", func(ln net.Listener) (*http.Server, error) {
			return serveNativeWebSocket(ln, "/ws", "node.example.com", ctx, onConn)
		}, "GET /ws HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n"},
		{"httpupgrade", func(ln net.Listener) (*http.Server, error) {
			return serveNativeHTTPUpgrade(ln, "/up", "node.example.com", ctx, onConn)
		}, "GET /up HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"},
		{"grpc", func(ln net.Listener) (*http.Server, error) {
			return serveNativeGRPC(ln, "/svc/Tun", "node.example.com", 16<<20, true, onConn)
		}, "POST /svc/Tun HTTP/1.1\r\nHost: %s\r\nContent-Type: application/grpc\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"},
	}
	for _, fe := range fronts {
		t.Run(fe.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server, err := fe.serve(ln)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			status := func(host string) int {
				conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := fmt.Fprintf(conn, fe.req, host); err != nil {
					t.Fatal(err)
				}
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				return resp.StatusCode
			}
			// 放行后各自的状态码不同（WebSocket 缺握手头回 400、Upgrade 回 101、
			// gRPC 回 200），关键是不再是 404
			if code := status("node.example.com:8443"); code == http.StatusNotFound {
				t.Fatalf("Host with port was rejected as 404")
			}
			if code := status("cdn.example.com:8443"); code != http.StatusNotFound {
				t.Fatalf("foreign Host got %d, want 404", code)
			}
		})
	}
}
