package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	anytls "github.com/anytls/sing-anytls"
	"github.com/google/uuid"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"
)

// clientTLS 实现 sing 的 tls.Config，供 hy2/tuic 客户端校验节点证书。
type clientTLS struct {
	std *tls.Config
}

func newClientTLS(p *profile.Profile) (*clientTLS, error) {
	pem, err := os.ReadFile(p.CAFile())
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("无法解析 CA: %s", p.CAFile())
	}
	return &clientTLS{std: &tls.Config{
		RootCAs:    pool,
		ServerName: profile.ServerName,
		NextProtos: []string{"h3"},
		MinVersion: tls.VersionTLS13,
	}}, nil
}

func (c *clientTLS) ServerName() string              { return c.std.ServerName }
func (c *clientTLS) SetServerName(v string)          { c.std.ServerName = v }
func (c *clientTLS) NextProtos() []string            { return append([]string(nil), c.std.NextProtos...) }
func (c *clientTLS) SetNextProtos(v []string)        { c.std.NextProtos = append([]string(nil), v...) }
func (c *clientTLS) STDConfig() (*tls.Config, error) { return c.std, nil }
func (c *clientTLS) Client(conn net.Conn) (aTLS.Conn, error) {
	return &clientTLSConn{Conn: tls.Client(conn, c.std)}, nil
}
func (c *clientTLS) Clone() aTLS.Config {
	return &clientTLS{std: c.std.Clone()}
}
func (c *clientTLS) Start() error { return nil }
func (c *clientTLS) Close() error { return nil }
func (c *clientTLS) Server(conn net.Conn) (aTLS.Conn, error) {
	return &clientTLSConn{Conn: tls.Server(conn, c.std)}, nil
}

type clientTLSConn struct{ *tls.Conn }

func (c *clientTLSConn) NetConn() net.Conn { return c.Conn }

// quicDialer：hy2 / tuic 共用的 QUIC 客户端拨号。
type quicDialer struct {
	hy2Client  *hy2.Client
	tuicClient *tuic.Client
}

func (q *quicDialer) DialTCP(ctx context.Context, dest string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addr := M.ParseSocksaddr(dest)
	if q.hy2Client != nil {
		return q.hy2Client.DialConn(dialCtx, addr)
	}
	return q.tuicClient.DialConn(dialCtx, addr)
}

func (q *quicDialer) ListenUDP(ctx context.Context) (net.PacketConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if q.hy2Client != nil {
		return q.hy2Client.ListenPacket(dialCtx)
	}
	return q.tuicClient.ListenPacket(dialCtx)
}

func (q *quicDialer) Close() error {
	if q.hy2Client != nil {
		return q.hy2Client.CloseWithError(nil)
	}
	return q.tuicClient.CloseWithError(nil)
}

func newHy2Dialer(p *profile.Profile, node string, user profile.User) (Dialer, error) {
	tlsCfg, err := newClientTLS(p)
	if err != nil {
		return nil, err
	}
	port := uint16(p.Port(profile.Hy2))
	ctx := context.Background()
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context:       ctx,
		Dialer:        N.SystemDialer,
		Logger:        logger.NOP(),
		ServerAddress: M.ParseSocksaddrHostPort(node, port),
		Password:      user.UUID,
		TLSConfig:     tlsCfg,
	})
	if err != nil {
		return nil, err
	}
	return &quicDialer{hy2Client: client}, nil
}

func newTuicDialer(p *profile.Profile, node string, user profile.User) (Dialer, error) {
	tlsCfg, err := newClientTLS(p)
	if err != nil {
		return nil, err
	}
	parsed, err := uuid.Parse(user.UUID)
	if err != nil {
		return nil, err
	}
	port := uint16(p.Port(profile.TUIC))
	ctx := context.Background()
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context:           ctx,
		Dialer:            N.SystemDialer,
		ServerAddress:     M.ParseSocksaddrHostPort(node, port),
		TLSConfig:         tlsCfg,
		UUID:              [16]byte(parsed),
		Password:          user.UUID,
		CongestionControl: profile.TUICCongestion,
	})
	if err != nil {
		return nil, err
	}
	return &quicDialer{tuicClient: client}, nil
}

type anytlsDialer struct {
	client *anytls.Client
}

func newAnytlsDialer(p *profile.Profile, node string, user profile.User) (Dialer, error) {
	port := p.Port(profile.AnyTLS)
	addr := net.JoinHostPort(node, strconv.Itoa(port))
	pem, err := os.ReadFile(p.CAFile())
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("无法解析 CA")
	}
	tlsCfg := &tls.Config{
		RootCAs:    pool,
		ServerName: profile.ServerName,
		MinVersion: tls.VersionTLS13,
	}
	ctx := context.Background()
	tlsDialer := &tls.Dialer{Config: tlsCfg, NetDialer: &net.Dialer{Timeout: 10 * time.Second}}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{
		Password: user.UUID,
		Logger:   logger.NOP(), // 会话收到服务端告警或填充更新时会调用 logger，不能是 nil
		DialOut: func(dialCtx context.Context) (net.Conn, error) {
			d, cancel := context.WithTimeout(dialCtx, 10*time.Second)
			defer cancel()
			return tlsDialer.DialContext(d, "tcp", addr)
		},
	})
	if err != nil {
		return nil, err
	}
	return &anytlsDialer{client: client}, nil
}

func (a *anytlsDialer) DialTCP(ctx context.Context, dest string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return a.client.CreateProxy(dialCtx, M.ParseSocksaddr(dest))
}

func (a *anytlsDialer) Close() error {
	return a.client.Close()
}
