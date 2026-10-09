package kernel

import (
	"context"
	"sync/atomic"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/ipv4"
)

// hy2 / TUIC UDP 会话的上下行循环，空闲时不常驻收发缓冲（10-09 vpcnode2 缺陷 1）。
//
// 原先每个会话一建好就各自分配下行 32×64KB、上行 32×16KB 的批量缓冲，共约 2.6MB，
// 一直占到会话结束；每用户 1024 个会话就是约 2.6GB。现在：
//   - 下行（hysteria2_udp_downlink.go）：空闲时阻塞在 MSG_PEEK 上（只窥视 1 字节、
//     不取走），有包了才借收包缓冲；包来得密时转为持有小组阻塞读，一段时间没包再
//     还回去；
//   - 上行：零拷贝地等会话队列里的第一条（WaitReadPacket 直接交出消息自己的缓冲），
//     要凑批时才借缓冲取走积压的消息，发完即还（hy2UplinkUDP）。
//
// 缓冲从存货表借还（hysteria2_udp_stock.go）。借来的缓冲同一时刻只被一个会话用：
// 下行写回客户端时 WritePacket 同步编码并复制（nativewire 的 SendDatagram 自带
// 复制），上行在 flush 返回后才归还，归还后不再有引用。借出的总量随「同时在收发
// 的会话数」走，而不是随会话总数走。

// hy2UDPMaxDatagram 是一个 UDP 数据报的最大长度（含余量），收包缓冲按它分配，
// 不截断任何合法数据报。
const hy2UDPMaxDatagram = 64 << 10

// hy2DownlinkProbeBatch 是下行小组的包数：零星流量（DNS、游戏、语音）一次醒来
// 多是一两包，2 包的小组收不满即视为收空，不必再多一次落空的收包；热态持有、
// 卡在写回时也只占 128KB。
const hy2DownlinkProbeBatch = 2

// hy2DownlinkGroup 是下行一次收包用的消息与缓冲，各条的 64KB 缓冲共用一块内存。
type hy2DownlinkGroup struct {
	messages []ipv4.Message
}

func newHy2DownlinkGroup(size int) *hy2DownlinkGroup {
	group := &hy2DownlinkGroup{messages: make([]ipv4.Message, size)}
	slab := make([]byte, size*hy2UDPMaxDatagram)
	for i := range group.messages {
		group.messages[i].Buffers = [][]byte{slab[i*hy2UDPMaxDatagram : (i+1)*hy2UDPMaxDatagram : (i+1)*hy2UDPMaxDatagram]}
	}
	return group
}

// hy2UplinkBatch 是上行凑批时取积压消息用的缓冲（首包之外最多 31 条），用到
// 第几条才分配第几条。
type hy2UplinkBatch struct {
	buffers [hy2UDPBatch - 1]*buf.Buffer
}

func hy2UplinkUDP(ctx context.Context, conn N.PacketConn, upstream hy2UDPUpstream, fallback M.Socksaddr, up *atomic.Int64) {
	tryReader, _ := conn.(hy2PacketTryReader)
	waiter, _ := conn.(N.PacketReadWaiter)
	if waiter != nil && waiter.InitializeReadWaiter(N.ReadWaitOptions{}) {
		// 要求调用方复制的实现不走零拷贝。
		waiter = nil
	}
	writer := newHy2BatchWriter(upstream.conn, upstream.batch)
	resolver := &hy2UDPResolver{ctx: ctx}
	for {
		first, destination, err := hy2ReadUplinkPacket(conn, waiter)
		if err != nil {
			return
		}
		writer.reset()
		writer.add(resolver, first.Bytes(), destination, fallback)
		var batch *hy2UplinkBatch
		if tryReader != nil {
			batch = hy2UplinkBatchStock.get()
			for i, buffer := range batch.buffers {
				if buffer == nil {
					buffer = buf.NewPacket()
					batch.buffers[i] = buffer
				}
				buffer.Reset()
				destination, ok := tryReader.TryReadPacket(buffer)
				if !ok {
					break
				}
				writer.add(resolver, buffer.Bytes(), destination, fallback)
			}
		}
		written, err := writer.flush()
		// flush 之后 writer 里的负载切片不再被用到，缓冲可以归还。
		first.Release()
		if batch != nil {
			hy2UplinkBatchStock.put(batch)
		}
		if written > 0 {
			up.Add(written)
		}
		if err != nil {
			return
		}
	}
}

// hy2ReadUplinkPacket 阻塞读会话的下一条上行消息。会话连接支持等待读时直接拿
// 消息自己的缓冲（零拷贝，等待期间不占缓冲）；否则借一个包缓冲读进来。返回的
// 缓冲由调用方 Release。
func hy2ReadUplinkPacket(conn N.PacketConn, waiter N.PacketReadWaiter) (*buf.Buffer, M.Socksaddr, error) {
	if waiter != nil {
		return waiter.WaitReadPacket()
	}
	buffer := buf.NewPacket()
	destination, err := conn.ReadPacket(buffer)
	if err != nil {
		buffer.Release()
		return nil, M.Socksaddr{}, err
	}
	return buffer, destination, nil
}

// hy2WriteDownlink 把一个上游包写回客户端。WritePacket 同步完成编码与复制，
// 返回后 payload 可以复用（也可以还回空闲表给别的会话用），不必逐包另拷一份。
func hy2WriteDownlink(conn N.PacketConn, payload []byte, source M.Socksaddr, down *atomic.Int64) bool {
	if err := conn.WritePacket(buf.As(payload), source); err != nil {
		return false
	}
	down.Add(int64(len(payload)))
	return true
}
