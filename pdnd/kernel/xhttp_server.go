package kernel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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
	// Payload 是 packet-up 上行包的负载，已按 uplink_data_placement 从请求体、
	// 请求头或 Cookie 取好；其余请求类型为 nil。
	Payload []byte
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

// ServeHTTP 只回状态码、不写错误文本：Go 的错误文本是现成的指纹（Xray 只回状态
// 码，审查 X8）。
func (s XHTTPServer) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req == nil || req.URL == nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	if s.Handler == nil {
		rw.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// host、path 对不上：不带任何附加头的 404（Xray 在写 CORS 与填充之前就回）。
	if !requestHostMatches(req.Host, s.Config.Host) || !s.Config.pathMatches(req.URL.Path) {
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	s.Config.writeXrayResponseHeaders(rw.Header(), req)
	if req.Method == http.MethodOptions {
		rw.WriteHeader(http.StatusOK)
		return
	}
	kind, sessionID, seq, err := s.Config.classifyRequest(req)
	if err != nil {
		if err == errXHTTPMethodNotAllowed {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	for key, value := range s.Config.Headers {
		if req.Header.Get(key) != value {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
	}
	var payload []byte
	if kind == XHTTPRequestPacket {
		// sc_max_each_post_bytes 只约束 packet-up 的单个上行包（与 Xray 一致）。
		// stream-one / stream-up 的请求体是整条连接的上行，套上它会把超过 1MB 的
		// 上传截断。
		if s.Config.MaxPost.To > 0 && req.ContentLength > int64(s.Config.MaxPost.To) {
			rw.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		if payload, err = s.Config.uplinkPayload(req); err != nil {
			rw.WriteHeader(xhttpErrorStatus(err))
			return
		}
	}
	if req.ProtoMajor == 1 && (kind == XHTTPRequestDuplex || kind == XHTTPRequestStreamUp) {
		// HTTP/1.1 上边读请求体边写响应：不开全双工的话，响应头一冲出去请求体
		// 就读不动了（审查 X2）。
		_ = http.NewResponseController(rw).EnableFullDuplex()
	}
	w := &xhttpResponseWriter{ResponseWriter: rw}
	setXHTTPKindHeaders(w.Header(), kind, req)
	// 双工与下行先把响应头冲出去，客户端据此开始收下行；stream-up 等会话认下这条
	// 上行再回（冲突时回 409）；上行包等包收下再回 200。
	if kind == XHTTPRequestDuplex || kind == XHTTPRequestDownlink {
		w.Flush()
	}
	if err := s.Handler(req.Context(), XHTTPSession{ID: sessionID, Seq: seq, Kind: kind, Request: req, Body: req.Body, Writer: w, Payload: payload}); err != nil && !w.wrote {
		w.WriteHeader(xhttpErrorStatus(err))
	}
}

// X-Padding 的长度范围，与 Xray 的 xPaddingBytes 缺省一致。
const xhttpPaddingMin, xhttpPaddingMax = 100, 1000

// xhttpPaddingSource 是最长的一份填充，按随机长度切片取用，不逐请求分配。
// 用 'X'：HPACK / QPACK 的哈夫曼表里它是 8 位码，压缩后线上长度不变。
var xhttpPaddingSource = strings.Repeat("X", xhttpPaddingMax)

// writeXrayResponseHeaders 照 Xray hub.go 给 host、path 校验通过之后的每个响应
// （含出错与 OPTIONS）写 CORS 头（Config.WriteResponseHeader）与随机长度的
// X-Padding（ApplyXPaddingToResponse，非 obfs 模式）。不这样做的话，挂 CDN 时
// CDN 一侧看到的响应头形状与 Xray 服务端不同，HTTP/2 HEADERS 帧长度也是固定的。
//
// 请求侧的填充校验（Xray 对不合法的 x_padding 回 400）刻意不做：sing-box
// 1.13 没有 XHTTP 客户端，无法确认三家现行客户端都会发合法填充，校验会让
// 现有客户端连不上。
func (c XHTTPConfig) writeXrayResponseHeaders(h http.Header, req *http.Request) {
	if origin := req.Header.Get("Origin"); origin != "" {
		h.Set("Access-Control-Allow-Origin", origin)
	} else {
		h.Set("Access-Control-Allow-Origin", "*")
	}
	if c.SessionPlacement == "cookie" || c.SeqPlacement == "cookie" || c.UplinkDataPlacement == "cookie" {
		h.Set("Access-Control-Allow-Credentials", "true")
	}
	if req.Method == http.MethodOptions {
		h.Set("Access-Control-Allow-Methods", headerOr(req.Header.Get("Access-Control-Request-Method"), "*"))
		h.Set("Access-Control-Allow-Headers", headerOr(req.Header.Get("Access-Control-Request-Headers"), "*"))
	}
	h.Set("X-Padding", xhttpPaddingSource[:xhttpPaddingMin+rand.IntN(xhttpPaddingMax-xhttpPaddingMin+1)])
}

func headerOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// setXHTTPKindHeaders 按请求角色写 Xray 的缓冲与缓存头：
//   - 下行（GET 下行、stream-one）：X-Accel-Buffering: no（nginx / apache 不缓冲）、
//     Cache-Control: no-store、Content-Type: text/event-stream（中间盒按 SSE 处理、
//     不攒包；Xray 的 noSSEHeader 节点配置目前没有对应项，恒带）；
//   - stream-up：前两个；
//   - packet-up：请求体为空（数据放在请求头或 Cookie）时只带 Cache-Control:
//     no-store，没有请求体的方法默认会被缓存；
//   - 其余不设 Content-Type，与 Xray 一样由 net/http 按正文决定。
func setXHTTPKindHeaders(h http.Header, kind XHTTPRequestKind, req *http.Request) {
	switch kind {
	case XHTTPRequestDuplex, XHTTPRequestDownlink:
		h.Set("X-Accel-Buffering", "no")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Type", "text/event-stream")
	case XHTTPRequestStreamUp:
		h.Set("X-Accel-Buffering", "no")
		h.Set("Cache-Control", "no-store")
	case XHTTPRequestPacket:
		if req.ContentLength == 0 {
			h.Set("Cache-Control", "no-store")
		}
	}
}

// xhttpErrorStatus 把会话出错映射成状态码（不带正文）。
func xhttpErrorStatus(err error) int {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, errXHTTPConflict):
		return http.StatusConflict
	case errors.Is(err, errXHTTPCapacity):
		return http.StatusServiceUnavailable
	case errors.As(err, &tooLarge), errors.Is(err, errXHTTPPayloadTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadRequest
	}
}

// xhttpResponseWriter 记下响应头是否已经发出：已发出时出错只能结束响应，不能再写状态码。
type xhttpResponseWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *xhttpResponseWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *xhttpResponseWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *xhttpResponseWriter) Flush() {
	w.wrote = true
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *xhttpResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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
