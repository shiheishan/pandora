package outbound

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func init() {
	Register("direct", newDirect)
	Register("block", newBlock)
}

// 域名解析策略。
//
// 默认 prefer_ipv4：多数 VPS 在有 v6 的机器上会优先走 v6，结果是节点入口
// 是 v4、出口却是 v6。有些站点对 v6 段的判定和 v4 不同（风控、地区、
// 直接拒绝），用户遇到「这个节点某些网站打不开」而我们这边看不出异常。
//
// prefer 系是「先试一族，不通再试另一族」；only 系是硬性限定，
// 纯 v6 站点在 ipv4_only 下会变成打不开——这是明确的取舍，不是默认。
const (
	StrategyPreferIPv4 = "prefer_ipv4"
	StrategyPreferIPv6 = "prefer_ipv6"
	StrategyIPv4Only   = "ipv4_only"
	StrategyIPv6Only   = "ipv6_only"
)

// Direct 直连出站，同时也是其它出站连接上游时用的拨号器。
type Direct struct {
	instanceIdentity
	tag      string
	strategy string
	dialer   net.Dialer
	resolver Resolver
}

// Resolver 解析域名。抽成接口是为了让上层能换成带缓存的实现——
// 每条连接都做一次系统解析，在高并发下 DNS 会先于带宽成为瓶颈。
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// NewDirect 供内部直接构造（其它出站需要一个拨号器时用）。
func NewDirect(tag, strategy string, r Resolver) *Direct {
	if strategy == "" {
		strategy = StrategyPreferIPv4
	}
	if r == nil {
		r = net.DefaultResolver
	}
	return &Direct{
		tag:      tag,
		strategy: strategy,
		dialer:   net.Dialer{Timeout: 10 * time.Second},
		resolver: r,
	}
}

func newDirect(o Options) (Outbound, error) {
	s := strings.ToLower(Str(o.Settings, "domain_strategy"))
	switch s {
	case "", StrategyPreferIPv4, StrategyPreferIPv6, StrategyIPv4Only, StrategyIPv6Only:
	default:
		return nil, fmt.Errorf("不认识的 domain_strategy %q", s)
	}
	return NewDirect(o.Tag, s, nil), nil
}

func (d *Direct) Tag() string  { return d.tag }
func (d *Direct) Type() string { return "direct" }
func (d *Direct) Close() error { return nil }

func (d *Direct) DialTCP(ctx context.Context, dest M.Socksaddr) (net.Conn, error) {
	return d.DialContext(ctx, "tcp", dest)
}

func (d *Direct) ListenUDP(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	return d.ListenPacket(ctx, dest)
}

// DialContext 实现 Dialer，供中转出站连接它们的上游。
func (d *Direct) DialContext(ctx context.Context, network string, dest M.Socksaddr) (net.Conn, error) {
	if dest.IsIP() {
		return d.dialer.DialContext(ctx, network, dest.String())
	}
	addrs, err := d.resolve(ctx, dest.Fqdn)
	if err != nil {
		return nil, err
	}
	// 逐个尝试。只试第一个的话，一个 IP 不通就整体失败，
	// 而多 A 记录站点里恰好有一个坏节点是常态。
	var lastErr error
	for _, a := range addrs {
		conn, err := d.dialer.DialContext(ctx, network,
			net.JoinHostPort(a.String(), fmt.Sprint(dest.Port)))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用地址")
	}
	return nil, fmt.Errorf("连接 %s: %w", dest.Fqdn, lastErr)
}

func (d *Direct) ListenPacket(ctx context.Context, dest M.Socksaddr) (net.PacketConn, error) {
	// UDP 绑本地任意地址。协议族跟着目标走：目标是 v4 就绑 v4，
	// 绑错族的话第一个包就会以 "address family mismatch" 失败。
	network := "udp"
	if dest.IsIP() {
		if dest.Addr.Is4() || dest.Addr.Is4In6() {
			network = "udp4"
		} else {
			network = "udp6"
		}
	} else if d.strategy == StrategyIPv4Only || d.strategy == StrategyPreferIPv4 {
		network = "udp4"
	}
	var lc net.ListenConfig
	return lc.ListenPacket(ctx, network, ":0")
}

// resolve 按策略排序解析结果。
func (d *Direct) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	network := "ip"
	switch d.strategy {
	case StrategyIPv4Only:
		network = "ip4"
	case StrategyIPv6Only:
		network = "ip6"
	}
	addrs, err := d.resolver.LookupNetIP(ctx, network, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("解析 %s 得到空结果", host)
	}

	if d.strategy == StrategyPreferIPv4 || d.strategy == StrategyPreferIPv6 {
		preferV4 := d.strategy == StrategyPreferIPv4
		var first, second []netip.Addr
		for _, a := range addrs {
			isV4 := a.Is4() || a.Is4In6()
			if isV4 == preferV4 {
				first = append(first, a)
			} else {
				second = append(second, a)
			}
		}
		// 稳定分组而不是排序：同一族内部保持解析返回的顺序，
		// 那个顺序通常已经被 DNS 服务器按就近做过安排。
		addrs = append(first, second...)
	}
	return addrs, nil
}

//------------------------------------------------------------------------------

// Block 拒绝出站。
//
// 返回错误而不是返回一个立刻 EOF 的连接：调用方需要能区分「连上了但对方
// 立即断开」和「被规则拒绝」，前者要重试，后者不该重试。
type Block struct {
	instanceIdentity
	tag string
}

func newBlock(o Options) (Outbound, error) { return &Block{tag: o.Tag}, nil }

func (b *Block) Tag() string  { return b.tag }
func (b *Block) Type() string { return "block" }
func (b *Block) Close() error { return nil }

// ErrBlocked 是被分流规则拒绝。
var ErrBlocked = fmt.Errorf("目标被规则拒绝")

func (b *Block) DialTCP(context.Context, M.Socksaddr) (net.Conn, error) {
	return nil, ErrBlocked
}
func (b *Block) ListenUDP(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, ErrBlocked
}
