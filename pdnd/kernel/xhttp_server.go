package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	realitytls "github.com/aegispanel/nodeagent/internal/reality"
	realityhttp3 "github.com/aegispanel/nodeagent/internal/realityquic/http3"
	"github.com/apernet/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// XHTTPRequestKind 是一次 XHTTP 请求在会话里扮演的角色，由 XHTTPServer 按
// 节点配置的 mode 与请求形状判定，适配器只按它分派，不再自己看 mode。
type XHTTPRequestKind uint8

const (
	// XHTTPRequestDuplex：stream-one，请求体是上行、响应体是下行，一个请求就是一条连接。
	XHTTPRequestDuplex XHTTPRequestKind = iota
	// XHTTPRequestDownlink：带会话、不带序号的 GET，承载该会话的下行（packet-up / stream-up 共用）。
	XHTTPRequestDownlink
	// XHTTPRequestPacket：带会话与序号的上行包（packet-up）。
	XHTTPRequestPacket
	// XHTTPRequestStreamUp：带会话、不带序号的上行流（stream-up），请求体持续送上行。
	XHTTPRequestStreamUp
)

// XHTTPSession is the H1 session boundary. The handler owns protocol
// decoding and may stream from Body to Writer; no xray transport object is
// involved.
type XHTTPSession struct {
	ID      string
	Seq     string
	Kind    XHTTPRequestKind
	Request *http.Request
	Body    io.ReadCloser
	Writer  http.ResponseWriter
}

type xhttpDuplexConn struct {
	ctx    context.Context
	body   io.ReadCloser
	writer http.ResponseWriter
}

func newXHTTPDuplexConn(ctx context.Context, body io.ReadCloser, writer http.ResponseWriter) net.Conn {
	return &xhttpDuplexConn{ctx: ctx, body: body, writer: writer}
}

func (c *xhttpDuplexConn) Read(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	default:
	}
	return c.body.Read(p)
}

func (c *xhttpDuplexConn) Write(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	default:
	}
	n, err := c.writer.Write(p)
	if flusher, ok := c.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

func (c *xhttpDuplexConn) Close() error                     { return c.body.Close() }
func (c *xhttpDuplexConn) LocalAddr() net.Addr              { return xhttpAddr("pandora-xhttp") }
func (c *xhttpDuplexConn) RemoteAddr() net.Addr             { return xhttpAddr("xhttp-client") }
func (c *xhttpDuplexConn) SetDeadline(time.Time) error      { return nil }
func (c *xhttpDuplexConn) SetReadDeadline(time.Time) error  { return nil }
func (c *xhttpDuplexConn) SetWriteDeadline(time.Time) error { return nil }

type xhttpAddr string

func (a xhttpAddr) Network() string { return "xhttp" }
func (a xhttpAddr) String() string  { return string(a) }

type XHTTPHandler func(context.Context, XHTTPSession) error

// XHTTPServer is an HTTP/1.1 native session endpoint. HTTP/2 and HTTP/3 use
// the same ServeHTTP contract once their listener/ALPN front ends are added;
// they do not require a second protocol parser.
type XHTTPServer struct {
	Config  XHTTPConfig
	Handler XHTTPHandler
}

func (s XHTTPServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req == nil || req.URL == nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if s.Handler == nil {
		http.Error(w, "xhttp handler unavailable", http.StatusServiceUnavailable)
		return
	}
	if !requestHostMatches(req.Host, s.Config.Host) {
		http.Error(w, "host mismatch", http.StatusNotFound)
		return
	}
	kind, sessionID, seq, err := s.Config.classifyRequest(req)
	if err != nil {
		if err == errXHTTPMethodNotAllowed {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "invalid xhttp metadata", http.StatusBadRequest)
		return
	}
	// sc_max_each_post_bytes 只约束 packet-up 的单个上行包（与 Xray 一致）。
	// stream-one / stream-up 的请求体是整条连接的上行，套上它会把超过 1MB 的
	// 上传截断。
	if kind == XHTTPRequestPacket && s.Config.MaxPost.To > 0 && req.ContentLength > int64(s.Config.MaxPost.To) {
		http.Error(w, "xhttp body too large", http.StatusRequestEntityTooLarge)
		return
	}
	for key, value := range s.Config.Headers {
		if req.Header.Get(key) != value {
			http.Error(w, "header mismatch", http.StatusNotFound)
			return
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	// 上行包请求等包收下再回 200；其余（双工、下行、流式上行）先把响应头冲出去，
	// 客户端据此开始收下行或继续送上行。
	if flusher, ok := w.(http.Flusher); ok && kind != XHTTPRequestPacket {
		flusher.Flush()
	}
	body := req.Body
	if kind == XHTTPRequestPacket && s.Config.MaxPost.To > 0 {
		body = http.MaxBytesReader(w, req.Body, int64(s.Config.MaxPost.To))
	}
	if err := s.Handler(req.Context(), XHTTPSession{ID: sessionID, Seq: seq, Kind: kind, Request: req, Body: body, Writer: w}); err != nil {
		http.Error(w, fmt.Sprintf("xhttp session: %v", err), http.StatusBadGateway)
	}
}

// Serve starts the H1 server with the protocol's explicit header-size limit.
// The caller owns the returned server and should call Shutdown/Close.
func (s XHTTPServer) Serve(listener net.Listener) (*http.Server, error) {
	if listener == nil {
		return nil, fmt.Errorf("xhttp listener 不能为空")
	}
	if s.Handler == nil {
		return nil, fmt.Errorf("xhttp handler 不能为空")
	}
	server := newInboundHTTPServer(h2c.NewHandler(s, &http2.Server{}), s.Config.ServerMaxHeaderBytes)
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if session, ok := InspectRealityConn(conn); ok {
			return withRealitySession(ctx, session)
		}
		return ctx
	}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

// ServeH3 starts the same Pandora-owned XHTTP handler on an HTTP/3/QUIC
// packet listener. The caller owns the packet listener and the returned
// server, and must provide a certificate-capable TLS configuration.
func (s XHTTPServer) ServeH3(packet net.PacketConn, tlsConfig *tls.Config) (*http3.Server, error) {
	if packet == nil {
		return nil, fmt.Errorf("xhttp h3 packet listener 不能为空")
	}
	if s.Handler == nil {
		return nil, fmt.Errorf("xhttp handler 不能为空")
	}
	if tlsConfig == nil {
		return nil, fmt.Errorf("xhttp h3 TLS 配置不能为空")
	}
	server := &http3.Server{Handler: s, TLSConfig: http3.ConfigureTLSConfig(tlsConfig), MaxHeaderBytes: s.Config.ServerMaxHeaderBytes}
	go func() { _ = server.Serve(packet) }()
	return server, nil
}

// ServeH3Reality starts the Pandora-owned HTTP/3 front end with Pandora's
// REALITY QUIC handshake. The method is intentionally separate from ServeH3:
// a standard TLS Config is not wire-equivalent to REALITY and must never be
// silently substituted.
func (s XHTTPServer) ServeH3Reality(packet net.PacketConn, spec RealityServerConfig, dial RealityDialContext) (*realityhttp3.Server, error) {
	if packet == nil {
		return nil, fmt.Errorf("xhttp reality h3 packet listener cannot be nil")
	}
	if s.Handler == nil {
		return nil, fmt.Errorf("xhttp handler cannot be nil")
	}
	if err := validateRealityServerConfig(spec); err != nil {
		return nil, err
	}
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	tlsConfig := &realitytls.Config{
		DialContext: dial,
		Type:        "tcp",
		Dest:        spec.Dest,
		ServerNames: cloneRealityNames(spec.ServerNames),
		PrivateKey:  append([]byte(nil), spec.PrivateKey...),
		ShortIds:    cloneRealityShortIDs(spec.ShortIDs),
		MaxTimeDiff: spec.MaxTimeDiff,
		Xver:        spec.Xver,
	}
	server := &realityhttp3.Server{
		Handler:        s,
		TLSConfig:      realityhttp3.ConfigureTLSConfig(tlsConfig),
		MaxHeaderBytes: s.Config.ServerMaxHeaderBytes,
	}
	go func() { _ = server.Serve(packet) }()
	return server, nil
}
