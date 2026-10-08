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

// testHTTP2Config 只在测试给了自定义 TLS 配置（假面板是 Go 的 HTTP/2 服务）时用。Go 客户端默认向对端宣告
// 最大帧 1 MiB，Go 服务端于是按 1 MiB 切帧，每条连接的读缓冲被撑到 1 MiB，两千条连接就是 2 GB。
// 生产里前面是 nginx，DATA 帧按 http2_chunk_size（缺省 8 KiB）切，读缓冲只有几十 KiB；
// 这里把宣告值压到协议下限 16 KiB，量到的内存才和生产同量级。
func testHTTP2Config() *http.HTTP2Config { return &http.HTTP2Config{MaxReadFrameSize: 16 << 10} }
