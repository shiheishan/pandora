package kernel

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/ipv4"
)

// hy2 / TUIC UDP 下行（上游 → 客户端）。
//
// 冷态（空闲、零星）：阻塞在 MSG_PEEK 上等包，只窥视 1 字节、不占收包缓冲；醒来
// 借 2 包的小组用 MSG_DONTWAIT 收，连收两次都满了说明积压大，换批量组收空。每次
// 收完先把包复制出来、归还缓冲再写回，卡在写回时不占收包组（见 burst）。
//
// 热态（包来得密）：冷态里一段 hy2DownlinkWarmIdle 收到的包达到
// hy2DownlinkWarmMinPackets（每秒 500 包），且占得到热态名额，就转入热态：持有
// 收包缓冲阻塞读，和改前一样每包只要「落空 + 收到」两次 recvmmsg，不再每包多一次
// 窥视。热态一段收到的包不到同一个数就还缓冲、回冷态。热态只持小组；一次收满
// 说明有积压，借批量组（占名额与用户份额）非阻塞收空即还，批量组不陪着等包。
//
// 进出用同一把尺子：一段 20ms 里够不够 10 包。
//   - 复审 N1：原先一次醒来收到一串就进热态、段内来过一包就续期，每秒 50 包（游戏、
//     语音每 8–20ms 一包、偶尔成串）就能常年占着 128KB，一个用户 1024 个会话约 128MB；
//   - 复审 P1：进入改按包数之前看的是「两次醒来间隔不到 2ms」，成对到达的低速流量
//     （每 15ms 一对、对内相隔 0.5ms，约每秒 133 包）每一对都进一段热态，256 个
//     这样的会话就能把热态名额占满。按包数算，这类会话一段只有两三包，进不去。
//   一串十几包的突发能让会话进一段热态，下一段不够包就出来，名额兜着上限。
//
// 热态名额（hy2DownlinkWarm，每用户最多 1/4）：同时处在热态的会话数有上限，热态
// 小组的常驻（含还回后留在存货里的）才有界。占不到名额的会话照常在冷态收包，
// 每包多一次窥视。
//
// 常驻：空闲与低速会话不占收包缓冲；热态会话占 128KB（小组），全进程最多
// 64×GOMAXPROCS 个（4 核 32MB）；批量组只在收积压或热态卡在写回时占着，受批量名额
// 与份额约束；卡在写回的冷态会话只占它复制出来的那几个包。
//
// 读截止与收尾：转发收尾时 relayHy2UDP 先取消 ctx、再由 AfterFunc 把上游读截止设
// 为现在。这里每次改读截止之后都再看一次 ctx：取消发生在改之前，看得到 ctx 已取消；
// 发生在改之后，AfterFunc 的截止覆盖这里的设置，阻塞读照样被打断。

const (
	// hy2DownlinkWarmIdle 是冷态计包、热态续期共用的一段时长（热态会话每秒多 50 次
	// 计时器唤醒）。
	hy2DownlinkWarmIdle = 20 * time.Millisecond
	// hy2DownlinkWarmMinPackets：一段里收到这么多包（每秒 500 包）冷态才进热态，
	// 热态才续期。
	hy2DownlinkWarmMinPackets = 10
)

// hy2DownlinkSource 是下行的收包来源：带 flags 的收包（窥视、非阻塞）加读截止。
type hy2DownlinkSource interface {
	hy2UDPReader
	SetReadDeadline(t time.Time) error
}

// hy2UpstreamSource 把上游的 reader 与上游连接的读截止拼起来（两者是同一个 socket）。
type hy2UpstreamSource struct {
	hy2UDPReader
	conn net.PacketConn
}

func (s hy2UpstreamSource) SetReadDeadline(t time.Time) error { return s.conn.SetReadDeadline(t) }

func hy2DownlinkUDP(ctx context.Context, conn N.PacketConn, upstream hy2UDPUpstream, share *hy2BatchShare, down *atomic.Int64) {
	if upstream.reader != nil {
		size := 1
		if hy2UDPBatchSupported {
			size = hy2UDPBatch
		}
		d := &hy2Downlink{ctx: ctx, conn: conn, src: hy2UpstreamSource{upstream.reader, upstream.conn}, size: size, share: share, down: down, warmLimit: hy2DownlinkWarm}
		d.run()
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

// hy2Downlink 是一个会话的下行状态，只在下行 goroutine 里用。
type hy2Downlink struct {
	ctx   context.Context
	conn  N.PacketConn
	src   hy2DownlinkSource
	size  int // 一次最多收的包数（Linux 32，别的平台 1）
	share *hy2BatchShare
	down  *atomic.Int64
	// warmLimit 是热态名额（生产为 hy2DownlinkWarm，测试注入自己的）。
	warmLimit *hy2WarmLimit
	// coldStart / coldPackets 是冷态当前计包段的起点（hy2StockNow）与段内收到的包数。
	coldStart   int64
	coldPackets int
	// copied 是冷态复制出来、等着写回的包（容量随最大一批，最多 32 条）。
	copied []hy2CopiedPacket
}

func (d *hy2Downlink) probe() int { return min(d.size, hy2DownlinkProbeBatch) }

func (d *hy2Downlink) run() {
	peek := []ipv4.Message{{Buffers: [][]byte{make([]byte, 1)}}}
	for {
		if _, err := d.src.ReadBatch(peek, hy2UDPPeekFlag); err != nil {
			return
		}
		if d.dense() && d.warmLimit.acquire(d.share) {
			ok := d.warmHeld()
			d.coldStart, d.coldPackets = 0, 0
			if !ok {
				return
			}
			continue
		}
		n, ok := d.burst()
		if !ok {
			return
		}
		d.coldPackets += n
	}
}

// dense 在冷态每次醒来时判断要不要进热态，按整段判：计包段从某次醒来开始，段龄
// 满 hy2DownlinkWarmIdle 之后的第一次醒来，看这一段的平均速率是否到每秒 500 包
// （段内包数 × 2ms ≥ 段龄），到了就进，然后从这次醒来起新开一段。
//
// 和热态续期（一段 20ms 不足 10 包就出）用同一把尺子。只按「段起点起收够 10 包」
// 判（半段判）时，一串十几包挤在几毫秒里的中低速流量（每 33ms 一串 12 包，约每秒
// 360 包）每串都进一段热态，能把名额占满（复审 review-r4 L2）。按段龄折算速率，
// 很久以前的一串旧包也自然不够（过期即作废）。
func (d *hy2Downlink) dense() bool {
	now := hy2StockNow()
	age := now - d.coldStart
	if d.coldStart != 0 && age < int64(hy2DownlinkWarmIdle) {
		return false
	}
	enough := d.coldStart != 0 && int64(d.coldPackets)*int64(hy2DownlinkWarmIdle/hy2DownlinkWarmMinPackets) >= age
	d.coldStart, d.coldPackets = now, 0
	return enough
}

// warmHeld 在占到热态名额后跑热态，返回时归还名额（含 panic）。
func (d *hy2Downlink) warmHeld() bool {
	defer d.warmLimit.release(d.share)
	return d.warm()
}

// burst 是冷态醒来的一次收包，收空为止：前两次用 2 包的小组，两次都收满（积压大）
// 之后借批量组（借不到接着用小组）。每次收完先把包复制成按长度分配的小缓冲、立刻
// 归还收包组，再写回客户端。返回收到的包数；false 表示会话该结束。
//
// 冷态复制一次：写回在 QUIC 的 DATAGRAM 发送队列满时会阻塞，许多会话同时来一小串
// 时（VPC 复测：一条连接上 256–512 个会话同时突发）会一起卡在写回。零拷贝时每个卡住
// 的会话都占着 128KB 的小组（或 2MB 的批量组），高峰过后还留在存货里，1024 个会话
// 时堆里约 120MB；复制之后卡住的会话只占它那几个包本身的字节。冷态每秒不到 500 包，
// 复制的开销可以忽略；热态（高速）仍零拷贝。
func (d *hy2Downlink) burst() (int, bool) {
	total := 0
	for round := 0; ; round++ {
		n, size, ok := d.readCopied(round >= 2)
		total += n
		if !ok || !d.writeCopied() {
			d.dropCopied()
			return total, false
		}
		if n < size {
			return total, true
		}
	}
}

// hy2CopiedPacket 是冷态复制出来、等着写回的一个包。
type hy2CopiedPacket struct {
	data   *buf.Buffer
	source M.Socksaddr
}

// hy2DownlinkCopyBudget 是冷态一次复制的容量上限（按分配到的缓冲容量算），与 2 包
// 小组一样大：卡在写回的冷态会话复制出的缓冲不超过它。hy2 单包上限 4096
// （protocol.MaxUDPSize），32 包恰好 128KB，不会超出；TUIC 单包可到 0xffff，大包时
// 一批里超出的部分不复制，见 readCopied。
const hy2DownlinkCopyBudget = hy2DownlinkProbeBatch * hy2UDPMaxDatagram

// readCopied 借一组收包缓冲非阻塞收一次，把包复制进 d.copied，归还缓冲后返回包数与
// 这次最多能收的包数。wantBatch 时先试借批量组。
//
// 复制按 hy2DownlinkCopyBudget 封顶（复审 review-r4 L1）：批量组一次收到的大包超出
// 上限时，先写回已复制的，再从批量组里零拷贝写回剩下的，写完才归还批量组。卡住时
// 占着的是批量组，受批量名额（全局）与用户份额约束；不封顶的话，复制完立刻还名额，
// 每个卡住的会话都能各复制 32 个大包（32×64KB = 2MB），常驻就不再受名额约束。
func (d *hy2Downlink) readCopied(wantBatch bool) (n, size int, ok bool) {
	probe := d.probe()
	if wantBatch && d.size > probe {
		if batch, got := d.share.acquireBatchGroup(); got {
			defer d.share.releaseBatchGroup(batch)
			messages := batch.messages[:d.size]
			n, copied, ok := d.copyRead(messages)
			if ok && copied < n {
				ok = d.writeCopied() && hy2WriteDownlinkMessages(d.conn, messages[copied:n], d.down)
			}
			return n, d.size, ok
		}
	}
	group := hy2DownlinkProbeStock.get()
	defer hy2DownlinkProbeStock.put(group)
	n, _, ok = d.copyRead(group.messages[:probe])
	return n, probe, ok
}

// copyRead 非阻塞地收一批，逐包复制进 d.copied，复制的容量到 hy2DownlinkCopyBudget
// 为止；返回收到的包数与复制了的包数（见 read 的错误语义）。
func (d *hy2Downlink) copyRead(messages []ipv4.Message) (int, int, bool) {
	n, err := d.src.ReadBatch(messages, hy2UDPDontWaitFlag)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return 0, 0, d.ctx.Err() == nil
		}
		return 0, 0, isHy2UDPWouldBlock(err)
	}
	spent := 0
	for i := range messages[:n] {
		message := &messages[i]
		data := buf.NewSize(message.N)
		if spent+data.Cap() > hy2DownlinkCopyBudget {
			data.Release()
			return n, i, true
		}
		spent += data.Cap()
		_, _ = data.Write(message.Buffers[0][:message.N])
		d.copied = append(d.copied, hy2CopiedPacket{data: data, source: hy2MessageSource(message)})
		message.Addr = nil
	}
	return n, n, true
}

// writeCopied 按序写回复制出来的包。WritePacket 接手缓冲（写完或出错都由它归还）。
func (d *hy2Downlink) writeCopied() bool {
	for i := range d.copied {
		packet := d.copied[i]
		d.copied[i] = hy2CopiedPacket{}
		size := packet.data.Len()
		if err := d.conn.WritePacket(packet.data, packet.source); err != nil {
			d.copied = d.copied[i+1:]
			return false
		}
		d.down.Add(int64(size))
	}
	d.copied = d.copied[:0]
	return true
}

// dropCopied 归还没写回的复制包（会话结束时）。
func (d *hy2Downlink) dropCopied() {
	for i := range d.copied {
		if d.copied[i].data != nil {
			d.copied[i].data.Release()
		}
		d.copied[i] = hy2CopiedPacket{}
	}
	d.copied = d.copied[:0]
}

// drainBatch 用借到的批量组收空，归还名额与份额（写回 panic 也还）。
func (d *hy2Downlink) drainBatch(batch *hy2DownlinkGroup) (int, bool) {
	defer d.share.releaseBatchGroup(batch)
	return d.drain(batch.messages[:d.size])
}

// drain 非阻塞地反复收，直到一次收不满（视为收空）。
func (d *hy2Downlink) drain(messages []ipv4.Message) (int, bool) {
	received := 0
	for {
		n, ok := d.read(messages, hy2UDPDontWaitFlag)
		received += n
		if !ok || n < len(messages) {
			return received, ok
		}
	}
}

// read 非阻塞地收一批并写回客户端。socket 已空时返回 0 与 true（窥视到的包在
// 真正收之前被内核丢掉，如校验和错，也是这样）。
//
// 热态的读截止可能正好在收积压时到点，非阻塞收包同样报超时：会话没被取消就当作
// 「先停在这」，交回热态的阻塞读去续期或回冷态，不能当成会话结束。
func (d *hy2Downlink) read(messages []ipv4.Message, flags int) (int, bool) {
	n, err := d.src.ReadBatch(messages, flags)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return 0, d.ctx.Err() == nil
		}
		return 0, isHy2UDPWouldBlock(err)
	}
	return n, hy2WriteDownlinkMessages(d.conn, messages[:n], d.down)
}

// warm 是热态：持有小组阻塞读，一次收满就借批量组非阻塞收空、立即还（批量组
// 只在真有积压时占着，不陪着等包）。一段 hy2DownlinkWarmIdle 里收到的包不到
// hy2DownlinkWarmMinPackets 就还小组、清读截止并返回 true（回冷态）；false 表示
// 会话该结束。
func (d *hy2Downlink) warm() bool {
	probe := d.probe()
	group := hy2DownlinkProbeStock.get()
	defer hy2DownlinkProbeStock.put(group)
	messages := group.messages[:probe]
	if !d.setDeadline(time.Now().Add(hy2DownlinkWarmIdle)) {
		return false
	}
	received := 0
	for {
		n, err := d.src.ReadBatch(messages, 0)
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) || d.ctx.Err() != nil {
				return false
			}
			if received >= hy2DownlinkWarmMinPackets {
				// 这一段仍够密：续一段。
				received = 0
				if !d.setDeadline(time.Now().Add(hy2DownlinkWarmIdle)) {
					return false
				}
				continue
			}
			return d.setDeadline(time.Time{})
		}
		received += n
		if !hy2WriteDownlinkMessages(d.conn, messages[:n], d.down) {
			return false
		}
		if n == probe && d.size > probe {
			// 小组一次收满：还有积压，借批量组收空（名额或份额不够就接着用小组读）。
			if batch, ok := d.share.acquireBatchGroup(); ok {
				more, ok := d.drainBatch(batch)
				if !ok {
					return false
				}
				received += more
			}
		}
	}
}

// setDeadline 改上游读截止，再确认会话没被取消（见文件头「读截止与收尾」）。
func (d *hy2Downlink) setDeadline(t time.Time) bool {
	_ = d.src.SetReadDeadline(t)
	return d.ctx.Err() == nil
}

// hy2WriteDownlinkMessages 把收到的一批包按序写回客户端。写回之后清掉消息里的
// 来源地址：缓冲还回存货后不留对上一个会话的引用。
func hy2WriteDownlinkMessages(conn N.PacketConn, messages []ipv4.Message, down *atomic.Int64) bool {
	for i := range messages {
		message := &messages[i]
		source := hy2MessageSource(message)
		message.Addr = nil
		if !hy2WriteDownlink(conn, message.Buffers[0][:message.N], source, down) {
			return false
		}
	}
	return true
}

// hy2MessageSource 取一条收到的消息的来源地址（IPv4 映射地址还原成 IPv4）。
func hy2MessageSource(message *ipv4.Message) M.Socksaddr {
	addr, ok := message.Addr.(*net.UDPAddr)
	if !ok {
		return M.Socksaddr{}
	}
	ip, _ := netip.AddrFromSlice(addr.IP)
	return M.Socksaddr{Addr: ip.Unmap(), Port: uint16(addr.Port)}
}
