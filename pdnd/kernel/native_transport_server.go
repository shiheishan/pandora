package kernel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

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
		return host == "" || strings.EqualFold(strings.TrimSpace(req.Host), host)
	}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL == nil || req.URL.Path != path || (host != "" && !strings.EqualFold(strings.TrimSpace(req.Host), host)) {
			http.NotFound(w, req)
			return
		}
		wsConn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		onConn(ctx(), newWebSocketNetConn(wsConn))
	})
	server := &http.Server{Handler: handler, MaxHeaderBytes: 64 << 10}
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
	server := &http.Server{Handler: handler, MaxHeaderBytes: 64 << 10}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}
