package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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

// h2TLSTestTransport 是只讲 TLS 上 HTTP/2 的客户端，不验证书。ALPN 没协商出 h2 时拨号报错：
// 只开 HTTP2 的 http.Transport 遇到没协商出 h2 的连接会悄悄讲 HTTP/1，测试就假绿了。
func h2TLSTestTransport() *http.Transport {
	p := new(http.Protocols)
	p.SetHTTP2(true)
	dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}}} //nolint:gosec -- 测试证书。
	return &http.Transport{Protocols: p, DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if err := requireH2(conn.(*tls.Conn)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}}
}

func requireH2(conn *tls.Conn) error {
	if got := conn.ConnectionState().NegotiatedProtocol; got != "h2" {
		return fmt.Errorf("ALPN 协商出 %q，不是 h2", got)
	}
	return nil
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
		if err := requireH2(conn); err != nil {
			return nil, err
		}
		return conn, nil
	}}
}
