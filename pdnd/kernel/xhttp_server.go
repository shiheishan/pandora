package kernel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	realitytls "github.com/aegispanel/nodeagent/internal/reality"
	realityhttp3 "github.com/aegispanel/nodeagent/internal/realityquic/http3"
	"github.com/apernet/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// XHTTPSession is the H1 session boundary. The handler owns protocol
// decoding and may stream from Body to Writer; no xray transport object is
// involved.
type XHTTPSession struct {
	ID      string
	Seq     string
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
	if s.Config.Host != "" && !strings.EqualFold(req.Host, s.Config.Host) {
		http.Error(w, "host mismatch", http.StatusNotFound)
		return
	}
	packetMode := isXHTTPPacketMode(s.Config.Mode)
	if req.Method != s.Config.UplinkHTTPMethod && !(packetMode && req.Method == http.MethodGet) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Config.MaxPost.To > 0 && req.ContentLength > int64(s.Config.MaxPost.To) {
		http.Error(w, "xhttp body too large", http.StatusRequestEntityTooLarge)
		return
	}
	allowStreamOne := s.Config.Mode == XHTTPStreamOne
	sessionID, seq, err := s.Config.extractRequestMetaOptions(req, allowStreamOne, allowStreamOne || (packetMode && req.Method == http.MethodGet))
	if err != nil {
		http.Error(w, "invalid xhttp metadata", http.StatusBadRequest)
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
	if flusher, ok := w.(http.Flusher); ok && !(packetMode && req.Method == s.Config.UplinkHTTPMethod) {
		flusher.Flush()
	}
	body := req.Body
	if s.Config.MaxPost.To > 0 {
		body = http.MaxBytesReader(w, req.Body, int64(s.Config.MaxPost.To))
	}
	if err := s.Handler(req.Context(), XHTTPSession{ID: sessionID, Seq: seq, Request: req, Body: body, Writer: w}); err != nil {
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
	server := &http.Server{Handler: h2c.NewHandler(s, &http2.Server{}), MaxHeaderBytes: s.Config.ServerMaxHeaderBytes}
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
