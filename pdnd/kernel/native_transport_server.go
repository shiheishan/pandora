package kernel

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

// newInboundHTTPServer 是所有 HTTP 承载（WS、HTTP Upgrade、gRPC、XHTTP、
// Naive）共用的 http.Server 构造。
//
// ReadHeaderTimeout 不能省：net/http 拿它（与 ReadTimeout / WriteTimeout 中
// 最小的非零值）给 TLS 握手限时，三者全为零时握手不限时，只建 TCP 不说话
// 的连接会一直占着 goroutine 和 fd。它只管握手与请求头：读完请求头 net/http
// 就把截止时间清零，WebSocket / HTTP Upgrade 劫持后的会话、gRPC 与 XHTTP
// 的长流都不受影响；HTTP/2 的流也不用它。Accept 出错的退避由 Serve 自带。
func newInboundHTTPServer(handler http.Handler, maxHeaderBytes int) *http.Server {
	// ErrorLog 不能留空：net/http 缺省把握手失败连同完整对端 IP 写进标准库 log（inbound_http_errorlog.go）。
	return &http.Server{Handler: handler, MaxHeaderBytes: maxHeaderBytes, ReadHeaderTimeout: inboundHandshakeTimeout,
		ErrorLog: inboundHTTPErrorLog(connErrorReporter{})}
}

// serveNativeWebSocket and serveNativeHTTPUpgrade are shared HTTP front ends
// for protocol adapters whose wire decoder already speaks a net.Conn stream.
// The request context is intentionally not passed through: net/http cancels
// it when a hijacked/upgraded handler returns, while the adapter context owns
// the actual session lifetime.
func serveNativeWebSocket(listener net.Listener, path, host string, ctx func() context.Context, onConn func(context.Context, net.Conn)) (*http.Server, error) {
	if listener == nil || ctx == nil || onConn == nil {
		return nil, fmt.Errorf("native websocket server requires listener, context and handler")
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 32 * 1024, WriteBufferSize: 32 * 1024, CheckOrigin: func(req *http.Request) bool {
		return requestHostMatches(req.Host, host)
	}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL == nil || req.URL.Path != path || !requestHostMatches(req.Host, host) {
			http.NotFound(w, req)
			return
		}
		wsConn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		onConn(ctx(), newWebSocketNetConn(wsConn))
	})
	server := newInboundHTTPServer(handler, 64<<10)
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

func serveNativeHTTPUpgrade(listener net.Listener, path, host string, ctx func() context.Context, onConn func(context.Context, net.Conn)) (*http.Server, error) {
	if listener == nil || ctx == nil || onConn == nil {
		return nil, fmt.Errorf("native httpupgrade server requires listener, context and handler")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !validHTTPUpgradeRequest(path, host, req) {
			http.NotFound(w, req)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "httpupgrade unavailable", http.StatusHTTPVersionNotSupported)
			return
		}
		rawConn, rw, err := hijacker.Hijack()
		if err != nil {
			return
		}
		if err := writeHTTPUpgradeResponse(rw); err != nil {
			_ = rawConn.Close()
			return
		}
		onConn(ctx(), newHijackedNetConn(rawConn, rw.Reader))
	})
	server := newInboundHTTPServer(handler, 64<<10)
	go func() { _ = server.Serve(listener) }()
	return server, nil
}
