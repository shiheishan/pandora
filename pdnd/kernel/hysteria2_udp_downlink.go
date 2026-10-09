package kernel

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/net/ipv4"
)

// hy2 / TUIC UDP 下行（上游 → 客户端）。
//
// 冷态（空闲、零星）：阻塞在 MSG_PEEK 上等包，只窥视 1 字节、不占收包缓冲；醒来
// 借 2 包的小组用 MSG_DONTWAIT 收，连收两次都满了说明积压大，换批量组收空，然后
// 全还回去。
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
// 64×GOMAXPROCS 个（4 核 32MB）；批量组只在收积压或卡在写回时占着，受批量名额与
// 份额约束；卡在写回的冷态会话占 128KB。
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

// dense 在冷态每次醒来时判断要不要进热态：当前这一段（起点起不超过
// hy2DownlinkWarmIdle）里已经收够 hy2DownlinkWarmMinPackets 包，与热态续期同一
// 口径。段已过期就作废，从这次醒来起新开一段：一串旧包不能让很久之后的一包进热态。
func (d *hy2Downlink) dense() bool {
	now := hy2StockNow()
	if d.coldStart != 0 && now-d.coldStart <= int64(hy2DownlinkWarmIdle) {
		return d.coldPackets >= hy2DownlinkWarmMinPackets
	}
	d.coldStart, d.coldPackets = now, 0
	return false
}

// warmHeld 在占到热态名额后跑热态，返回时归还名额（含 panic）。
func (d *hy2Downlink) warmHeld() bool {
	defer d.warmLimit.release(d.share)
	return d.warm()
}

// burst 是冷态醒来的一次收包：小组非阻塞收一次，收满了再换批量组（借不到就用
// 小组）收空。返回收到的包数；false 表示会话该结束。
func (d *hy2Downlink) burst() (int, bool) {
	probe := d.probe()
	group := hy2DownlinkProbeStock.get()
	n, ok := d.read(group.messages[:probe], hy2UDPDontWaitFlag)
	if !ok || n < probe {
		hy2DownlinkProbeStock.put(group)
		return n, ok
	}
	// 小组收满：再用小组收一次，还是满的才说明积压大、借批量组。三五包的一小串
	// （游戏、语音的成串包）不占 2MB 的批量组：许多会话同时来一小串、又一起卡在
	// 写回时，批量名额会被它们占满，存货里留下名额 × 2MB（VPC 复测 1024 会话时 64MB）。
	m, ok := d.read(group.messages[:probe], hy2UDPDontWaitFlag)
	n += m
	if !ok || m < probe {
		hy2DownlinkProbeStock.put(group)
		return n, ok
	}
	if d.size > probe {
		if batch, got := d.share.acquireBatchGroup(); got {
			hy2DownlinkProbeStock.put(group)
			more, ok := d.drainBatch(batch)
			return n + more, ok
		}
	}
	more, ok := d.drain(group.messages[:probe])
	hy2DownlinkProbeStock.put(group)
	return n + more, ok
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
		source := M.Socksaddr{}
		if addr, ok := message.Addr.(*net.UDPAddr); ok {
			ip, _ := netip.AddrFromSlice(addr.IP)
			source = M.Socksaddr{Addr: ip.Unmap(), Port: uint16(addr.Port)}
		}
		message.Addr = nil
		if !hy2WriteDownlink(conn, message.Buffers[0][:message.N], source, down) {
			return false
		}
	}
	return true
}
