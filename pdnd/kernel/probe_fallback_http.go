package kernel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
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
			// HTTP/2 的 CONNECT 没有路径；转成 HTTP/1.1 时给一个 "/"，让回落
			// 站点按它自己的方式拒绝 CONNECT，而不是由 Transport 当隧道处理。
			if pr.Out.Method == http.MethodConnect && pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
		},
		Transport:    transport,
		ErrorLog:     neutralHTTPErrorLog,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) },
		ModifyResponse: func(resp *http.Response) error {
			resp.Body = &cappedReadCloser{rc: resp.Body, remaining: probeFallbackMaxDownload}
			return nil
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), probeFallbackMaxDuration)
		defer cancel()
		req = req.WithContext(ctx)
		if req.Body != nil {
			req.Body = http.MaxBytesReader(w, req.Body, probeFallbackMaxUpload)
		}
		proxy.ServeHTTP(w, req)
	})
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
