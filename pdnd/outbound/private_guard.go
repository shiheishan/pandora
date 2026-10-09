package outbound

import (
	"errors"
	"net"
	"net/netip"
	"sync/atomic"

	"github.com/aegispanel/nodeagent/internal/udprecv"
	"golang.org/x/net/ipv4"
)

// 默认拒绝私网目标（相当于 Xray 的 geoip:private block）。
//
// 代理原先不拦回环与内网目标：用户能经节点访问节点本机的服务（面板、数据库、
// 云厂商元数据接口）和同机房内网。现在直连出站对「用户给的目标」默认拒绝
// 回环、私网、链路本地、运营商 NAT、保留与组播段；域名在解析之后按实际 IP 判，
// 防 DNS 指向内网。可经 pdnd 配置 runtime.allow_private_destinations 放开。
//
// 只管用户目标：中转出站连接管理员配置的上游（可能就在内网）走 DialContext，
// 不受限；入站的回落目标（允许本机回环，用户 2026-10-07 定）与 REALITY dest
// 不经出站，也不受影响。

var blockPrivateDestinations atomic.Bool

func init() { blockPrivateDestinations.Store(true) }

// SetBlockPrivateDestinations 设置是否拒绝私网目标（默认拒绝）。
func SetBlockPrivateDestinations(block bool) { blockPrivateDestinations.Store(block) }

// BlockPrivateDestinations 返回当前是否拒绝私网目标。
func BlockPrivateDestinations() bool { return blockPrivateDestinations.Load() }

// ErrPrivateDestination 是目标落在私网段、被默认策略拒绝。
var ErrPrivateDestination = errors.New("目标是回环、内网或保留地址，默认拒绝")

// privatePrefixes 与 v2fly geoip:private 同一份清单。
var privatePrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3",
		"::/127", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// isPrivateDestination 判断一个目标 IP 是否落在私网清单里（4in6 按 v4 判）。
func isPrivateDestination(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return true
	}
	for _, p := range privatePrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// destinationBlocked 是「当前策略下这个 IP 该不该拒」。
func destinationBlocked(addr netip.Addr) bool {
	return blockPrivateDestinations.Load() && isPrivateDestination(addr)
}

// guardedPacketConn 给直连 UDP 逐包把关：发往私网目标的包直接丢掉（像防火墙
// 那样静默，不把整个 UDP 会话打断——客户端把 DNS 指向局域网路由器很常见）。
//
// 只内嵌 net.PacketConn 接口，不内嵌 *net.UDPConn：WriteToUDP、SyscallConn、
// File 这些能绕过逐包检查的方法一个都不透出；也绝不能实现 RawUDPConn——批量
// 收发路径拿到裸 socket 就绕过了这道检查。私网目标放开时 ListenUDP 不套这层，
// 裸 socket 照常透出。批量收发改经 UDPBatch 交出的带检查接口（见下）。
type guardedPacketConn struct {
	net.PacketConn
}

func (c *guardedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if ip, ok := packetDestinationIP(addr); ok && destinationBlocked(ip) {
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

// packetDestinationIP 取出包的目标 IP；取不出（非 IP 地址）时交给底层去报错。
func packetDestinationIP(addr net.Addr) (netip.Addr, bool) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return netip.AddrFromSlice(a.IP)
	case interface{ AddrPort() netip.AddrPort }:
		return a.AddrPort().Addr(), true
	}
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		if ip, err := netip.ParseAddr(host); err == nil {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

// UDPBatchConn 是直连 UDP 的批量发与就绪收（Linux 上 WriteBatch 即 sendmmsg，
// Receiver 收包用 recvmmsg），给 hy2、TUIC 这类单连接几万包/秒的 UDP 转发用。
//
// 它不交出裸 socket：拿到它的人只能经这几个方法发、收、调缓冲。私网拦截在
// WriteBatch 里逐条按目标把关，与 guardedPacketConn.WriteTo 同一个判定、同一种
// 语义（被拦的包静默丢弃、照常计为已发出）；收方向与 ReadFrom 一样不过滤。
type UDPBatchConn interface {
	// WriteBatch 依次发出 ms，返回已处理的条数（被拦的也算已处理，N 记为负载
	// 长度）；少于 len(ms) 且 err 为 nil 时，调用方从返回处接着发。
	WriteBatch(ms []ipv4.Message, flags int) (int, error)
	// Receiver 是这个 socket 的收包器（等待期间不占缓冲，见 internal/udprecv），
	// 只能收；平台不支持时为 nil。只给一个 goroutine 用。
	Receiver() *udprecv.Receiver
	LocalAddr() net.Addr
	SetReadBuffer(bytes int) error
	SetWriteBuffer(bytes int) error
}

// UDPBatchProvider 由能提供带检查批量收发的 UDP 连接实现；不支持时返回 nil。
type UDPBatchProvider interface {
	UDPBatch() UDPBatchConn
}

// UDPBatch 给 guardedPacketConn 开一个带检查的批量接口；底层不是裸 UDP socket
// 时返回 nil，调用方退回逐包。
func (c *guardedPacketConn) UDPBatch() UDPBatchConn {
	raw, ok := c.PacketConn.(*net.UDPConn)
	if !ok {
		return nil
	}
	rc, err := raw.SyscallConn()
	if err != nil {
		return nil
	}
	return &guardedUDPBatch{raw: raw, recv: udprecv.NewReceiver(rc), batch: ipv4.NewPacketConn(raw)}
}

// guardedUDPBatch 的字段都不导出、类型本身也不导出：包外拿不到 raw。
type guardedUDPBatch struct {
	raw   *net.UDPConn
	recv  *udprecv.Receiver
	batch *ipv4.PacketConn
}

// messageBlocked 是一条消息的目标是否被私网策略拒绝。目标取不出 IP（nil 等）
// 的交给内核去报错，与 WriteTo 一致。
func messageBlocked(m *ipv4.Message) bool {
	if m.Addr == nil {
		return false
	}
	ip, ok := packetDestinationIP(m.Addr)
	return ok && destinationBlocked(ip)
}

func (b *guardedUDPBatch) WriteBatch(ms []ipv4.Message, flags int) (int, error) {
	done := 0
	for done < len(ms) {
		if messageBlocked(&ms[done]) {
			// 被拦：从批里剔除、静默丢弃，按已发出记（与逐包 WriteTo 同语义）。
			n := 0
			for _, payload := range ms[done].Buffers {
				n += len(payload)
			}
			ms[done].N = n
			done++
			continue
		}
		// 一段连续放行的消息一次交给内核；全是公网目标时就是整批一次。
		start, end := done, done+1
		for end < len(ms) && !messageBlocked(&ms[end]) {
			end++
		}
		n, err := b.batch.WriteBatch(ms[start:end], flags)
		done = start + n
		if err != nil {
			return done, err
		}
		if done < end {
			// 内核只收了一部分（发送缓冲满），交还调用方决定是否续发。
			return done, nil
		}
	}
	return done, nil
}

func (b *guardedUDPBatch) Receiver() *udprecv.Receiver { return b.recv }

func (b *guardedUDPBatch) LocalAddr() net.Addr            { return b.raw.LocalAddr() }
func (b *guardedUDPBatch) SetReadBuffer(bytes int) error  { return b.raw.SetReadBuffer(bytes) }
func (b *guardedUDPBatch) SetWriteBuffer(bytes int) error { return b.raw.SetWriteBuffer(bytes) }
