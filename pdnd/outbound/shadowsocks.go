package outbound

import (
	"context"
	"fmt"
	"net"
	"strings"

	SS "github.com/sagernet/sing-shadowsocks2"
	M "github.com/sagernet/sing/common/metadata"
)

func init() {
	Register("shadowsocks", newShadowsocks)
	Register("ss", newShadowsocks)
}

// shadowsocksOut implements the raw Shadowsocks protocol. SIP003 plugins and
// ShadowTLS are separate transports with their own lifecycle and are rejected
// here until their dedicated outbound implementations are selected explicitly.
type shadowsocksOut struct {
	instanceIdentity
	tag    string
	server M.Socksaddr
	method SS.Method
	dialer Dialer
}

var secureShadowsocksMethods = map[string]bool{
	"aes-128-gcm":                   true,
	"aes-192-gcm":                   true,
	"aes-256-gcm":                   true,
	"chacha20-ietf-poly1305":        true,
	"xchacha20-ietf-poly1305":       true,
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
	"2022-blake3-chacha8-poly1305":  true,
}

func newShadowsocks(o Options) (Outbound, error) {
	server, err := Server(o.Settings)
	if err != nil {
		return nil, fmt.Errorf("出站 %s: %w", o.Tag, err)
	}
	if o.Dialer == nil {
		return nil, fmt.Errorf("出站 %s 没有拨号器", o.Tag)
	}
	if plugin := Str(o.Settings, "plugin"); plugin != "" {
		return nil, fmt.Errorf("出站 %s: Shadowsocks plugin %q 尚未支持", o.Tag, plugin)
	}
	tlsConfig, err := ParseTLS(o.Settings)
	if err != nil {
		return nil, fmt.Errorf("出站 %s: %w", o.Tag, err)
	}
	if tlsConfig.Enabled {
		return nil, fmt.Errorf("出站 %s: 原生 Shadowsocks 不接受 tls；请使用独立 ShadowTLS 出站", o.Tag)
	}

	methodName := strings.ToLower(Str(o.Settings, "method"))
	if !secureShadowsocksMethods[methodName] {
		return nil, fmt.Errorf("出站 %s 的 Shadowsocks 方法 %q 不安全或不受支持；仅允许 AEAD/2022 方法", o.Tag, methodName)
	}
	method, err := SS.CreateMethod(context.Background(), methodName, SS.MethodOptions{
		Password: Str(o.Settings, "password"),
	})
	if err != nil {
		return nil, fmt.Errorf("出站 %s 的 Shadowsocks 方法 %q 无效: %w", o.Tag, methodName, err)
	}
	return &shadowsocksOut{
		tag:    o.Tag,
		server: server,
		method: method,
		dialer: o.Dialer,
	}, nil
}

func (s *shadowsocksOut) Tag() string  { return s.tag }
func (s *shadowsocksOut) Type() string { return "shadowsocks" }
func (s *shadowsocksOut) Close() error { return nil }

func (s *shadowsocksOut) DialTCP(ctx context.Context, destination M.Socksaddr) (net.Conn, error) {
	conn, err := s.dialer.DialContext(ctx, "tcp", s.server)
	if err != nil {
		return nil, fmt.Errorf("连接 Shadowsocks 上游: %w", err)
	}
	return s.method.DialEarlyConn(conn, destination), nil
}

func (s *shadowsocksOut) ListenUDP(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	conn, err := s.dialer.DialContext(ctx, "udp", s.server)
	if err != nil {
		return nil, fmt.Errorf("连接 Shadowsocks UDP 上游: %w", err)
	}
	return s.method.DialPacketConn(conn), nil
}
