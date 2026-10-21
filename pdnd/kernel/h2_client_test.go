package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
)

// h2cTestTransport 是只讲明文 HTTP/2（prior knowledge）的客户端，URL 用 http://。
// dial 为空时按请求里的地址直连；REALITY 等用 dial 交回握好的连接。
func h2cTestTransport(dial func(ctx context.Context, network, address string) (net.Conn, error)) *http.Transport {
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true)
	return &http.Transport{Protocols: p, DialContext: dial}
}

// h2TLSTestTransport 是只讲 TLS 上 HTTP/2 的客户端，不验证书。
func h2TLSTestTransport() *http.Transport {
	p := new(http.Protocols)
	p.SetHTTP2(true)
	return &http.Transport{Protocols: p, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- 测试证书。
}

// h2OverConn 在已握好、协商出 h2 的 TLS 连接上发请求：Transport 第一次拨号交出 conn，
// 之后再拨报错（一条连接只用一次）。
func h2OverConn(conn *tls.Conn) *http.Transport {
	p := new(http.Protocols)
	p.SetHTTP2(true)
	var used atomic.Bool
	return &http.Transport{Protocols: p, DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		if used.Swap(true) {
			return nil, errors.New("h2OverConn 的连接已用过")
		}
		return conn, nil
	}}
}
