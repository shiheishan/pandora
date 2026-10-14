package kernel

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestXHTTPSessionReapedWithoutDownlink（审查 X7）：会话建起来却始终没接上下行 GET
// 时按 xhttpUnattachedGrace 回收，不等协议层 10 秒的请求头截止。
func TestXHTTPSessionReapedWithoutDownlink(t *testing.T) {
	old := xhttpUnattachedGrace
	xhttpUnattachedGrace = 200 * time.Millisecond
	defer func() { xhttpUnattachedGrace = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base, adapter := startAutoXHTTPVLESS(t, ctx, 7603, uuid.New())
	transport := h2cTransport()
	defer transport.CloseIdleConnections()
	// 只发 seq 1：会话建起来，请求头永远不到。
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/xhttp/lonely/1/", bytes.NewReader([]byte("x")))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		adapter.xhttpBroker.mu.Lock()
		left := len(adapter.xhttpBroker.sessions)
		adapter.xhttpBroker.mu.Unlock()
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("没接下行的会话 3 秒后还在（%d 个）", left)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestXHTTPPacketConnReadDeadline（审查 X7）：会话型连接的读截止要生效，清掉已设的
// 截止视为认证完成（只触发一次）。
func TestXHTTPPacketConnReadDeadline(t *testing.T) {
	up, _ := NewXHTTPPacketQueue(4)
	down, _ := NewXHTTPPacketQueue(4)
	conn := newXHTTPPacketConn(context.Background(), &XHTTPPacketDuplex{Uplink: up, Downlink: down})
	auths := 0
	conn.onAuth = func() { auths++ }
	_ = conn.SetReadDeadline(time.Time{}) // 没设过截止：不算认证
	_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	start := time.Now()
	if _, err := conn.Read(make([]byte, 8)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("读截止没生效：%v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("读截止用了 %v", took)
	}
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_ = conn.SetReadDeadline(time.Time{})
	if auths != 1 {
		t.Fatalf("认证回调触发了 %d 次", auths)
	}
}

// TestXHTTPGetUplinkPackets（审查 X6）：uplink_http_method=GET 时，带序号的 GET 是
// 上行包、负载在请求头或 Cookie 里；原先显式 packet-up 节点把所有 GET 都当下行，
// 上行全坏。
func TestXHTTPGetUplinkPackets(t *testing.T) {
	for _, mode := range []string{"packet-up", "auto"} {
		for _, placement := range []string{"header", "cookie"} {
			c, err := ParseXHTTPConfig(map[string]any{"path": "/x", "mode": mode, "uplink_http_method": "GET", "uplink_data_placement": placement})
			if err != nil {
				t.Fatal(err)
			}
			var got XHTTPSession
			server := XHTTPServer{Config: c, Handler: func(_ context.Context, s XHTTPSession) error { got = s; return nil }}
			payload := bytes.Repeat([]byte("get-uplink|"), 40)
			encoded := base64.RawURLEncoding.EncodeToString(payload)
			req := httptest.NewRequest(http.MethodGet, "https://edge.example/x/s1/3/", nil)
			half := len(encoded) / 2
			if placement == "header" {
				req.Header.Set("data-0", encoded[:half])
				req.Header.Set("data-1", encoded[half:])
			} else {
				req.AddCookie(&http.Cookie{Name: "data_0", Value: encoded[:half]})
				req.AddCookie(&http.Cookie{Name: "data_1", Value: encoded[half:]})
			}
			server.ServeHTTP(httptest.NewRecorder(), req)
			if got.Kind != XHTTPRequestPacket || got.Seq != "3" || !bytes.Equal(got.Payload, payload) {
				t.Fatalf("%s/%s：kind=%d seq=%q payload=%q", mode, placement, got.Kind, got.Seq, got.Payload)
			}
			server.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "https://edge.example/x/s1/", nil))
			if got.Kind != XHTTPRequestDownlink {
				t.Fatalf("%s/%s：不带序号的 GET 应是下行，得到 %d", mode, placement, got.Kind)
			}
		}
	}
	if _, err := ParseXHTTPConfig(map[string]any{"path": "/x", "uplink_http_method": "GET"}); err == nil {
		t.Fatal("GET 上行配 body 放置应被拒")
	}
}
