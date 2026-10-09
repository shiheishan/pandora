package sing

// Naive 的伪装回落。
//
// 上游 sing-box 没有给 Naive 做这个 —— 同一个仓库里 Hysteria2 有 Masquerade，
// Naive 没有。原因是 naiveproxy 官方部署方式是 Caddy + forward_proxy 插件，
// 回落由 Caddy 负责，节点端不管。
//
// 但那要求每台节点都装 Caddy 并配好证书与站点，对机场运维是实打实的负担。
// 既然 Hysteria2 的做法就在隔壁，把它搬到 Naive 上是自然的选择：
// 非法请求不再收到规整的 400/407，而是看到一个真实的网站响应，
// 主动探测拿不到「这里是代理」的判据。

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/aegispanel/nodeagent/internal/confnum"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// NaiveOptions 是我们自己的 Naive 入站配置。
//
// 之所以不直接用 option.NaiveInboundOptions：注册表允许任意 options 类型，
// 而配置对象是我们自己构造的（不走 JSON 反序列化），
// 因此可以干净地在上游结构外面加字段，不必 fork option 包。
type NaiveOptions struct {
	option.NaiveInboundOptions
	Masquerade *NaiveMasquerade `json:"masquerade,omitempty"`
}

// NaiveMasquerade 描述非法请求该看到什么。
// 三种模式与 Hysteria2 的 masquerade 对齐，配置写法可以互相迁移。
type NaiveMasquerade struct {
	// file / proxy / string
	Type string `json:"type"`

	// file：把一个本地目录当静态站点发布
	Directory string `json:"directory,omitempty"`

	// proxy：反代到一个真实网站
	//
	// RewriteHost 怎么选，实测过：
	//   - 反代到第三方站点（example.com 之类）必须开。不开的话上游收到的
	//     Host 是客户端连过来时用的域名，多数站点会直接 403 ——
	//     那反而成了一个显眼的异常特征。
	//   - 反代到自己的站点则不要开，保留原 Host 才能让日志、
	//     虚拟主机、证书这些都对得上。
	URL         string `json:"url,omitempty"`
	RewriteHost bool   `json:"rewrite_host,omitempty"`

	// string：直接回一段固定内容
	StatusCode int                 `json:"status_code,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Content    string              `json:"content,omitempty"`
}

// buildMasquerade 把配置翻译成一个 http.Handler。
// 返回 nil 表示不做伪装，退回到原来的「尽量断链」行为。
func buildMasquerade(m *NaiveMasquerade) (http.Handler, error) {
	if m == nil || m.Type == "" {
		return nil, nil
	}
	switch m.Type {
	case "file":
		if m.Directory == "" {
			return nil, E.New("masquerade file 模式缺少 directory")
		}
		return http.FileServer(http.Dir(m.Directory)), nil

	case "proxy":
		if m.URL == "" {
			return nil, E.New("masquerade proxy 模式缺少 url")
		}
		target, err := url.Parse(m.URL)
		if err != nil {
			return nil, E.Cause(err, "解析 masquerade url")
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(target)
				if !m.RewriteHost {
					r.Out.Host = r.In.Host
				}
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				// 上游挂了也不能暴露自己，回一个普通的网关错误
				w.WriteHeader(http.StatusBadGateway)
			},
		}
		connect := newMasqueradeConnect(target, m.RewriteHost)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				connect.ServeHTTP(w, r)
				return
			}
			proxy.ServeHTTP(w, r)
		}), nil

	case "string":
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			for k, values := range m.Headers {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			if m.StatusCode != 0 {
				w.WriteHeader(m.StatusCode)
			}
			_, _ = w.Write([]byte(m.Content))
		}), nil

	default:
		return nil, E.New("未知的 masquerade 类型: ", m.Type)
	}
}

// masqueradeConnect 把 CONNECT 探测当一次普通请求交给伪装站点，状态码、头和
// 正文都来自伪装站点自己。
//
// 不交给 ReverseProxy：Go 1.26.9 起它对 CONNECT 一律走 ErrorHandler（CVE-2026-56866，
// golang/go#81740：HTTP/1 Transport 把 CONNECT 的请求体不加分帧地写在请求头
// 后面，上游 keep-alive 拒绝后连接回到空闲池，残留字节被当成下一条请求）。改回
// 节点合成的 502，就成了「这里是代理」的判据。这里守住那次修复的意图：只转请求头、
// 不带请求体；专用 Transport 关掉 keep-alive、只讲 HTTP/1.1，连接用完即关；
// 上游即使回 2xx 也只转这一个响应，不建隧道。与 kernel/probe_fallback_http.go
// 的 probeFallbackConnect 同一口径（兼容内核不能引用 kernel，各留一份）。
type masqueradeConnect struct {
	target      *url.URL
	rewriteHost bool
	transport   http.RoundTripper
}

func newMasqueradeConnect(target *url.URL, rewriteHost bool) *masqueradeConnect {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	// 只讲 HTTP/1.1：HTTP/2 的 CONNECT 会被 Transport 当成隧道。
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return &masqueradeConnect{target: target, rewriteHost: rewriteHost, transport: transport}
}

func (c *masqueradeConnect) ServeHTTP(w http.ResponseWriter, in *http.Request) {
	u := *c.target
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	// HTTP/2 的 CONNECT 没有路径，HTTP/1.1 的 CONNECT 是 authority 形式；与
	// ReverseProxy 的 SetURL 一样拼上目标路径，空路径给 "/"。
	if strings.HasPrefix(in.URL.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/") + in.URL.Path
	} else if u.Path == "" {
		u.Path = "/"
	}
	out, err := http.NewRequestWithContext(in.Context(), http.MethodConnect, u.String(), nil)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if !c.rewriteHost {
		out.Host = in.Host
	}
	out.Header = in.Header.Clone()
	removeMasqueradeHopHeaders(out.Header)
	for _, h := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		out.Header.Del(h)
	}
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header.Set("User-Agent", "")
	}
	resp, err := c.transport.RoundTrip(out)
	if err != nil {
		// 上游挂了也不能暴露自己，回一个普通的网关错误
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeMasqueradeHopHeaders(resp.Header)
	dst := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// 与 ReverseProxy 拷正文出错时一样中止这条响应。
		panic(http.ErrAbortHandler)
	}
}

// removeMasqueradeHopHeaders 去掉 RFC 9110 的逐跳头（与 httputil.ReverseProxy
// 同一份），以及 Connection 里点名的头。
func removeMasqueradeHopHeaders(h http.Header) {
	for _, f := range h["Connection"] {
		for _, name := range strings.Split(f, ",") {
			if name = textproto.TrimString(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		h.Del(name)
	}
}

// parseMasquerade 从面板下发的 protocol_config 里读伪装配置。
func parseMasquerade(raw map[string]any) *NaiveMasquerade {
	if raw == nil {
		return nil
	}
	node, ok := raw["masquerade"].(map[string]any)
	if !ok {
		return nil
	}
	m := &NaiveMasquerade{}
	m.Type, _ = node["type"].(string)
	m.Directory, _ = node["directory"].(string)
	m.URL, _ = node["url"].(string)
	m.RewriteHost, _ = node["rewrite_host"].(bool)
	m.Content, _ = node["content"].(string)
	if v, ok := confnum.Int(node["status_code"]); ok {
		m.StatusCode = v
	}
	if hdrs, ok := node["headers"].(map[string]any); ok {
		m.Headers = make(map[string][]string, len(hdrs))
		for k, v := range hdrs {
			switch val := v.(type) {
			case string:
				m.Headers[k] = []string{val}
			case []any:
				for _, item := range val {
					if s, ok := item.(string); ok {
						m.Headers[k] = append(m.Headers[k], s)
					}
				}
			}
		}
	}
	if m.Type == "" {
		return nil
	}
	return m
}
