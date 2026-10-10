package kernel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 响应头对齐上游 Xray（transport/internet/splithttp/hub.go，v1.260327.0）：
//   - host、path 都对上之后，每个响应（含出错）都带 CORS 头与随机长度的
//     X-Padding（100–1000 个 'X'，HPACK / QPACK 下长度不变）；
//   - 下行（GET 下行与 stream-one）带 X-Accel-Buffering: no、Cache-Control:
//     no-store、Content-Type: text/event-stream；stream-up 带前两个；packet-up
//     只在请求体为空时带 Cache-Control: no-store；其余不设 Content-Type；
//   - OPTIONS 预检回 200；host、path 对不上回不带填充的 404。

func newPaddingTestServer(t *testing.T, raw map[string]any) XHTTPServer {
	t.Helper()
	c, err := ParseXHTTPConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	return XHTTPServer{Config: c, Handler: func(_ context.Context, session XHTTPSession) error {
		if session.Kind == XHTTPRequestDuplex {
			_, _ = io.Copy(io.Discard, session.Body)
		}
		return nil
	}}
}

func serveRecorded(server XHTTPServer, method, target string, body string, header http.Header) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for k, v := range header {
		req.Header[k] = v
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func assertXPadding(t *testing.T, name string, h http.Header) int {
	t.Helper()
	padding := h.Get("X-Padding")
	if len(padding) < xhttpPaddingMin || len(padding) > xhttpPaddingMax || strings.Trim(padding, "X") != "" {
		t.Fatalf("%s：X-Padding 长度 %d、内容应全是 X，实际 %q", name, len(padding), padding)
	}
	if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("%s：Access-Control-Allow-Origin=%q", name, got)
	}
	return len(padding)
}

func TestXHTTPResponseHeadersMatchXray(t *testing.T) {
	server := newPaddingTestServer(t, map[string]any{"path": "/xhttp", "host": "edge.example"})
	const base = "https://edge.example/xhttp/"

	down := serveRecorded(server, http.MethodGet, base+"sess/", "", nil)
	assertXPadding(t, "下行", down.Header())
	for key, want := range map[string]string{"Content-Type": "text/event-stream", "X-Accel-Buffering": "no", "Cache-Control": "no-store"} {
		if got := down.Header().Get(key); got != want {
			t.Fatalf("下行 %s=%q，期望 %q", key, got, want)
		}
	}

	one := serveRecorded(server, http.MethodPost, base, "x", nil)
	assertXPadding(t, "stream-one", one.Header())
	if got := one.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("stream-one Content-Type=%q", got)
	}

	packet := serveRecorded(server, http.MethodPost, base+"sess/0/", "payload", nil)
	assertXPadding(t, "packet-up", packet.Header())
	if got := packet.Header().Get("Content-Type"); got != "" {
		t.Fatalf("packet-up 不应设 Content-Type，实际 %q", got)
	}
	if got := packet.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("带请求体的 packet-up 不设 Cache-Control（Xray 同此），实际 %q", got)
	}

	up := serveRecorded(server, http.MethodPost, base+"sess2/", "x", nil)
	assertXPadding(t, "stream-up", up.Header())
	if up.Header().Get("X-Accel-Buffering") != "no" || up.Header().Get("Cache-Control") != "no-store" || up.Header().Get("Content-Type") != "" {
		t.Fatalf("stream-up 头 %v", up.Header())
	}

	// 出错响应同样带填充（Xray 在校验之前就写好了这两个头）。
	bad := serveRecorded(server, http.MethodPost, base+"sess/notanumber/", "x", nil)
	if bad.Code < 400 || bad.Body.Len() != 0 {
		t.Fatalf("元数据无效 code=%d body=%q", bad.Code, bad.Body.String())
	}
	assertXPadding(t, "出错响应", bad.Header())
}

func TestXHTTPPaddingLengthIsRandom(t *testing.T) {
	server := newPaddingTestServer(t, map[string]any{"path": "/xhttp"})
	seen := make(map[int]bool)
	for i := 0; i < 32; i++ {
		seen[assertXPadding(t, "下行", serveRecorded(server, http.MethodGet, "https://edge.example/xhttp/s/", "", nil).Header())] = true
	}
	if len(seen) < 8 {
		t.Fatalf("32 次请求只出现 %d 种填充长度", len(seen))
	}
}

func TestXHTTPOptionsPreflight(t *testing.T) {
	server := newPaddingTestServer(t, map[string]any{"path": "/xhttp"})
	header := http.Header{}
	header.Set("Origin", "https://app.example")
	header.Set("Access-Control-Request-Method", "POST")
	header.Set("Access-Control-Request-Headers", "content-type")
	resp := serveRecorded(server, http.MethodOptions, "https://edge.example/xhttp/s/0/", "", header)
	if resp.Code != http.StatusOK || resp.Body.Len() != 0 {
		t.Fatalf("OPTIONS code=%d body=%q", resp.Code, resp.Body.String())
	}
	for key, want := range map[string]string{
		"Access-Control-Allow-Origin":  "https://app.example",
		"Access-Control-Allow-Methods": "POST",
		"Access-Control-Allow-Headers": "content-type",
	} {
		if got := resp.Header().Get(key); got != want {
			t.Fatalf("OPTIONS %s=%q，期望 %q", key, got, want)
		}
	}
	if p := resp.Header().Get("X-Padding"); len(p) < xhttpPaddingMin {
		t.Fatalf("OPTIONS 应带 X-Padding，实际 %q", p)
	}
}

func TestXHTTPHostOrPathMismatchIsBare404(t *testing.T) {
	server := newPaddingTestServer(t, map[string]any{"path": "/xhttp", "host": "edge.example"})
	for name, target := range map[string]string{
		"host 不符": "https://other.example/xhttp/s/0/",
		"path 不符": "https://edge.example/other/s/0/",
	} {
		resp := serveRecorded(server, http.MethodPost, target, "x", nil)
		if resp.Code != http.StatusNotFound || resp.Body.Len() != 0 {
			t.Fatalf("%s：code=%d body=%q", name, resp.Code, resp.Body.String())
		}
		if resp.Header().Get("X-Padding") != "" || resp.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s：404 不应带填充与 CORS 头，实际 %v", name, resp.Header())
		}
	}
}
