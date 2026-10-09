package kernel

import (
	"context"
	"net"
	"net/netip"
	"runtime"
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
//   - 下行：阻塞在 MSG_PEEK 上等上游来包（只窥视 1 字节、不取走），有包了才从共享的
//     空闲表借收包缓冲，用 MSG_DONTWAIT 收空即还（hy2DownlinkUDPPeek）。先借 2 包的小组，
//     收满了说明流量大，再借 32 包的批量组；上一轮用过批量组就直接借批量组；
//   - 上行：零拷贝地等会话队列里的第一条（WaitReadPacket 直接交出消息自己的缓冲），
//     要凑批时才从共享的空闲表借缓冲取走积压的消息，发完即还（hy2UplinkUDP）。
//
// 借来的缓冲同一时刻只被一个会话用：下行写回客户端时 WritePacket 同步编码并
// 复制（nativewire 的 SendDatagram 自带复制），上行在 flush 返回后才归还，归还后
// 不再有引用。借出的总量随「同时在收发的会话数」走，而不是随会话总数走。
//
// 代价：下行每次从空闲醒来多一次只窥视 1 字节的 recvmmsg；持续有流量时用批量组
// 收满一批就接着收，不再窥视。

// hy2UDPMaxDatagram 是一个 UDP 数据报的最大长度（含余量），收包缓冲按它分配，
// 不截断任何合法数据报。
const hy2UDPMaxDatagram = 64 << 10

// hy2DownlinkProbeBatch 是下行醒来先借的小组的包数：零星流量（DNS、游戏、语音）
// 一次醒来多是一两包，2 包的小组收不满即视为收空，不必再多一次落空的收包；卡在
// 写回时也只占 128KB。
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

// hy2FreeList 是借还缓冲用的定长空闲表：还回来时表满就交给 GC，借时表空就新分配。
//
// 不用 sync.Pool：它按 P 存放，还进某个 P 私有槽的那组别的 P 拿不到，goroutine
// 换了 P 就只能新分配；每轮 GC 还会清掉一半。会话常驻缓冲去掉以后存活堆小、GC
// 频繁，本机 300Mbps 下行压测里每秒要重新分配近百组；批量组每组 2MB，这样的抖动
// 不能接受。空闲表只留「同时在收发的会话数」量级的几组（按 GOMAXPROCS 定），
// 常驻有上限。
type hy2FreeList[T any] struct {
	free  chan *T
	alloc func() *T
}

func newHy2FreeList[T any](keep int, alloc func() *T) *hy2FreeList[T] {
	return &hy2FreeList[T]{free: make(chan *T, keep), alloc: alloc}
}

func (l *hy2FreeList[T]) get() *T {
	select {
	case item := <-l.free:
		return item
	default:
		return l.alloc()
	}
}

func (l *hy2FreeList[T]) put(item *T) {
	select {
	case l.free <- item:
	default:
	}
}

// 空闲表常驻上限（4 核）：批量组 4×2MB，小组 16×128KB，上行 16 组（每组按用到的
// 条数、每条 16KB，最多 496KB）。
var (
	hy2DownlinkBatchFree = newHy2FreeList(runtime.GOMAXPROCS(0), func() *hy2DownlinkGroup {
		return newHy2DownlinkGroup(hy2UDPBatch)
	})
	hy2DownlinkProbeFree = newHy2FreeList(4*runtime.GOMAXPROCS(0), func() *hy2DownlinkGroup {
		return newHy2DownlinkGroup(hy2DownlinkProbeBatch)
	})
)

// hy2DownlinkBatchSlots 限制同时借出的下行批量组数（每组 2MB）。
//
// 写回客户端的 WritePacket 在 QUIC 连接的 DATAGRAM 发送队列（quic-go 每连接 32 条）
// 满时会阻塞，借来的缓冲要一直占到它返回。连接拥塞（含客户端故意不回 ACK）时，
// 这条连接上每个有包的会话都卡在这里；批量组不设上限，一个用户 1024 个会话又能
// 占回约 2GB。名额借光时接着用小组收：每个卡住的会话最多占 128KB，批量组全局最多
// hy2DownlinkBatchSlots 组。
//
// 不卡在发送侧的收包很快还回去，这类借用同一时刻约为 GOMAXPROCS 组；8 倍留出给
// 忙碌连接排队占用的余量（4 核即 32 组、64MB）。超出名额的会话本来就卡在 QUIC
// 发送侧，每次少收几包只多耗系统调用，不降吞吐。
var hy2DownlinkBatchSlots = make(chan struct{}, 8*runtime.GOMAXPROCS(0))

// hy2UplinkBatch 是上行凑批时取积压消息用的缓冲（首包之外最多 31 条），用到
// 第几条才分配第几条。
type hy2UplinkBatch struct {
	buffers [hy2UDPBatch - 1]*buf.Buffer
}

var hy2UplinkBatchFree = newHy2FreeList(4*runtime.GOMAXPROCS(0), func() *hy2UplinkBatch { return new(hy2UplinkBatch) })

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
			batch = hy2UplinkBatchFree.get()
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
			hy2UplinkBatchFree.put(batch)
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

func hy2DownlinkUDP(conn N.PacketConn, upstream hy2UDPUpstream, down *atomic.Int64) {
	if upstream.reader != nil {
		size := 1
		if hy2UDPBatchSupported {
			size = hy2UDPBatch
		}
		hy2DownlinkUDPPeek(conn, upstream.reader, size, down)
		return
	}
	// 上游只是 net.PacketConn（加密、封装类出站）：只能阻塞在 ReadFrom 里，收包
	// 缓冲随会话常驻。
	data := make([]byte, hy2UDPMaxDatagram)
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

// hy2DownlinkUDPPeek 是有 reader 时的下行：空闲时阻塞在 MSG_PEEK 上（只窥视 1 字节，
// 数据报留在 socket 里），有包了借收包缓冲收空再还。读截止（会话收尾）同样打断
// 窥视。size 是一次最多收的包数（Linux 32，别的平台 1）。
func hy2DownlinkUDPPeek(conn N.PacketConn, reader hy2UDPReader, size int, down *atomic.Int64) {
	peek := []ipv4.Message{{Buffers: [][]byte{make([]byte, 1)}}}
	probe := min(size, hy2DownlinkProbeBatch)
	// busy：上一轮醒来收到的包超过小组一次能收的，这一轮直接借批量组，省掉小组那次收包。
	busy := false
	for {
		if _, err := reader.ReadBatch(peek, hy2UDPPeekFlag); err != nil {
			return
		}
		total := 0
		if !busy {
			group := hy2DownlinkProbeFree.get()
			n, ok := hy2ReadDownlink(conn, reader, group.messages[:probe], down)
			hy2DownlinkProbeFree.put(group)
			if !ok {
				return
			}
			if n < probe {
				// 小组没收满：已收空。
				continue
			}
			total = n
		}
		more, ok := hy2DrainDownlink(conn, reader, size, down)
		if !ok {
			return
		}
		busy = total+more > probe
	}
}

// hy2DrainDownlink 借一组收包缓冲把 socket 收空，返回收到的包数：批量组有名额时
// 一次收 size 包，名额借光（或 size 不超过小组）时用小组收。
func hy2DrainDownlink(conn N.PacketConn, reader hy2UDPReader, size int, down *atomic.Int64) (int, bool) {
	if size > hy2DownlinkProbeBatch {
		select {
		case hy2DownlinkBatchSlots <- struct{}{}:
			return hy2DrainDownlinkBatch(conn, reader, size, down)
		default:
		}
	}
	group := hy2DownlinkProbeFree.get()
	defer hy2DownlinkProbeFree.put(group)
	return hy2DrainDownlinkGroup(conn, reader, group.messages[:min(size, hy2DownlinkProbeBatch)], down)
}

// hy2DrainDownlinkBatch 用批量组收空；调用方已占到一个名额，这里负责归还（含 panic）。
func hy2DrainDownlinkBatch(conn N.PacketConn, reader hy2UDPReader, size int, down *atomic.Int64) (int, bool) {
	defer func() { <-hy2DownlinkBatchSlots }()
	group := hy2DownlinkBatchFree.get()
	defer hy2DownlinkBatchFree.put(group)
	return hy2DrainDownlinkGroup(conn, reader, group.messages[:size], down)
}

// hy2DrainDownlinkGroup 用 messages 反复收包，收满一批说明可能还有、接着收；
// 不满一批即视为收空（省一次必然落空的收包）。
func hy2DrainDownlinkGroup(conn N.PacketConn, reader hy2UDPReader, messages []ipv4.Message, down *atomic.Int64) (int, bool) {
	received := 0
	for {
		n, ok := hy2ReadDownlink(conn, reader, messages, down)
		received += n
		if !ok || n < len(messages) {
			return received, ok
		}
	}
}

// hy2ReadDownlink 非阻塞地收一批包并写回客户端，返回收到的包数。socket 已空时
// 返回 0 与 true；返回 false 表示会话该结束（上游出错、读截止、写回客户端失败）。
func hy2ReadDownlink(conn N.PacketConn, reader hy2UDPReader, messages []ipv4.Message, down *atomic.Int64) (int, bool) {
	n, err := reader.ReadBatch(messages, hy2UDPDontWaitFlag)
	if err != nil {
		// 窥视到的包在真正收之前被内核丢掉（如校验和错）时会落空，回去接着等。
		return 0, isHy2UDPWouldBlock(err)
	}
	for i := range messages[:n] {
		message := &messages[i]
		source := M.Socksaddr{}
		if addr, ok := message.Addr.(*net.UDPAddr); ok {
			ip, _ := netip.AddrFromSlice(addr.IP)
			source = M.Socksaddr{Addr: ip.Unmap(), Port: uint16(addr.Port)}
		}
		message.Addr = nil
		if !hy2WriteDownlink(conn, message.Buffers[0][:message.N], source, down) {
			return n, false
		}
	}
	return n, true
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
