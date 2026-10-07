package kernel

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/ipv4"
)

// QUIC 类协议（Hysteria2、TUIC）共用的 UDP 转发热路径。单条 QUIC 连接上几万包/秒
// 时，逐包的分配、全局锁、地址解析和 sendto 都会变成瓶颈（实测 100Mbps 起丢包）。
// 这里：
//   - 上行（客户端 → 上游）：先阻塞读一条，再把会话队列里积压的消息一次取走，
//     凑批发出；上游能批量时 Linux 走 sendmmsg，一次系统调用发一批；
//   - 下行（上游 → 客户端）：上游能批量时 Linux 走 recvmmsg 批量收，否则逐包读；
//     不再为每包复制一份负载；
//   - 目标地址按上一包缓存，域名目标不再逐包解析；
//   - 流量直接原子累加到会话所属用户的计数器（userSession），不抢适配器的锁；
//   - 上游 socket 收发缓冲调大（受系统 rmem_max / wmem_max 上限约束）。
//
// 上游能不能批量（hy2UDPUpstream）：出站明确交出裸 *net.UDPConn（RawUDPConn）时
// 直接批量，否则逐包 WriteTo / ReadFrom。

const (
	// hy2UDPBatch 是一次批量收发的最大包数。
	hy2UDPBatch = 32
	// hy2UDPSocketBuffer 是给上游 UDP socket 申请的收发缓冲。
	hy2UDPSocketBuffer = 4 << 20
	// hy2UDPResolveTTL 是域名目标解析结果在会话内的复用时长。
	hy2UDPResolveTTL = 30 * time.Second
)

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

// hy2UDPBatchIO 是上游的批量收发（裸 socket 时是 ipv4.PacketConn）。
type hy2UDPBatchIO interface {
	ReadBatch(ms []ipv4.Message, flags int) (int, error)
	WriteBatch(ms []ipv4.Message, flags int) (int, error)
}

// hy2UDPUpstream 是一个上游 socket 可用的收发路径。
type hy2UDPUpstream struct {
	conn net.PacketConn
	// raw 只在出站明确交出裸 socket 时非 nil（逐包下行走 ReadFromUDPAddrPort）。
	raw *net.UDPConn
	// batch 在 Linux、IPv4 本地地址、出站支持批量时非 nil。
	batch hy2UDPBatchIO
}

func newHy2UDPUpstream(upstream net.PacketConn) hy2UDPUpstream {
	u := hy2UDPUpstream{conn: upstream, raw: rawUDPConnOf(upstream)}
	if u.raw != nil {
		_ = u.raw.SetReadBuffer(hy2UDPSocketBuffer)
		_ = u.raw.SetWriteBuffer(hy2UDPSocketBuffer)
		if hy2UDPBatchSupported && isIPv4Local(u.raw.LocalAddr()) {
			u.batch = ipv4.NewPacketConn(u.raw)
		}
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
func relayHy2UDP(ctx context.Context, conn N.PacketConn, upstream net.PacketConn, destination M.Socksaddr, up, down *atomic.Int64) {
	u := newHy2UDPUpstream(upstream)
	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer cancel()
		defer wg.Done()
		hy2UplinkUDP(bridgeCtx, conn, u, destination, up)
	}()
	go func() {
		defer cancel()
		defer wg.Done()
		hy2DownlinkUDP(conn, u, down)
	}()
	go func() {
		<-bridgeCtx.Done()
		// udpPacketConn 不支持 SetDeadline，只认读截止时间。
		_ = conn.SetReadDeadline(time.Now())
		_ = upstream.SetDeadline(time.Now())
	}()
	wg.Wait()
}

func hy2UplinkUDP(ctx context.Context, conn N.PacketConn, upstream hy2UDPUpstream, fallback M.Socksaddr, up *atomic.Int64) {
	tryReader, _ := conn.(hy2PacketTryReader)
	batch := 1
	if tryReader != nil {
		batch = hy2UDPBatch
	}
	buffers := make([]*buf.Buffer, batch)
	for i := range buffers {
		buffers[i] = buf.NewPacket()
	}
	defer func() {
		for _, buffer := range buffers {
			buffer.Release()
		}
	}()
	writer := newHy2BatchWriter(upstream.conn, upstream.batch)
	resolver := &hy2UDPResolver{ctx: ctx}
	for {
		buffers[0].Reset()
		destination, err := conn.ReadPacket(buffers[0])
		if err != nil {
			return
		}
		writer.reset()
		writer.add(resolver, buffers[0].Bytes(), destination, fallback)
		for n := 1; n < batch; n++ {
			buffers[n].Reset()
			destination, ok := tryReader.TryReadPacket(buffers[n])
			if !ok {
				break
			}
			writer.add(resolver, buffers[n].Bytes(), destination, fallback)
		}
		written, err := writer.flush()
		if written > 0 {
			up.Add(written)
		}
		if err != nil {
			return
		}
	}
}

// hy2BatchWriter 攒一批上行包：上游能批量且目标都是 IPv4 时走 WriteBatch
// （Linux 上即 sendmmsg），否则逐包 WriteTo。
type hy2BatchWriter struct {
	upstream net.PacketConn
	batch    hy2UDPBatchIO
	messages []ipv4.Message
	buffers  [][1][]byte
	addrs    []*net.UDPAddr
	payloads [][]byte
	v4Only   bool
}

func newHy2BatchWriter(upstream net.PacketConn, batch hy2UDPBatchIO) *hy2BatchWriter {
	w := &hy2BatchWriter{upstream: upstream}
	if batch != nil {
		w.batch = batch
		w.messages = make([]ipv4.Message, 0, hy2UDPBatch)
		w.buffers = make([][1][]byte, hy2UDPBatch)
	}
	return w
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
	var written int64
	if w.batch != nil && w.v4Only && len(w.payloads) > 1 {
		w.messages = w.messages[:0]
		for i, payload := range w.payloads {
			w.buffers[i][0] = payload
			w.messages = append(w.messages, ipv4.Message{Buffers: w.buffers[i][:], Addr: w.addrs[i]})
		}
		for sent := 0; sent < len(w.messages); {
			n, err := w.batch.WriteBatch(w.messages[sent:], 0)
			for _, message := range w.messages[sent : sent+n] {
				written += int64(message.N)
			}
			sent += n
			if err != nil {
				return written, err
			}
			if n == 0 {
				break
			}
		}
		return written, nil
	}
	for i, payload := range w.payloads {
		n, err := w.upstream.WriteTo(payload, w.addrs[i])
		if err != nil {
			return written, err
		}
		written += int64(n)
	}
	return written, nil
}

func hy2DownlinkUDP(conn N.PacketConn, upstream hy2UDPUpstream, down *atomic.Int64) {
	if upstream.batch != nil {
		hy2DownlinkUDPBatch(conn, upstream.batch, down)
		return
	}
	data := make([]byte, 64<<10)
	if raw := upstream.raw; raw != nil {
		for {
			n, source, err := raw.ReadFromUDPAddrPort(data)
			if err != nil {
				return
			}
			if !hy2WriteDownlink(conn, data[:n], M.SocksaddrFromNetIP(source).Unwrap(), down) {
				return
			}
		}
	}
	for {
		n, addr, err := upstream.conn.ReadFrom(data)
		if err != nil {
			return
		}
		if !hy2WriteDownlink(conn, data[:n], M.SocksaddrFromNet(addr).Unwrap(), down) {
			return
		}
	}
}

func hy2DownlinkUDPBatch(conn N.PacketConn, batch hy2UDPBatchIO, down *atomic.Int64) {
	messages := make([]ipv4.Message, hy2UDPBatch)
	for i := range messages {
		messages[i].Buffers = [][]byte{make([]byte, 64<<10)}
	}
	for {
		n, err := batch.ReadBatch(messages, 0)
		if err != nil {
			return
		}
		for _, message := range messages[:n] {
			source := M.Socksaddr{}
			if addr, ok := message.Addr.(*net.UDPAddr); ok {
				ip, _ := netip.AddrFromSlice(addr.IP)
				source = M.Socksaddr{Addr: ip.Unmap(), Port: uint16(addr.Port)}
			}
			if !hy2WriteDownlink(conn, message.Buffers[0][:message.N], source, down) {
				return
			}
		}
	}
}

// hy2WriteDownlink 把一个上游包写回客户端。WritePacket 同步完成编码与复制，
// 返回后 payload 可以复用，不必逐包另拷一份。
func hy2WriteDownlink(conn N.PacketConn, payload []byte, source M.Socksaddr, down *atomic.Int64) bool {
	if err := conn.WritePacket(buf.As(payload), source); err != nil {
		return false
	}
	down.Add(int64(len(payload)))
	return true
}
