package kernel

// 口令不对的 Naive CONNECT 交给回落站点时，状态码与响应特征必须来自回落站点
// 自己，同时守住 Go CVE-2026-56866 的修复意图：探测方在 CONNECT 后面带的字节
// 一个也不能进回落站点（否则会被当成下一条请求），连接也不复用。

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

const (
	connectProbeSmuggled = "GET /smuggled HTTP/1.1\r\nHost: decoy.example.com\r\n\r\n"
	connectProbePadding  = "!!!!!!!!!!!!!!!!"
	connectProbeAuth     = "Basic d3Jvbmc6d3Jvbmc="
	connectProbeBody     = "method not allowed"
)

// connectProbeSite 是一个裸 TCP 写的回落站点：对 CONNECT 回 405（keep-alive、
// 带自己的特征头），其余回 200；记下每条连接收到的全部原始字节和解析出的请求。
type connectProbeSite struct {
	addr string
	mu   sync.Mutex
	raw  []string
	reqs []*http.Request
}

func startConnectProbeSite(t *testing.T) *connectProbeSite {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	site := &connectProbeSite{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); site.serve(conn) }()
		}
	}()
	return site
}

func (s *connectProbeSite) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	var raw bytes.Buffer
	defer func() {
		s.mu.Lock()
		s.raw = append(s.raw, raw.String())
		s.mu.Unlock()
	}()
	br := bufio.NewReader(io.TeeReader(conn, &raw))
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()
		resp := "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\nsite"
		if req.Method == http.MethodConnect {
			resp = "HTTP/1.1 405 Method Not Allowed\r\nAllow: GET, HEAD\r\nX-Decoy-Site: 1\r\n" +
				"Content-Length: 18\r\n\r\n" + connectProbeBody
		}
		if _, err := io.WriteString(conn, resp); err != nil {
			return
		}
	}
}

// snapshot 等回落站点那边至少 conns 条连接收尾（Transport 用完即关），返回记录。
func (s *connectProbeSite) snapshot(t *testing.T, conns int) ([]string, []*http.Request) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		raw, reqs := append([]string(nil), s.raw...), append([]*http.Request(nil), s.reqs...)
		s.mu.Unlock()
		if len(raw) >= conns || time.Now().After(deadline) {
			return raw, reqs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertConnectProbeResponse(t *testing.T, label string, resp *http.Response) {
	t.Helper()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("X-Decoy-Site") != "1" ||
		resp.Header.Get("Allow") != "GET, HEAD" || string(body) != connectProbeBody {
		t.Fatalf("%s：应是回落站点的 405，实际 status=%d header=%v body=%q", label, resp.StatusCode, resp.Header, body)
	}
	if resp.Header.Get("Proxy-Authenticate") != "" {
		t.Fatalf("%s：带了 Proxy-Authenticate：%v", label, resp.Header)
	}
}

func TestNaiveConnectProbeGetsFallbackSiteResponse(t *testing.T) {
	site := startConnectProbeSite(t)
	addr, _ := startProbeInbound(t, "naive", map[string]any{"fallback": site.addr}, nil)

	// h2：CONNECT 流上紧跟一条走私请求。
	tr := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- 测试证书。
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodConnect, "https://"+addr, io.NopCloser(strings.NewReader(connectProbeSmuggled)))
	req.Host = "www.example.com:443"
	req.Header.Set("Padding", connectProbePadding)
	req.Header.Set("Proxy-Authorization", connectProbeAuth)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("h2 CONNECT：%v", err)
	}
	assertConnectProbeResponse(t, "h2 CONNECT", resp)
	_ = resp.Body.Close()

	// http/1.1：authority 形式的 CONNECT，请求头后面同样紧跟走私请求。
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}) //nolint:gosec -- 测试证书。
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	h1 := "CONNECT www.example.com:443 HTTP/1.1\r\nHost: www.example.com:443\r\n" +
		"Padding: " + connectProbePadding + "\r\nProxy-Authorization: " + connectProbeAuth + "\r\n\r\n" + connectProbeSmuggled
	if _, err := io.WriteString(conn, h1); err != nil {
		t.Fatal(err)
	}
	resp, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("h1 CONNECT：%v", err)
	}
	assertConnectProbeResponse(t, "h1 CONNECT", resp)
	_ = resp.Body.Close()

	raw, reqs := site.snapshot(t, 2)
	if len(reqs) != 2 || len(raw) != 2 {
		t.Fatalf("回落站点应恰好在 2 条连接上各收到 1 条请求：reqs=%d conns=%d raw=%q", len(reqs), len(raw), raw)
	}
	for i, r := range reqs {
		if r.Method != http.MethodConnect || r.RequestURI != "/" || r.Host != "www.example.com:443" {
			t.Fatalf("第 %d 条：%s %s Host=%s", i, r.Method, r.RequestURI, r.Host)
		}
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Padding") != connectProbePadding {
			t.Fatalf("第 %d 条请求头不对：%v", i, r.Header)
		}
	}
	for i, b := range raw {
		// 不带 Content-Length / Transfer-Encoding 的请求按 RFC 9112 没有请求体，
		// 原始字节应在请求头的空行处结束。
		if strings.Contains(b, "smuggled") || !strings.HasSuffix(b, "\r\n\r\n") ||
			strings.Contains(b, "Content-Length") || strings.Contains(b, "Transfer-Encoding") {
			t.Fatalf("第 %d 条连接：CONNECT 后面的字节不该进回落站点：%q", i, b)
		}
	}
}
