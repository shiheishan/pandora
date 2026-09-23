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
	"net/http"
	"net/http/httputil"
	"net/url"

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
		return &httputil.ReverseProxy{
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
		}, nil

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
	if v, ok := node["status_code"].(float64); ok {
		m.StatusCode = int(v)
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
