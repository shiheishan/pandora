package kernel

// 抗主动探测的回归测试：REALITY 认证不过转给 dest（与上游 XTLS/REALITY 对照），
// Trojan / AnyTLS / Naive 认证不过交给回落或中性页面，TCP 直连类 TLS 入站
// 宣告 h2 / http/1.1 且合法客户端照常连通。全部只用本机起的站点，不依赖外网。

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"github.com/google/uuid"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	utls "github.com/refraction-networking/utls"
	M "github.com/sagernet/sing/common/metadata"
	upstreamreality "github.com/xtls/reality"
	"golang.org/x/net/http2"
)

// probeResult 是一次探测在探测方眼里的全部可观测结果。
type probeResult struct {
	handshake bool
	alpn      string
	certCN    string
	status    int
	body      string
	header    http.Header
	elapsed   time.Duration
	err       error
}

// summary 只保留探测方能拿来比对的字段（不含耗时）。
func (r probeResult) summary() string {
	if !r.handshake {
		return "handshake-failed"
	}
	if r.err != nil {
		return fmt.Sprintf("cert=%s alpn=%s request-failed", r.certCN, r.alpn)
	}
	return fmt.Sprintf("cert=%s alpn=%s status=%d body=%q", r.certCN, r.alpn, r.status, r.body)
}

func probeTestCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startProbeDest 起一个本机 TLS 站点充当 REALITY 的 dest。
func startProbeDest(t *testing.T) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{probeTestCert(t, "dest.example.com")}, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "dest-site") }), ErrorLog: neutralHTTPErrorLog}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// startProbeFallbackSite 起一个明文 HTTP 站点充当回落目标，记下收到的路径。
func startProbeFallbackSite(t *testing.T) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var paths []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, "fallback-site")
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// probeTLSGet 以探测方身份握 TLS 后发一个 GET（协商出 h2 就讲 HTTP/2）。
func probeTLSGet(addr, sni string, alpn []string, path string) probeResult {
	var out probeResult
	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		out.err = err
		return out
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: alpn}) //nolint:gosec -- 探测方不验证书。
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := conn.Handshake(); err != nil {
		out.err = err
		return out
	}
	state := conn.ConnectionState()
	out.handshake, out.alpn, out.certCN = true, state.NegotiatedProtocol, state.PeerCertificates[0].Subject.CommonName
	start := time.Now()
	var resp *http.Response
	if state.NegotiatedProtocol == http2.NextProtoTLS {
		clientConn, err := (&http2.Transport{}).NewClientConn(conn)
		if err != nil {
			out.err = err
			return out
		}
		req, _ := http.NewRequest(http.MethodGet, "https://"+sni+path, nil)
		resp, err = clientConn.RoundTrip(req)
		if err != nil {
			out.err = err
			return out
		}
	} else {
		if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: Mozilla/5.0\r\n\r\n", path, sni); err != nil {
			out.err = err
			return out
		}
		resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			out.err = err
			out.elapsed = time.Since(start)
			return out
		}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	out.status, out.body, out.header, out.elapsed = resp.StatusCode, string(body), resp.Header, time.Since(start)
	return out
}

// probePlainHTTP 不握 TLS，直接发明文 HTTP，返回响应首行。
func probePlainHTTP(addr string) string {
	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "dial-failed"
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(raw, "GET / HTTP/1.1\r\nHost: dest.example.com\r\n\r\n")
	line, err := bufio.NewReader(raw).ReadString('\n')
	if line == "" {
		return "no-response: " + fmt.Sprint(err)
	}
	return strings.TrimSpace(line)
}

func assertNoBrand(t *testing.T, label string, result probeResult) {
	t.Helper()
	text := strings.ToLower(result.body)
	for name, values := range result.header {
		text += "\n" + strings.ToLower(name) + ": " + strings.ToLower(strings.Join(values, ","))
	}
	for _, brand := range []string{"pandora", "proxy-authenticate", "naive", "trojan", "anytls"} {
		if strings.Contains(text, brand) {
			t.Fatalf("%s 的响应带了品牌或代理特征 %q：%s", label, brand, text)
		}
	}
}

func startRealityProbeListener(t *testing.T, dest string, priv []byte) *RealityListener {
	t.Helper()
	spec := RealityServerConfig{Dest: dest, ServerNames: map[string]bool{"dest.example.com": true}, PrivateKey: priv, ShortIDs: map[[8]byte]bool{{0x12, 0x34}: true}}
	ln, err := ListenReality("tcp", "127.0.0.1:0", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln
}

// 审计实验 probe_audit_test.go 的仓库版：三种探测在 Pandora 与上游
// XTLS/REALITY 上的结果逐项一致，都是 dest 本身的握手与响应。
func TestRealityProbesFallBackToDestLikeUpstream(t *testing.T) {
	dest := startProbeDest(t)
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	ln := startRealityProbeListener(t, dest, priv)

	uln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uln.Close() })
	ucfg := &upstreamreality.Config{DialContext: (&net.Dialer{}).DialContext, Type: "tcp", Dest: dest, ServerNames: map[string]bool{"dest.example.com": true}, PrivateKey: priv, ShortIds: map[[8]byte]bool{{0x12, 0x34}: true}, SessionTicketsDisabled: true}
	go func() {
		for {
			conn, err := uln.Accept()
			if err != nil {
				return
			}
			go func() {
				if c, err := upstreamreality.Server(context.Background(), conn, ucfg); err == nil {
					_ = c.Close()
				}
			}()
		}
	}()

	baseline := probeTLSGet(dest, "dest.example.com", []string{"http/1.1"}, "/")
	if baseline.summary() != `cert=dest.example.com alpn=http/1.1 status=200 body="dest-site"` {
		t.Fatalf("dest 本身 = %s", baseline.summary())
	}
	for _, tc := range []struct{ name, sni string }{
		{"right-sni-plain-tls13", "dest.example.com"},
		{"wrong-sni", "other.example.org"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ours := probeTLSGet(ln.Addr().String(), tc.sni, []string{"http/1.1"}, "/")
			theirs := probeTLSGet(uln.Addr().String(), tc.sni, []string{"http/1.1"}, "/")
			if ours.summary() != theirs.summary() || ours.summary() != baseline.summary() {
				t.Fatalf("pandora=%s upstream=%s dest=%s", ours.summary(), theirs.summary(), baseline.summary())
			}
		})
	}
	t.Run("plain-http", func(t *testing.T) {
		ours, theirs, direct := probePlainHTTP(ln.Addr().String()), probePlainHTTP(uln.Addr().String()), probePlainHTTP(dest)
		if ours != theirs || ours != direct || !strings.Contains(ours, "400") {
			t.Fatalf("pandora=%q upstream=%q dest=%q", ours, theirs, direct)
		}
	})
}

// 回落中的连接也是监听器的：Close 要把它们一起关掉，而不是等 5 分钟上限。
func TestRealityFallbackRelayEndsOnListenerClose(t *testing.T) {
	dest := startProbeDest(t)
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	ln := startRealityProbeListener(t, dest, priv)
	raw, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: "dest.example.com", InsecureSkipVerify: true}) //nolint:gosec -- 探测方不验证书。
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := conn.Handshake(); err != nil {
		t.Fatalf("回落握手应成功：%v", err)
	}
	_ = ln.Close()
	if _, err := conn.Read(make([]byte, 1)); err == nil || isTimeoutErr(err) {
		t.Fatalf("Close 之后回落连接应被关掉，实际 %v", err)
	}
}

func isTimeoutErr(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

func startProbeInbound(t *testing.T, protocol string, extra map[string]any, plane DataPlane) (string, Adapter) {
	t.Helper()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	raw := map[string]any{"network": "tcp", "tls": true, "cert_path": certPath, "key_path": keyPath}
	for k, v := range extra {
		raw[k] = v
	}
	port := reserveTCPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: protocol, Listen: "127.0.0.1", Port: port, Raw: raw}}
	adapter, err := NewDefaultAdapterRegistry().New(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if plane == nil {
		plane = &vlessTestPlane{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	user := core.User{ID: 931, UUID: "probe-test-user"}
	if protocol == "vless" {
		user.UUID = uuid.NewString() // VLESS 只收 UUID 形式的用户
	}
	if err := adapter.AddUsers([]core.User{user}); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), adapter
}

// Trojan / AnyTLS / VLESS 认证不过：没配回落时回中性 404，配了回落时探测方看到的
// 就是回落站点（h1 原样转发、h2 经本地 HTTP/2 反代过去）。
func TestTLSInboundAuthFailureServesFallback(t *testing.T) {
	site, paths := startProbeFallbackSite(t)
	for _, protocol := range []string{"trojan", "anytls", "vless"} {
		t.Run(protocol, func(t *testing.T) {
			bare, _ := startProbeInbound(t, protocol, nil, nil)
			withFallback, _ := startProbeInbound(t, protocol, map[string]any{"fallback": site}, nil)
			for _, alpn := range [][]string{{"http/1.1"}, {"h2", "http/1.1"}} {
				want := alpn[0]
				got := probeTLSGet(bare, "localhost", alpn, "/index.html")
				if got.alpn != want || got.status != http.StatusNotFound || got.body != "404 page not found\n" {
					t.Fatalf("无回落 alpn=%v：%s", alpn, got.summary())
				}
				assertNoBrand(t, protocol+" 中性页面", got)
				path := "/probe-" + want
				got = probeTLSGet(withFallback, "localhost", alpn, path)
				if got.alpn != want || got.status != http.StatusOK || got.body != "fallback-site" {
					t.Fatalf("配回落 alpn=%v：%s", alpn, got.summary())
				}
				found := false
				for _, seen := range paths() {
					found = found || seen == "GET "+path
				}
				if !found {
					t.Fatalf("回落站点没收到 %s：%v", path, paths())
				}
			}
		})
	}
}

// 一个不足 58 字节的 GET 以前要挂满 10 秒读超时才被断；首字节不是十六进制
// 就该立刻判定失败。
func TestTrojanShortProbeIsAnsweredImmediately(t *testing.T) {
	addr, _ := startProbeInbound(t, "trojan", nil, nil)
	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}) //nolint:gosec -- 测试证书。
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: a\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("短探测应立刻拿到响应：%v（%v）", err, time.Since(start))
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || time.Since(start) > 2*time.Second {
		t.Fatalf("status=%d elapsed=%v", resp.StatusCode, time.Since(start))
	}
}

func TestTrojanProofValidation(t *testing.T) {
	valid := trojanPasswordProof("secret") + "\r\n"
	var proof [trojanProofLen]byte
	if err := readTrojanProof(strings.NewReader(valid), &proof); err != nil {
		t.Fatalf("合法口令行被拒：%v", err)
	}
	for name, input := range map[string]string{
		"http":       "GET / HTTP/1.1\r\n",
		"uppercase":  strings.ToUpper(valid[:56]) + "\r\n",
		"no-crlf":    valid[:56] + "\n\n",
		"h2-preface": "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n",
	} {
		if err := readTrojanProof(strings.NewReader(input), &proof); err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("%s：应判定为格式不对，实际 %v", name, err)
		}
	}
	// 合法前缀但数据不够：是读错误，不是格式错误。
	if err := readTrojanProof(strings.NewReader(valid[:20]), &proof); err == nil || strings.Contains(err.Error(), "malformed") {
		t.Fatalf("截断的口令行应报读错误，实际 %v", err)
	}
}

func TestParseProbeFallback(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:80", "localhost:8080", "[::1]:80", "site.internal:443"} {
		if got, err := parseProbeFallback(map[string]any{"fallback": ok}); err != nil || got == "" {
			t.Fatalf("%q 应合法：%q %v", ok, got, err)
		}
	}
	if got, err := parseProbeFallback(map[string]any{}); err != nil || got != "" {
		t.Fatalf("未配置应为空：%q %v", got, err)
	}
	for _, bad := range []any{"http://127.0.0.1:80", "127.0.0.1", "127.0.0.1:0", ":80", "a b:80", "host:80/path", 8080} {
		if _, err := parseProbeFallback(map[string]any{"fallback": bad}); err == nil {
			t.Fatalf("%v 应被拒", bad)
		}
	}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Port: 443, Raw: map[string]any{"fallback": "http://x:80"}}}
	if err := (&trojanAdapter{}).Validate(spec); err == nil {
		t.Fatal("trojan Validate 应拒绝非法 fallback")
	}
}

// TCP 直连类 TLS 入站宣告 h2 / http/1.1，但不能因此挡掉合法客户端：不给
// ALPN、给常见组合、给完全不重叠的 ALPN（Go 默认会以 no_application_protocol
// 拒绝），Trojan 会话都照常通。
func TestTrojanTLSClientsConnectWithAnyALPN(t *testing.T) {
	for _, tc := range []struct {
		name string
		alpn []string
		want string
	}{
		{"none", nil, ""},
		{"browser", []string{"h2", "http/1.1"}, "h2"},
		{"http1-only", []string{"http/1.1"}, "http/1.1"},
		{"no-overlap", []string{"h3"}, ""},
		// 真实客户端（Xray / mihomo / sing-box 的 uTLS）用的 Chrome 指纹。
		{"utls-chrome", nil, "h2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, target := startProxyEcho(t)
			defer upstream.Close()
			plane := &vlessTestPlane{target: M.SocksaddrFromNet(target.(*net.TCPAddr)).Unwrap()}
			addr, _ := startProbeInbound(t, "trojan", nil, plane)
			conn, negotiated, err := dialTrojanTLSClient(addr, tc.name, tc.alpn)
			if err != nil {
				t.Fatalf("握手失败：%v", err)
			}
			defer conn.Close()
			if negotiated != tc.want {
				t.Fatalf("ALPN=%q want %q", negotiated, tc.want)
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			header := []byte(trojanPasswordProof("probe-test-user") + "\r\n")
			header = append(header, 1, 1, 127, 0, 0, 1, 1, 187, '\r', '\n')
			payload := []byte("trojan-alpn-" + tc.name)
			if _, err := conn.Write(append(header, payload...)); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
				t.Fatalf("echo=%q err=%v", got, err)
			}
		})
	}
}

func dialTrojanTLSClient(addr, name string, alpn []string) (net.Conn, string, error) {
	if name != "utls-chrome" {
		conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: alpn}) //nolint:gosec -- 测试证书。
		if err != nil {
			return nil, "", err
		}
		return conn, conn.ConnectionState().NegotiatedProtocol, nil
	}
	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, "", err
	}
	conn := utls.UClient(raw, &utls.Config{ServerName: "localhost", InsecureSkipVerify: true}, utls.HelloChrome_Auto) //nolint:gosec -- 测试证书。
	if err := conn.Handshake(); err != nil {
		_ = raw.Close()
		return nil, "", err
	}
	return conn, conn.ConnectionState().NegotiatedProtocol, nil
}

// Naive：非 CONNECT 请求、不带 Padding 的 CONNECT、口令不对的 CONNECT 都交给
// 回落；没有 407、没有 realm；http/1.1 也照常服务。
func TestNaiveProbesServeFallback(t *testing.T) {
	site, paths := startProbeFallbackSite(t)
	for _, tc := range []struct {
		name     string
		extra    map[string]any
		status   int
		bodyWant string
	}{
		{"neutral", nil, http.StatusNotFound, "404 page not found\n"},
		{"fallback", map[string]any{"fallback": site}, http.StatusOK, "fallback-site"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, _ := startProbeInbound(t, "naive", tc.extra, nil)
			for _, alpn := range [][]string{{"http/1.1"}, {"h2", "http/1.1"}} {
				got := probeTLSGet(addr, "localhost", alpn, "/naive-"+tc.name)
				if got.alpn != alpn[0] || got.status != tc.status || got.body != tc.bodyWant {
					t.Fatalf("GET alpn=%v：%s", alpn, got.summary())
				}
				assertNoBrand(t, "naive GET", got)
			}
			for _, padding := range []string{"!!!!!!!!!!!!!!!!", ""} {
				tr := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- 测试证书。
				req, _ := http.NewRequest(http.MethodConnect, "https://"+addr, nil)
				req.Host = "www.example.com:443"
				if padding != "" {
					req.Header.Set("Padding", padding)
				}
				req.Header.Set("Proxy-Authorization", "Basic d3Jvbmc6d3Jvbmc=")
				resp, err := tr.RoundTrip(req)
				if err != nil {
					t.Fatalf("CONNECT padding=%q：%v", padding, err)
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
				_ = resp.Body.Close()
				result := probeResult{handshake: true, status: resp.StatusCode, body: string(body), header: resp.Header}
				if resp.StatusCode == http.StatusProxyAuthRequired || resp.Header.Get("Proxy-Authenticate") != "" || resp.StatusCode != tc.status {
					t.Fatalf("错误口令 CONNECT padding=%q：status=%d header=%v", padding, resp.StatusCode, resp.Header)
				}
				assertNoBrand(t, "naive CONNECT", result)
			}
			if tc.name == "fallback" && len(paths()) == 0 {
				t.Fatal("回落站点没收到任何请求")
			}
		})
	}
}

// naive 的响应 Padding 头按 forwardproxy 的规则随机：长度 [30, 62)，前 16 个
// 字符取自不被 HPACK Huffman 压缩的符号表，其余是 '~'。
func TestNaivePaddingHeaderIsRandomized(t *testing.T) {
	seen := map[string]bool{}
	lengths := map[int]bool{}
	for i := 0; i < 256; i++ {
		value := naivePaddingHeader()
		if len(value) < 30 || len(value) > 61 {
			t.Fatalf("长度 %d 越界：%q", len(value), value)
		}
		for j := 0; j < 16; j++ {
			if !strings.ContainsRune(naivePaddingSymbols, rune(value[j])) {
				t.Fatalf("第 %d 个字符 %q 不在符号表：%q", j, value[j], value)
			}
		}
		if strings.Trim(value[16:], "~") != "" {
			t.Fatalf("16 字符之后应全是 '~'：%q", value)
		}
		if strings.Contains(strings.ToLower(value), "pandora") {
			t.Fatalf("padding 带品牌：%q", value)
		}
		seen[value], lengths[len(value)] = true, true
	}
	if len(seen) < 250 || len(lengths) < 16 {
		t.Fatalf("随机性不足：%d 个不同值、%d 种长度", len(seen), len(lengths))
	}
}
