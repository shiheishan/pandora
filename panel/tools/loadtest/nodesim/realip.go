// [INPUT]: 依赖 net/http 的 RoundTripper
// [OUTPUT]: 包内提供 withRealIP：给一个模拟节点的全部请求带上它固定的 X-Real-IP
// [POS]: tools/loadtest/nodesim 的来源地址接线，signed.go 与 uniproxy.go 的 Transport 都经它包一层；
//        压测机经 nginx-realip.sh 被采信 X-Real-IP，nginx 的每 IP 限流与面板风控才按节点各算各的

package nodesim

import "net/http"

// realIPTransport 在转发前给请求补 X-Real-IP；地址来自清单（节点所在服务器的虚构公网 IP）。
type realIPTransport struct {
	base http.RoundTripper
	ip   string
}

// withRealIP 地址为空（旧清单）时原样返回，行为与以前一致。
func withRealIP(base http.RoundTripper, ip string) http.RoundTripper {
	if ip == "" {
		return base
	}
	return &realIPTransport{base: base, ip: ip}
}

func (t *realIPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("X-Real-IP", t.ip)
	return t.base.RoundTrip(r)
}

// CloseIdleConnections 让 http.Client.CloseIdleConnections 穿透到底层 Transport，收尾时连接照常释放。
func (t *realIPTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
