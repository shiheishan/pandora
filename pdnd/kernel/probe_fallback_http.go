package kernel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// probeFallback 是一个入站的回落配置：addr 为空表示没配，由中性页面接住。
type probeFallback struct {
	addr    string
	handler http.Handler
}

func newProbeFallback(addr string) *probeFallback {
	return &probeFallback{addr: addr, handler: newProbeFallbackHTTPHandler(addr)}
}

// ServeHTTP 让 HTTP 承载的入站（Naive）直接把请求交给回落。
func (f *probeFallback) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if f == nil || f.handler == nil {
		neutralHTTPHandler.ServeHTTP(w, req)
		return
	}
	f.handler.ServeHTTP(w, req)
}

// serveConn 是 Trojan / AnyTLS 认证失败后的统一出口。prefix 是认证判定之前
// 已经从 conn 读走的字节。阻塞到会话结束；不关 conn，由调用方关。
//
//   - TLS 协商出 h2：由本地 HTTP/2 服务接住，请求交给 handler（配了回落就按
//     HTTP/1.1 反代过去）。这样回落目标不需要支持 h2c。
//   - 其余：配了回落就把字节原样转发过去（回落站点自己的响应特征原样保留），
//     没配或拨不通就由中性 HTTP/1.x 页面接住。
func (f *probeFallback) serveConn(ctx context.Context, conn net.Conn, prefix []byte, h2 bool) {
	if f == nil {
		f = newProbeFallback("")
	}
	if !acquireProbeFallbackSlot() {
		return
	}
	defer releaseProbeFallbackSlot()
	if h2 {
		serveFallbackHTTP(conn, prefix, true, f)
		return
	}
	if f.addr != "" {
		if target, err := dialProbeFallback(ctx, f.addr); err == nil {
			relayProbeFallback(conn, prefix, target)
			return
		}
	}
	serveFallbackHTTP(conn, prefix, false, neutralHTTPHandler)
}

// newProbeFallbackHTTPHandler 返回接住「不是本协议客户端」请求的 handler：
// addr 为空时是中性 404，否则反代到 addr（明文 HTTP，TLS 在节点上已终结）。
// 目标只来自配置；请求里的 Host、路径都不影响转发去哪。
func newProbeFallbackHTTPHandler(addr string) http.Handler {
	if addr == "" {
		return neutralHTTPHandler
	}
	target := &url.URL{Scheme: "http", Host: addr}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: probeFallbackDialTimeout}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       neutralHTTPIdleTimeout,
		MaxIdleConnsPerHost:   16,
		DisableCompression:    true,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// 像常见反代一样把原始 Host 带给回落站点。
			pr.Out.Host = pr.In.Host
		},
		Transport:    transport,
		ErrorLog:     neutralHTTPErrorLog,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) },
		ModifyResponse: func(resp *http.Response) error {
			resp.Body = &cappedReadCloser{rc: resp.Body, remaining: probeFallbackMaxDownload}
			return nil
		},
	}
	connect := &probeFallbackConnect{
		target: target,
		// 专用、不复用连接：CONNECT 之后的连接一律不回空闲池。
		transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: probeFallbackDialTimeout}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			DisableKeepAlives:     true,
			DisableCompression:    true,
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), probeFallbackMaxDuration)
		defer cancel()
		req = req.WithContext(ctx)
		if req.Method == http.MethodConnect {
			connect.ServeHTTP(w, req)
			return
		}
		if req.Body != nil {
			req.Body = http.MaxBytesReader(w, req.Body, probeFallbackMaxUpload)
		}
		proxy.ServeHTTP(w, req)
	})
}

// probeFallbackConnect 把 CONNECT 探测当一次普通请求交给回落站点，状态码、头和
// 正文都来自回落站点自己。
//
// 不交给 ReverseProxy：Go 1.26.9 起它对 CONNECT 一律走 ErrorHandler（CVE-2026-56866，
// golang/go#81740）。漏洞是 HTTP/1 Transport 把 CONNECT 的请求体不加分帧地写在
// 请求头后面，上游以 keep-alive 拒绝后连接回到空闲池，残留字节被当成下一条请求，
// 共享 Transport 的反代因此会把别人的响应串给下一位访客。回落若改回 pdnd 合成的
// 502，探测方一个 CONNECT 就能把节点和普通网站区分开。这里守住那次修复的意图：
//   - 只转请求头，不带请求体：探测方在 CONNECT 流上发的字节一个也不进回落站点，
//     请求以 Content-Length: 0 明确收尾；
//   - 一请求一连接：专用 Transport 关掉 keep-alive，连接用完即关，不进空闲池；
//   - 回落站点即使回 2xx，也只转这一个响应（带下行上限），不会建成隧道。
type probeFallbackConnect struct {
	target    *url.URL
	transport http.RoundTripper
}

func (c *probeFallbackConnect) ServeHTTP(w http.ResponseWriter, in *http.Request) {
	u := *c.target
	// HTTP/2 的 CONNECT 没有路径，HTTP/1.1 的 CONNECT 是 authority 形式：都给 "/"，
	// 让回落站点按它自己的方式应答 "CONNECT / HTTP/1.1"。
	u.Path, u.RawPath = "/", ""
	if strings.HasPrefix(in.URL.Path, "/") {
		u.Path, u.RawPath = in.URL.Path, in.URL.RawPath
	}
	out, err := http.NewRequestWithContext(in.Context(), http.MethodConnect, u.String(), nil)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	// 像常见反代一样把原始 Host 带给回落站点；请求头的处理与 ReverseProxy 一致：
	// 去掉逐跳头与客户端自带的转发头，客户端没给 User-Agent 就不补 Go 的默认值。
	out.Host = in.Host
	out.Header = in.Header.Clone()
	removeProbeHopHeaders(out.Header)
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		out.Header.Del(h)
	}
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header.Set("User-Agent", "")
	}
	resp, err := c.transport.RoundTrip(out)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeProbeHopHeaders(resp.Header)
	dst := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, &cappedReadCloser{rc: resp.Body, remaining: probeFallbackMaxDownload}); err != nil {
		// 与 ReverseProxy 拷正文出错时一样中止这条响应。
		panic(http.ErrAbortHandler)
	}
}

// probeHopHeaders 是 RFC 9110 的逐跳头（与 httputil.ReverseProxy 同一份）。
var probeHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// removeProbeHopHeaders 去掉逐跳头，以及 Connection 里点名的头。
func removeProbeHopHeaders(h http.Header) {
	for _, f := range h["Connection"] {
		for _, name := range strings.Split(f, ",") {
			if name = textproto.TrimString(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range probeHopHeaders {
		h.Del(name)
	}
}

// cappedReadCloser 在读满 remaining 字节后报错，让反代中止这条响应。
type cappedReadCloser struct {
	rc        io.ReadCloser
	remaining int64
}

func (c *cappedReadCloser) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, errProbeFallbackCapped
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.rc.Read(p)
	c.remaining -= int64(n)
	return n, err
}

func (c *cappedReadCloser) Close() error { return c.rc.Close() }
