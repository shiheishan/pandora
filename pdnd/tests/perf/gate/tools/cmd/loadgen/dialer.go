package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	xnet "github.com/xtls/xray-core/common/net"
	xraycore "github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Dialer 经代理拨 TCP 到 dest（host:port）。
type Dialer interface {
	DialTCP(ctx context.Context, dest string) (net.Conn, error)
	Close() error
}

// PacketDialer 经代理获得 UDP（hy2、tuic）。
type PacketDialer interface {
	ListenUDP(ctx context.Context) (net.PacketConn, error)
}

// NewDialer 按协议创建拨号器（每个实例一个 xray / QUIC / anytls 客户端）。
func NewDialer(p *profile.Profile, proto, node string, user profile.User) (Dialer, error) {
	switch proto {
	case profile.VLESSVision, profile.VLESSGRPC, profile.TrojanTCP, profile.TrojanWS, profile.SS, profile.VMessWS:
		cfg, err := xrayClientConfig(p, proto, node, user)
		if err != nil {
			return nil, err
		}
		inst, err := xraycore.StartInstance("json", cfg)
		if err != nil {
			return nil, err
		}
		base, cancel := context.WithCancel(context.Background())
		return &xrayDialer{inst: inst, base: base, cancel: cancel}, nil
	case profile.Hy2:
		return newHy2Dialer(p, node, user)
	case profile.TUIC:
		return newTuicDialer(p, node, user)
	case profile.AnyTLS:
		return newAnytlsDialer(p, node, user)
	default:
		return nil, fmt.Errorf("不支持的协议: %s", proto)
	}
}

// NewDirectDialer 直连（lat --proto direct）。
func NewDirectDialer() Dialer {
	return &directDialer{dialer: net.Dialer{Timeout: 10 * time.Second}}
}

type directDialer struct {
	dialer net.Dialer
}

func (d *directDialer) DialTCP(ctx context.Context, dest string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return d.dialer.DialContext(dialCtx, "tcp", dest)
}

func (d *directDialer) Close() error { return nil }

// xrayDialer：一个 xray 实例。xraycore.Dial 是惰性的（立即返回，出站在后台按 ctx 拨号），
// 连接的整个生命周期都挂在传入的 ctx 上，所以不能用调用方的限时 ctx（返回后一取消连接就断），
// 改用实例级的 base（Close 时取消）。拨号超时由调用方的读写期限体现。
type xrayDialer struct {
	inst   *xraycore.Instance
	base   context.Context
	cancel context.CancelFunc
}

func (d *xrayDialer) DialTCP(ctx context.Context, dest string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(dest)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("无效端口: %s", portStr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addr := xnet.ParseAddress(host)
	c, err := xraycore.Dial(d.base, d.inst, xnet.TCPDestination(addr, xnet.Port(port)))
	if err != nil {
		return nil, err
	}
	return newDeadlineConn(c), nil
}

func (d *xrayDialer) Close() error {
	d.cancel()
	return d.inst.Close()
}

// deadlineConn 给 SetDeadline 是空操作的连接（xray 的 cnc.Connection）补上期限：
// 到期就关闭连接，之后的读写返回 os.ErrDeadlineExceeded。到期即作废，和压测里「超时算失败」的用法一致；
// 用完一次期限要用零值清掉（roundTrip / echoOnce 已这样做），否则连接会在期限到时被关。
type deadlineConn struct {
	net.Conn
	mu      sync.Mutex
	rt, wt  *time.Timer
	expired atomic.Bool
}

func newDeadlineConn(c net.Conn) *deadlineConn { return &deadlineConn{Conn: c} }

func (c *deadlineConn) arm(slot **time.Timer, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *slot != nil {
		(*slot).Stop()
		*slot = nil
	}
	if t.IsZero() {
		return
	}
	*slot = time.AfterFunc(max(time.Until(t), 0), func() {
		c.expired.Store(true)
		_ = c.Conn.Close()
	})
}

func (c *deadlineConn) SetDeadline(t time.Time) error {
	c.arm(&c.rt, t)
	c.arm(&c.wt, t)
	return nil
}

func (c *deadlineConn) SetReadDeadline(t time.Time) error  { c.arm(&c.rt, t); return nil }
func (c *deadlineConn) SetWriteDeadline(t time.Time) error { c.arm(&c.wt, t); return nil }

func (c *deadlineConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil && c.expired.Load() {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if err != nil && c.expired.Load() {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *deadlineConn) Close() error {
	c.arm(&c.rt, time.Time{})
	c.arm(&c.wt, time.Time{})
	return c.Conn.Close()
}
