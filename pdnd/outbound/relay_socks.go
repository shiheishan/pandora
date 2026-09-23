package outbound

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/protocol/socks"
	"github.com/sagernet/sing/protocol/socks/socks5"
)

const proxyHandshakeTimeout = 15 * time.Second

func guardHandshake(ctx context.Context, conn net.Conn) func() {
	deadline := time.Now().Add(proxyHandshakeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	return func() {
		close(done)
		<-stopped
		_ = conn.SetDeadline(time.Time{})
	}
}

func init() {
	Register("socks", newSocks)
	Register("socks5", newSocks)
	Register("http", newHTTP)
}

//------------------------------------------------------------------------------
// SOCKS5
//------------------------------------------------------------------------------

type socksOut struct {
	instanceIdentity
	tag              string
	server           M.Socksaddr
	username         string
	password         string
	dialer           Dialer
	udpOverTCPNotSup bool
}

func newSocks(o Options) (Outbound, error) {
	server, err := Server(o.Settings)
	if err != nil {
		return nil, err
	}
	if o.Dialer == nil {
		return nil, fmt.Errorf("出站 %s 没有拨号器", o.Tag)
	}
	return &socksOut{
		tag:      o.Tag,
		server:   server,
		username: Str(o.Settings, "username"),
		password: Str(o.Settings, "password"),
		dialer:   o.Dialer,
	}, nil
}

func (s *socksOut) Tag() string  { return s.tag }
func (s *socksOut) Type() string { return "socks5" }
func (s *socksOut) Close() error { return nil }

func (s *socksOut) DialTCP(ctx context.Context, dest M.Socksaddr) (net.Conn, error) {
	conn, err := s.dialer.DialContext(ctx, "tcp", s.server)
	if err != nil {
		return nil, fmt.Errorf("连接 socks5 上游: %w", err)
	}
	// 握手期间用 ctx 的截止时间约束读写：SOCKS5 握手没有自己的超时，
	// 上游半死不活时会把连接卡住直到系统 TCP 超时（分钟级）。
	finishHandshake := guardHandshake(ctx, conn)
	if _, err := socks.ClientHandshake5(conn, socks5.CommandConnect, dest,
		s.username, s.password); err != nil {
		finishHandshake()
		conn.Close()
		return nil, fmt.Errorf("socks5 握手: %w", err)
	}
	finishHandshake()
	return conn, nil
}

// DialUDP 走 UDP ASSOCIATE。
//
// 关键点：控制用的 TCP 连接必须一直开着——RFC 1928 规定它一断，
// UDP 关联立即失效。所以返回的 PacketConn 持有它，Close 时一起关。
func (s *socksOut) ListenUDP(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	ctrl, err := s.dialer.DialContext(ctx, "tcp", s.server)
	if err != nil {
		return nil, fmt.Errorf("连接 socks5 上游: %w", err)
	}
	finishHandshake := guardHandshake(ctx, ctrl)
	resp, err := socks.ClientHandshake5(ctrl, socks5.CommandUDPAssociate, dest,
		s.username, s.password)
	if err != nil {
		finishHandshake()
		ctrl.Close()
		return nil, fmt.Errorf("socks5 UDP ASSOCIATE: %w", err)
	}
	finishHandshake()

	relay := resp.Bind
	// 上游常回 0.0.0.0（意思是「就用你连我的那个地址」）。
	// 照着 0.0.0.0 发包会直接发不出去。
	if !relay.IsValid() || (relay.IsIP() && relay.Addr.IsUnspecified()) {
		relay = M.SocksaddrFrom(s.server.Addr, relay.Port)
		if !s.server.IsIP() {
			relay = M.ParseSocksaddrHostPort(s.server.Fqdn, relay.Port)
		}
	}
	if relay.Port == 0 {
		ctrl.Close()
		return nil, fmt.Errorf("socks5 上游没有返回 UDP 端口")
	}

	// The SOCKS packet codec needs a connected datagram transport to the
	// server-provided relay.  The destination carried inside each SOCKS5 UDP
	// frame remains independent, so one association can still serve many
	// targets.
	udpConn, err := s.dialer.DialContext(ctx, "udp", relay)
	if err != nil {
		ctrl.Close()
		return nil, err
	}
	return socks.NewAssociatePacketConn(udpConn, dest, ctrl), nil
}

//------------------------------------------------------------------------------
// HTTP CONNECT
//------------------------------------------------------------------------------

type httpOut struct {
	instanceIdentity
	tag      string
	server   M.Socksaddr
	username string
	password string
	tls      TLSConfig
	dialer   Dialer
}

func newHTTP(o Options) (Outbound, error) {
	server, err := Server(o.Settings)
	if err != nil {
		return nil, err
	}
	if o.Dialer == nil {
		return nil, fmt.Errorf("出站 %s 没有拨号器", o.Tag)
	}
	tlsCfg, err := ParseTLS(o.Settings)
	if err != nil {
		return nil, err
	}
	return &httpOut{
		tag:      o.Tag,
		server:   server,
		username: Str(o.Settings, "username"),
		password: Str(o.Settings, "password"),
		tls:      tlsCfg,
		dialer:   o.Dialer,
	}, nil
}

func (h *httpOut) Tag() string  { return h.tag }
func (h *httpOut) Type() string { return "http" }
func (h *httpOut) Close() error { return nil }

// ListenUDP 恒定失败：HTTP 代理协议本身没有 UDP。
// 明确报错而不是静默丢包——静默丢包的表现是「网页能开、游戏和 DNS 不通」，
// 用户根本不会往代理协议上想。
func (h *httpOut) ListenUDP(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, ErrUDPNotSupported
}

func (h *httpOut) DialTCP(ctx context.Context, dest M.Socksaddr) (net.Conn, error) {
	conn, err := h.dialer.DialContext(ctx, "tcp", h.server)
	if err != nil {
		return nil, fmt.Errorf("连接 http 上游: %w", err)
	}
	finishHandshake := guardHandshake(ctx, conn)
	if h.tls.Enabled {
		host := h.server.Fqdn
		if host == "" {
			host = h.server.Addr.String()
		}
		tlsConn, tlsErr := h.tls.Client(ctx, conn, host)
		if tlsErr != nil {
			// This outbound created the transport, so it also owns cleanup when
			// the TLS handshake fails.
			_ = conn.Close()
			finishHandshake()
			return nil, tlsErr
		}
		conn = tlsConn
	}
	target := dest.String()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: target},
		Host:   target,
		Header: http.Header{},
	}
	if h.username != "" {
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString(
			[]byte(h.username+":"+h.password)))
	}
	if err := req.Write(conn); err != nil {
		finishHandshake()
		conn.Close()
		return nil, fmt.Errorf("发送 CONNECT: %w", err)
	}
	// 必须用带缓冲的 reader 读响应，但缓冲里可能已经含有隧道的头几个字节。
	// 所以不能读完就丢——用 bufferedConn 把剩余字节接回连接。
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		finishHandshake()
		conn.Close()
		return nil, fmt.Errorf("读取 CONNECT 响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		finishHandshake()
		conn.Close()
		return nil, fmt.Errorf("上游拒绝 CONNECT: %s", resp.Status)
	}
	finishHandshake()
	if br.Buffered() > 0 {
		peek, _ := br.Peek(br.Buffered())
		return &bufferedConn{Conn: conn, buf: peek}, nil
	}
	return conn, nil
}

// bufferedConn 把预读进缓冲的数据接回连接前面。
type bufferedConn struct {
	net.Conn
	buf []byte
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	if len(b.buf) > 0 {
		n := copy(p, b.buf)
		b.buf = b.buf[n:]
		return n, nil
	}
	return b.Conn.Read(p)
}
