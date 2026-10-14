package kernel

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/internal/udprecv"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// hy2 / TUIC UDP 下行（上游 → 客户端）。
//
// 冷态（空闲、零星）：等上游 socket 可读，等待期间不占收包缓冲（udprecv.Receiver.Ready：
// 第一次回调只用 1 字节 MSG_PEEK 看有没有包、不借组，没有就交给 netpoller 等；叫醒
// 后才借 2 包的小组、recvmmsg 非阻塞收）。一次醒来「窥视落空 + 收到」两次系统调用、
// 借还一次，与持有缓冲阻塞读（sing-box 的「落空 + 收到」）同形状；之前「阻塞窥视
// 等包、再收」是三次（复审 review-r6 第 1 条：1024 个冷会话时每 Gbps 多约 10% 的
// CPU）。连收两次都满了说明积压大，换批量组收空。每次收完先把包复制出来、归还缓冲
// 再写回，卡在写回时不占收包组（见 burst）。
//
// 热态（包来得密）：冷态里一段（至少 hy2DownlinkWarmIdle）的平均速率到每秒 500 包
// （hy2DownlinkWarmMinPackets 包每 hy2DownlinkWarmIdle），且占得到热态名额，就转入
// 热态：持有小组阻塞读、零拷贝写回，省掉冷态每次醒来的借还与复制。热态一段收到的
// 包不到同一个数就还缓冲、回冷态。热态只持小组；一次收满说明有积压，借批量组（占
// 名额与用户份额）非阻塞收空即还，批量组不陪着等包。
//
// 进出用同一把尺子：一段 100ms 里平均每秒够不够 500 包。
//   - 复审 N1：原先一次醒来收到一串就进热态、段内来过一包就续期，每秒 50 包（游戏、
//     语音每 8–20ms 一包、偶尔成串）就能常年占着 128KB；
//   - 复审 P1：成对到达的低速流量（每 15ms 一对）按「醒来间隔」判会进热态；
//   - 复审 review-r4 L2：一串十几包挤在几毫秒里的中低速流量按「段起点起收够 10 包」
//     判会进热态，改按段龄折算平均速率；
//   - 复审 review-r5 K3：段长 20ms 时，段的起点落在包上，音频（每 20ms 一包）加视频
//     （每 33ms 一串 12 包，合计约每秒 410 包）的会话，常有一段恰好框住一串视频加
//     一两个音频包，折算超过每秒 500 包，名额被这类会话占满。段长取 100ms，一段框
//     住三四串，折算的就是真实的平均速率。
//   热态续期也按 100ms 一段：热态会话每秒的计时器唤醒从 50 次降到 10 次，代价是
//   流量停下后最多多持 200ms 的小组（名额兜着上限）。
//
// 热态名额（hy2DownlinkWarm，每用户最多 1/4）：同时处在热态的会话数有上限，热态
// 小组的常驻（含还回后留在存货里的）才有界。占不到名额的会话照常在冷态收包。
//
// 常驻：空闲与低速会话不占收包缓冲；热态会话占 128KB（小组），全进程最多
// 64×GOMAXPROCS 个（4 核 32MB）；批量组只在收积压或热态卡在写回时占着，受批量名额
// 与份额约束；卡在写回的冷态会话只占它复制出来的那几个包。
//
// 读截止与收尾：转发收尾时 relayHy2UDP 先取消 ctx、再由 AfterFunc 把上游读截止设
// 为现在。这里每次改读截止之后都再看一次 ctx：取消发生在改之前，看得到 ctx 已取消；
// 发生在改之后，AfterFunc 的截止覆盖这里的设置，阻塞读照样被打断。

const (
	// hy2DownlinkWarmIdle 是冷态计包、热态续期共用的一段时长（热态会话每秒多 10 次
	// 计时器唤醒）。
	hy2DownlinkWarmIdle = 100 * time.Millisecond
	// hy2DownlinkWarmMinPackets：一段里收到这么多包（每秒 500 包）冷态才进热态，
	// 热态才续期。
	hy2DownlinkWarmMinPackets = 50
)

// hy2DownlinkSource 是下行的收包来源（生产是 udprecv.Receiver 加上游连接的读截止）。
type hy2DownlinkSource interface {
	// Ready 等到可读再借组收，等待期间不占组，见 udprecv.Receiver.Ready。
	Ready(size int, borrow func() *udprecv.Batch, giveBack func(*udprecv.Batch)) (*udprecv.Batch, int, error)
	// Recv 用给定的组收：wait 时阻塞，否则非阻塞、空了返回 udprecv.ErrWouldBlock。
	Recv(b *udprecv.Batch, size int, wait bool) (int, error)
	SetReadDeadline(t time.Time) error
}

// hy2UpstreamSource 把收包器与上游连接的读截止拼起来（两者是同一个 socket）。
type hy2UpstreamSource struct {
	*udprecv.Receiver
	conn net.PacketConn
}

func (s hy2UpstreamSource) SetReadDeadline(t time.Time) error { return s.conn.SetReadDeadline(t) }

func hy2DownlinkUDP(ctx context.Context, conn N.PacketConn, upstream hy2UDPUpstream, share *hy2BatchShare, down *atomic.Int64) {
	if upstream.recv != nil {
		d := &hy2Downlink{ctx: ctx, conn: conn, src: hy2UpstreamSource{upstream.recv, upstream.conn}, size: hy2UDPBatch, share: share, down: down, warmLimit: hy2DownlinkWarm}
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
	ctx  context.Context
	conn N.PacketConn
	src  hy2DownlinkSource
	// size 是一次最多收的包数（32）。非 Linux 的收包器一次只收一包，收不满小组即
	// 视为收空，不会多一次落空的收包。
	size  int
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

// 冷态借还小组用的函数（顶层函数，传给 Ready 不分配）：先占冷态名额
// （hy2DownlinkColdSlots），再从存货拿；还时先还存货再放名额。热态持组不走这里
// （热态另有名额）。
func hy2BorrowProbe() *udprecv.Batch {
	hy2DownlinkColdSlots <- struct{}{}
	return hy2DownlinkProbeStock.get()
}

func hy2GiveBackProbe(b *udprecv.Batch) {
	hy2DownlinkProbeStock.put(b)
	<-hy2DownlinkColdSlots
}

func (d *hy2Downlink) run() {
	for {
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

// dense 在冷态每收完一次（以及会话开始时）判断要不要进热态，按整段判：计包段从
// 某次判断开始，段龄满 hy2DownlinkWarmIdle 之后的第一次判断，看这一段的平均速率
// 是否到每秒 500 包（段内包数 × 2ms ≥ 段龄），到了就进，然后从这次起新开一段。
// 按段龄折算速率，很久以前的一串旧包也自然不够（过期即作废）。
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

// burst 是冷态的一次醒来，收空为止：先等可读、借 2 包的小组收，收满了接着用小组
// 非阻塞收，两次都收满（积压大）之后借批量组（借不到接着用小组）。每次收完先把包
// 复制成按长度分配的小缓冲、立刻归还收包组，再写回客户端。返回收到的包数；false
// 表示会话该结束。
//
// 冷态复制一次：写回在 QUIC 的 DATAGRAM 发送队列满时会阻塞，许多会话同时来一小串
// 时（VPC 复测：一条连接上 256–512 个会话同时突发）会一起卡在写回。零拷贝时每个卡住
// 的会话都占着 128KB 的小组（或 2MB 的批量组），高峰过后还留在存货里，1024 个会话
// 时堆里约 120MB；复制之后卡住的会话只占它那几个包本身的字节。冷态每秒不到 500 包，
// 复制的开销可以忽略；热态（高速）仍零拷贝。
func (d *hy2Downlink) burst() (int, bool) {
	total := 0
	for round := 0; ; round++ {
		n, size, ok := d.readCopied(round)
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

// hy2DownlinkCopyBudget 是冷态一次复制的上限（按实际分配到的块算，见
// hy2CopyBlockSize），与 2 包小组一样大：卡在写回的冷态会话复制出的缓冲不超过它。
// 小组的两包怎么都放得下（每包的块不超过 64KB）；批量组一次收到的大包超出的部分
// 不复制，见 readCopied。
const hy2DownlinkCopyBudget = hy2DownlinkProbeBatch * hy2UDPMaxDatagram

// 编译期钉住「小组收到的包总能整包复制」：冷态小组因此在还组前不等 I/O，冷态名额
// （hy2DownlinkColdSlots）的等待才有界（复审 review-r6 J1）。每包至多
// hy2UDPMaxDatagram 字节；它是不小于 64 的 2 的幂时，hy2CopyBlockSize 给的块至多就是
// 它本身，所以只要预算 ≥ 小组包数 × hy2UDPMaxDatagram。改小预算、改大单包上限或
// 改成非 2 的幂，下面任一行都会编不过（无符号常量为负）。
const (
	_ = uint(hy2DownlinkCopyBudget - hy2DownlinkProbeBatch*hy2UDPMaxDatagram)
	_ = uint(hy2UDPMaxDatagram - 64)
	_ = uint(0 - hy2UDPMaxDatagram&(hy2UDPMaxDatagram-1))
)

// hy2CopyBlockSize 是 buf.NewSize(n) 实际占的内存：sing 的分配器按 2 的幂分档
// （最小 64B，到 64KB），超过 65535 才按原长分配。Cap() 报的是请求的长度，按它记账
// 会把 4097 字节的包记成 4097、实际占 8KB（复审 review-r5 K1）。
func hy2CopyBlockSize(n int) int {
	switch {
	case n <= 0:
		return 0
	case n > 65535:
		return n
	case n <= 64:
		return 64
	}
	size := 64
	for size < n {
		size <<= 1
	}
	return size
}

// readCopied 收一次（round 0 等可读，之后非阻塞），把包复制进 d.copied，归还收包组
// 后返回包数与这次最多能收的包数。round ≥ 2 时先试借批量组。
//
// 复制按 hy2DownlinkCopyBudget 封顶（复审 review-r4 L1）：一次收到的大包超出上限
// 时，先写回已复制的，再从收包组里零拷贝写回剩下的，写完才归还收包组（批量组连同
// 名额与份额，复审 review-r5 K2）。卡住时占着的是收包组，受批量名额（全局）与用户
// 份额约束；不封顶的话，复制完立刻还名额，每个卡住的会话都能各复制 32 个大包
// （32×64KB = 2MB），常驻就不再受名额约束。小组与批量组走同一段代码。
func (d *hy2Downlink) readCopied(round int) (n, size int, ok bool) {
	size = d.probe()
	var b *udprecv.Batch
	batched := false
	if round == 0 {
		var err error
		b, n, err = d.src.Ready(size, hy2BorrowProbe, hy2GiveBackProbe)
		if err != nil {
			return 0, size, false
		}
	} else {
		if round >= 2 && d.size > size {
			b, batched = d.share.acquireBatchGroup()
		}
		if batched {
			size = d.size
		} else {
			b = hy2BorrowProbe()
		}
		n, ok = d.recv(b, size)
		if !ok {
			d.giveBack(b, batched)
			return 0, size, false
		}
	}
	defer d.giveBack(b, batched)
	// 小组的两包（每包至多 64KB 的块）总能整包复制，下面的零拷贝写回只会发生在
	// 批量组上：冷态小组还组前不等 I/O，冷态名额（hy2DownlinkColdSlots）靠这一点。
	copied := d.copyIn(b, n)
	if copied < n {
		return n, size, d.writeCopied() && hy2WriteDownlinkBatch(d.conn, b, copied, n, d.down)
	}
	return n, size, true
}

// giveBack 归还冷态借的收包组（批量组连同名额与份额）。
func (d *hy2Downlink) giveBack(b *udprecv.Batch, batched bool) {
	if batched {
		d.share.releaseBatchGroup(b)
		return
	}
	hy2GiveBackProbe(b)
}

// copyIn 把 b 里收到的前 n 包逐包复制进 d.copied，按实际占用的块记账，到
// hy2DownlinkCopyBudget 为止；返回复制了的包数。
func (d *hy2Downlink) copyIn(b *udprecv.Batch, n int) int {
	spent := 0
	for i := range n {
		length := b.N[i]
		block := hy2CopyBlockSize(length)
		if spent+block > hy2DownlinkCopyBudget {
			return i
		}
		spent += block
		data := buf.NewSize(length)
		_, _ = data.Write(b.Bufs[i][:length])
		d.copied = append(d.copied, hy2CopiedPacket{data: data, source: hy2Source(b.From[i])})
	}
	return n
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

// recv 非阻塞地收一次。socket 已空返回 0 与 true。
//
// 热态的读截止可能正好在收积压时到点：会话没被取消就当作「先停在这」，交回热态的
// 阻塞读去续期或回冷态，不能当成会话结束。
func (d *hy2Downlink) recv(b *udprecv.Batch, size int) (int, bool) {
	n, err := d.src.Recv(b, size, false)
	if err != nil {
		if errors.Is(err, udprecv.ErrWouldBlock) {
			return 0, true
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return 0, d.ctx.Err() == nil
		}
		return 0, false
	}
	return n, true
}

// drainBatch 用借到的批量组非阻塞地反复收、零拷贝写回，直到一次收不满（视为收空），
// 归还名额与份额（写回 panic 也还）。
func (d *hy2Downlink) drainBatch(batch *udprecv.Batch) (int, bool) {
	defer d.share.releaseBatchGroup(batch)
	received := 0
	for {
		n, ok := d.recv(batch, d.size)
		received += n
		if !ok || !hy2WriteDownlinkBatch(d.conn, batch, 0, n, d.down) {
			return received, false
		}
		if n < d.size {
			return received, true
		}
	}
}

// warm 是热态：持有小组阻塞读，一次收满就借批量组非阻塞收空、立即还（批量组
// 只在真有积压时占着，不陪着等包）。一段 hy2DownlinkWarmIdle 里收到的包不到
// hy2DownlinkWarmMinPackets 就还小组、清读截止并返回 true（回冷态）；false 表示
// 会话该结束。
func (d *hy2Downlink) warm() bool {
	probe := d.probe()
	group := hy2DownlinkProbeStock.get()
	defer hy2DownlinkProbeStock.put(group)
	if !d.setDeadline(time.Now().Add(hy2DownlinkWarmIdle)) {
		return false
	}
	received := 0
	for {
		n, err := d.src.Recv(group, probe, true)
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
		if !hy2WriteDownlinkBatch(d.conn, group, 0, n, d.down) {
			return false
		}
		if n == probe && d.size > probe {
			// 小组一次收满：还有积压，借批量组收空（名额或份额不够就接着用小组读）。
			if batch, got := d.share.acquireBatchGroup(); got {
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

// hy2WriteDownlinkBatch 把 b 里第 from 到 to（不含）包按序零拷贝写回客户端。
func hy2WriteDownlinkBatch(conn N.PacketConn, b *udprecv.Batch, from, to int, down *atomic.Int64) bool {
	for i := from; i < to; i++ {
		if !hy2WriteDownlink(conn, b.Bufs[i][:b.N[i]], hy2Source(b.From[i]), down) {
			return false
		}
	}
	return true
}

// hy2Source 把收包器给的来源转成写回用的地址（IPv4 映射地址已还原成 IPv4）。
func hy2Source(from netip.AddrPort) M.Socksaddr {
	return M.Socksaddr{Addr: from.Addr(), Port: from.Port()}
}
