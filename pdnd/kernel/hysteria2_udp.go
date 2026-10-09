package kernel

import (
	"context"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/outbound"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/ipv4"
)

// QUIC 类协议（Hysteria2、TUIC）共用的 UDP 转发热路径。单条 QUIC 连接上几万包/秒
// 时，逐包的分配、全局锁、地址解析和 sendto 都会变成瓶颈（实测 100Mbps 起丢包）。
// 这里：
//   - 上行（客户端 → 上游）：先阻塞等一条，再把会话队列里积压的消息一次取走，
//     凑批发出；上游能批量时 Linux 走 sendmmsg，一次系统调用发一批；
//   - 下行（上游 → 客户端）：上游能批量时 Linux 走 recvmmsg 批量收，否则逐包读；
//     不再为每包复制一份负载；
//   - 空闲会话不常驻收发缓冲（hysteria2_udp_relay.go）：批量缓冲只在真正收发的
//     那一刻从共享池借用，空闲时上行零拷贝地等会话队列、下行只窥视上游 socket；
//   - 目标地址按上一包缓存，域名目标不再逐包解析；
//   - 流量直接原子累加到会话所属用户的计数器（userSession），不抢适配器的锁；
//   - 上游 socket 收发缓冲固定申请 hy2UDPSocketBuffer（见该常量的数据与理由）。
//
// 上游能不能批量（hy2UDPUpstream）：
//   - 出站明确交出裸 *net.UDPConn（RawUDPConn，私网目标放开时的直连）：直接批量；
//   - 出站给出带检查的批量接口（outbound.UDPBatchProvider，默认拦私网时的直连）：
//     经它批量，私网目标在它的 WriteBatch 里逐条剔除，裸 socket 不出 outbound 包；
//   - 都没有（加密、封装类出站）：逐包 WriteTo / ReadFrom。

const (
	// hy2UDPBatch 是一次批量收发的最大包数。
	hy2UDPBatch = 32
	// hy2UDPSocketBuffer 是给上游 UDP socket 申请的收发缓冲。Linux 按申请值的两倍
	// 记账与读回，实得 212992 字节：等于 Debian / Ubuntu 缺省的 net.core.rmem_default，
	// 也就是官方 hysteria 与 sing-box 不调缓冲时上游 socket 的大小。
	//
	// 数据：10-09 vpcnode2 VPC 复测，hy2 UDP 下行，3 次中位（Mbps / 丢包）。
	// 「8MB 档」指 rmem_max / wmem_max 为 8MB，节点安装脚本写的 16MB 也落在这一档。
	//   - 申请 4MB，8MB 档实得 8MB：300M 档 228 / 24.2%，500M 档 217 / 56.7%；
	//     输给官方 hysteria（243 / 19.0%、246 / 50.8%）。
	//   - 只把这一项改成 106496（实验 B，其余不变），8MB 档：254 / 15.4%、239 / 52.2%，
	//     与官方持平。
	//   - 缺省档（rmem_max 212992，申请 4MB 被截到上限、实得 425984）：252 / 15.9%、
	//     253 / 49.4%，与官方（不调缓冲，实得 212992）持平；本值在这一档实得 212992，
	//     与官方相同，留给复测确认。
	//   - TUIC 两种取值结果相同（230 / 23.2% 对 230 / 23.4%）。
	//
	// 机理：下行持续过载（目标发得比 QUIC 送得快）时，大缓冲只是多收进注定在 QUIC
	// 发送侧被丢的包，白耗单连接发送 goroutine 的 CPU、拉长排队；小缓冲让内核在入口
	// 直接丢，最便宜。上行只测了 4MB（丢包不超过 0.03%），208KB 的上行留给复测确认。
	//
	// 明确申请而不是不调：上游 socket 每会话一个、数量由用户决定（每用户上限见
	// quic_udp_quota.go），每会话的内核记账固定在收发各约 208KB，不随调大了
	// rmem_default 的机器膨胀，也不会靠会话数顶满全局 net.ipv4.udp_mem。
	hy2UDPSocketBuffer = 106496
	// hy2UDPResolveTTL 是域名目标解析结果在会话内的复用时长。
	hy2UDPResolveTTL = 30 * time.Second
)

// quicSocketBufferWant 是 quic-go 给监听 socket 要的 UDP 收发缓冲（sagernet/quic-go 的
// protocol.DesiredReceiveBufferSize 与 DesiredSendBufferSize，各 8MB）。
const quicSocketBufferWant = 8 << 20

// hy2UDPBatchSupported：x/net 的 ReadBatch / WriteBatch 只在 Linux 上是
// recvmmsg / sendmmsg，别的平台一次只收发一包且每包多几次分配，不如逐包。
var hy2UDPBatchSupported = runtime.GOOS == "linux"

// rawUDPConnProvider 由只管生命周期、不改包内容的出站包装实现（如出站租约），
// 交出底层 *net.UDPConn 供批量收发与调缓冲。会改写负载或要逐包把关的包装
// （加密、封装类出站、私网拦截）绝不能实现它，否则批量路径会绕过它们。
type rawUDPConnProvider interface {
	RawUDPConn() *net.UDPConn
}

func rawUDPConnOf(conn net.PacketConn) *net.UDPConn {
	switch v := conn.(type) {
	case *net.UDPConn:
		return v
	case rawUDPConnProvider:
		return v.RawUDPConn()
	}
	return nil
}

// hy2UDPBatchIO 是上游的批量收发：裸 socket 时是 ipv4.PacketConn，默认拦私网时
// 是出站给的 outbound.UDPBatchConn（WriteBatch 里逐条把关）。
type hy2UDPBatchIO interface {
	ReadBatch(ms []ipv4.Message, flags int) (int, error)
	WriteBatch(ms []ipv4.Message, flags int) (int, error)
}

// hy2UDPReader 是能带 flags 收包的上游接口（ipv4.PacketConn 或出站的
// outbound.UDPBatchConn）：下行靠它窥视等包、非阻塞收包，见 hy2DownlinkUDPPeek。
type hy2UDPReader interface {
	ReadBatch(ms []ipv4.Message, flags int) (int, error)
}

// hy2UDPUpstream 是一个上游 socket 可用的收发路径。
type hy2UDPUpstream struct {
	conn net.PacketConn
	// raw 只在出站明确交出裸 socket 时非 nil。
	raw *net.UDPConn
	// batch 在 Linux、IPv4 本地地址、出站支持批量时非 nil（上行批量发、下行批量收）。
	batch hy2UDPBatchIO
	// reader 在出站交出裸 socket 或带检查的批量接口时非 nil，不分协议族：下行经它
	// 窥视等包，空闲时不占收包缓冲。Linux 上一次收一批（recvmmsg 不分协议族），
	// 别的平台一次一包。
	reader hy2UDPReader
}

func newHy2UDPUpstream(upstream net.PacketConn) hy2UDPUpstream {
	u := hy2UDPUpstream{conn: upstream, raw: rawUDPConnOf(upstream)}
	if u.raw != nil {
		_ = u.raw.SetReadBuffer(hy2UDPSocketBuffer)
		_ = u.raw.SetWriteBuffer(hy2UDPSocketBuffer)
		packetConn := ipv4.NewPacketConn(u.raw)
		if hy2UDPPeekSupported {
			u.reader = packetConn
		}
		if hy2UDPBatchSupported && isIPv4Local(u.raw.LocalAddr()) {
			u.batch = packetConn
		}
		return u
	}
	provider, ok := upstream.(outbound.UDPBatchProvider)
	if !ok {
		return u
	}
	checked := provider.UDPBatch()
	if checked == nil {
		return u
	}
	_ = checked.SetReadBuffer(hy2UDPSocketBuffer)
	_ = checked.SetWriteBuffer(hy2UDPSocketBuffer)
	if hy2UDPPeekSupported {
		u.reader = checked
	}
	if hy2UDPBatchSupported && isIPv4Local(checked.LocalAddr()) {
		u.batch = checked
	}
	return u
}

func isIPv4Local(addr net.Addr) bool {
	local, ok := addr.(*net.UDPAddr)
	return ok && local.IP.To4() != nil
}

// hy2PacketTryReader 是 nativewire 会话连接提供的非阻塞读。
type hy2PacketTryReader interface {
	TryReadPacket(*buf.Buffer) (M.Socksaddr, bool)
}

// hy2UDPResolver 缓存上一包目标的解析结果。
type hy2UDPResolver struct {
	ctx      context.Context
	last     M.Socksaddr
	addr     *net.UDPAddr
	resolved time.Time
}

func (r *hy2UDPResolver) resolve(destination M.Socksaddr) (*net.UDPAddr, error) {
	if r.addr != nil && destination == r.last && (destination.IsIP() || time.Since(r.resolved) < hy2UDPResolveTTL) {
		return r.addr, nil
	}
	addr, err := resolveUDPAddr(r.ctx, destination)
	if err != nil {
		return nil, err
	}
	r.last, r.addr, r.resolved = destination, addr, time.Now()
	return addr, nil
}

// relayHy2UDP 在 conn（客户端会话）与 upstream（上游 socket）之间双向转发，
// 任一方向结束即收尾；字节数随搬随记到 up / down（用户计数器）。
//
// 上行另起一个 goroutine，下行就在调用方的 goroutine 里跑；任一方向结束即取消，
// 取消时由 context.AfterFunc 给两端设读截止、打断另一方向的阻塞读，不再常驻一个
// 专门等取消的 goroutine（空闲会话每个 goroutine 都占一份栈）。
func relayHy2UDP(ctx context.Context, conn N.PacketConn, upstream net.PacketConn, destination M.Socksaddr, up, down *atomic.Int64) {
	u := newHy2UDPUpstream(upstream)
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(bridgeCtx, func() {
		// udpPacketConn 不支持 SetDeadline，只认读截止时间。
		_ = conn.SetReadDeadline(time.Now())
		_ = upstream.SetDeadline(time.Now())
	})
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer cancel()
		defer wg.Done()
		hy2UplinkUDP(bridgeCtx, conn, u, destination, up)
	}()
	hy2DownlinkUDP(conn, u, down)
	cancel()
	wg.Wait()
}

// hy2BatchWriter 攒一批上行包：上游能批量且目标都是 IPv4 时走 WriteBatch
// （Linux 上即 sendmmsg），同一目标的连续包再合成一条 GSO 消息（见
// hysteria2_udp_gso.go）；否则逐包 WriteTo。
type hy2BatchWriter struct {
	upstream net.PacketConn
	batch    hy2UDPBatchIO
	messages []ipv4.Message
	buffers  [][1][]byte
	addrs    []*net.UDPAddr
	payloads [][]byte
	v4Only   bool
	// gso 为真时同目标的连续包合成一条 UDP_SEGMENT 消息；内核或网卡不支持时
	// 第一次失败即关掉，本会话之后逐条发。
	gso bool
	// first[i] 是 messages[i] 里第一个包在 payloads 里的下标；oob 是各条 GSO
	// 消息的控制信息缓冲。
	first []int
	oob   []byte
}

// newHy2BatchWriter 只记上游与开关；批量发送用的消息、iovec 与 GSO 控制信息
// 缓冲（约 5KB）到第一次真的批量发送时才分配，只发过单包的会话不占。
func newHy2BatchWriter(upstream net.PacketConn, batch hy2UDPBatchIO) *hy2BatchWriter {
	w := &hy2BatchWriter{upstream: upstream, batch: batch}
	if batch != nil && hy2UDPGSOSupported {
		w.gso = true
	}
	return w
}

// ensureBatchBuffers 第一次批量发送前分配消息与控制信息缓冲。
func (w *hy2BatchWriter) ensureBatchBuffers() {
	if w.buffers != nil {
		return
	}
	w.messages = make([]ipv4.Message, 0, hy2UDPBatch)
	w.buffers = make([][1][]byte, hy2UDPBatch)
	w.first = make([]int, 0, hy2UDPBatch)
	if hy2UDPGSOSupported {
		w.oob = make([]byte, hy2UDPBatch*udpSegmentCmsgSpace)
	}
}

func (w *hy2BatchWriter) reset() {
	w.addrs, w.payloads, w.v4Only = w.addrs[:0], w.payloads[:0], true
}

func (w *hy2BatchWriter) add(resolver *hy2UDPResolver, payload []byte, destination, fallback M.Socksaddr) {
	if !destination.IsValid() {
		destination = fallback
	}
	addr, err := resolver.resolve(destination)
	if err != nil {
		// 解析失败的包照旧丢弃，不影响同批其它包。
		return
	}
	if addr.IP.To4() == nil {
		w.v4Only = false
	}
	w.addrs = append(w.addrs, addr)
	w.payloads = append(w.payloads, payload)
}

// flush 发出攒下的包，返回成功写出的字节数。
func (w *hy2BatchWriter) flush() (int64, error) {
	if w.batch != nil && w.v4Only && len(w.payloads) > 1 {
		return w.flushBatch(0)
	}
	var written int64
	for i, payload := range w.payloads {
		n, err := w.upstream.WriteTo(payload, w.addrs[i])
		if err != nil {
			return written, err
		}
		written += int64(n)
	}
	return written, nil
}

// flushBatch 从 payloads[from] 起批量发出。
func (w *hy2BatchWriter) flushBatch(from int) (int64, error) {
	w.ensureBatchBuffers()
	w.buildMessages(from)
	var written int64
	for sent := 0; sent < len(w.messages); {
		n, err := w.batch.WriteBatch(w.messages[sent:], 0)
		for _, message := range w.messages[sent : sent+n] {
			written += int64(message.N)
		}
		sent += n
		if err != nil {
			if w.gso && sent < len(w.messages) && len(w.messages[sent].OOB) > 0 && isUDPGSOError(err) {
				// 这条 GSO 消息被拒（老内核、出口网卡没有校验和卸载、段长超过路径
				// MTU 等）：本会话关掉 GSO，从这条的第一个包起逐条重发。
				w.gso = false
				more, err := w.flushBatch(w.first[sent])
				return written + more, err
			}
			return written, err
		}
		if n == 0 {
			break
		}
	}
	return written, nil
}
