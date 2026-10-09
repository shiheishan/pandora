package kernel

import (
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// memTestClientConn 是 hy2 / TUIC 会话连接（nativewire 的 udpPacketConn）的替身：
// 实现转发热路径用到的阻塞读、零拷贝等待读、非阻塞读与写回，收到的回包只计数。
type memTestClientConn struct {
	// block 非 nil 时 WritePacket 计数后卡住，模拟 QUIC 的 DATAGRAM 发送队列满。
	block    chan struct{}
	in       chan memTestPacket
	done     chan struct{}
	doneOnce sync.Once
	replies  atomic.Int64
	replied  chan struct{}
}

type memTestPacket struct {
	data        *buf.Buffer
	destination M.Socksaddr
}

func newMemTestClientConn() *memTestClientConn {
	return &memTestClientConn{in: make(chan memTestPacket, 64), done: make(chan struct{}), replied: make(chan struct{}, 1024)}
}

func (c *memTestClientConn) send(payload []byte, destination M.Socksaddr) {
	data := buf.NewSize(len(payload))
	_, _ = data.Write(payload)
	c.in <- memTestPacket{data: data, destination: destination}
}

func (c *memTestClientConn) stop() { c.doneOnce.Do(func() { close(c.done) }) }

func (c *memTestClientConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	select {
	case p := <-c.in:
		_, err := buffer.ReadOnceFrom(p.data)
		p.data.Release()
		return p.destination, err
	case <-c.done:
		return M.Socksaddr{}, io.ErrClosedPipe
	}
}

func (c *memTestClientConn) TryReadPacket(buffer *buf.Buffer) (M.Socksaddr, bool) {
	select {
	case p := <-c.in:
		_, _ = buffer.ReadOnceFrom(p.data)
		p.data.Release()
		return p.destination, true
	default:
		return M.Socksaddr{}, false
	}
}

func (c *memTestClientConn) InitializeReadWaiter(N.ReadWaitOptions) bool { return false }

func (c *memTestClientConn) WaitReadPacket() (*buf.Buffer, M.Socksaddr, error) {
	select {
	case p := <-c.in:
		return p.data, p.destination, nil
	case <-c.done:
		return nil, M.Socksaddr{}, io.ErrClosedPipe
	}
}

func (c *memTestClientConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	c.replies.Add(1)
	select {
	case c.replied <- struct{}{}:
	default:
	}
	if c.block != nil {
		select {
		case <-c.block:
		case <-c.done:
			return io.ErrClosedPipe
		}
	}
	return nil
}

func (c *memTestClientConn) Close() error                     { c.stop(); return nil }
func (c *memTestClientConn) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *memTestClientConn) SetDeadline(t time.Time) error    { return c.SetReadDeadline(t) }
func (c *memTestClientConn) SetWriteDeadline(time.Time) error { return nil }

// SetReadDeadline 只认「立即」：转发收尾时用它打断阻塞读。
func (c *memTestClientConn) SetReadDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		c.stop()
	}
	return nil
}

// heapAfterGC 返回两轮 GC 之后的存活堆字节数。
func heapAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// memTestSessions 起 n 个 hy2 / TUIC UDP 会话（relayHy2UDP），上游是真实的回环
// UDP socket（裸 socket，与私网放开时的直连同一条批量路径），每个会话先完成
// 一来一回。返回收尾函数。
func memTestSessions(t *testing.T, n int, echo net.Addr, block chan struct{}) ([]*memTestClientConn, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	conns := make([]*memTestClientConn, n)
	var up, down atomic.Int64
	target := M.SocksaddrFromNet(echo)
	for i := range conns {
		upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		conn := newMemTestClientConn()
		conn.block = block
		conns[i] = conn
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer upstream.Close()
			relayHy2UDP(ctx, conn, upstream, target, int64(i), &up, &down)
		}()
		conn.send([]byte("ping"), target)
	}
	for i, conn := range conns {
		select {
		case <-conn.replied:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatalf("会话 %d 没收到回包", i)
		}
	}
	return conns, func() {
		cancel()
		for _, conn := range conns {
			conn.stop()
		}
		wg.Wait()
	}
}

// 缺陷 1（vpcnode2）：空闲的 hy2 / TUIC UDP 会话不能常驻批量收发缓冲。每个会话
// 只走一来一回后空闲，存活堆按会话平均不超过 64KB（改前约 2.6MB：下行 32×64KB、
// 上行 32×16KB）。批量开关强制打开，非 Linux 上也量生产的批量路径。
func TestHy2IdleUDPSessionHeap(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	hy2UDPBatchSupported = true

	const sessions = 256
	echo := hy2EchoUDP(t)
	before := heapAfterGC()
	_, stop := memTestSessions(t, sessions, echo, nil)
	after := heapAfterGC()
	stop()
	perSession := (int64(after) - int64(before)) / sessions
	t.Logf("%d 个空闲会话：存活堆 %d → %d 字节，平均每会话 %d 字节", sessions, before, after, perSession)
	if os.Getenv("PDND_HY2_MEM_REPORT") != "" {
		return
	}
	if perSession > 64<<10 {
		t.Fatalf("每个空闲会话常驻 %d 字节，超过 64KB", perSession)
	}
}

// 活跃会话：每个会话持续收发小包，批量缓冲只在真正收发的那一刻从共享池借用，
// 存活堆随「同时在收发的会话数」走，而不是随会话总数走。
func TestHy2ActiveUDPSessionHeap(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	hy2UDPBatchSupported = true

	sessions := hy2LoadEnvInt("PDND_HY2_MEM_SESSIONS", 256)
	interval := time.Duration(hy2LoadEnvInt("PDND_HY2_MEM_INTERVAL_MS", 10)) * time.Millisecond
	echo := hy2EchoUDP(t)
	before := heapAfterGC()
	conns, stop := memTestSessions(t, sessions, echo, nil)
	defer stop()
	target := M.SocksaddrFromNet(echo)
	trafficDone := make(chan struct{})
	var traffic sync.WaitGroup
	for _, conn := range conns {
		traffic.Add(1)
		go func() {
			defer traffic.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-trafficDone:
					return
				case <-ticker.C:
					conn.send([]byte("ping"), target)
				}
			}
		}()
	}
	var peak uint64
	for range 5 {
		time.Sleep(200 * time.Millisecond)
		peak = max(peak, heapAfterGC())
	}
	close(trafficDone)
	traffic.Wait()
	var replies int64
	for _, conn := range conns {
		replies += conn.replies.Load()
	}
	perSession := (int64(peak) - int64(before)) / int64(sessions)
	t.Logf("%d 个活跃会话（每会话每 %v 一包，共回包 %d）：存活堆峰值 %d → %d 字节，平均每会话 %d 字节", sessions, interval, replies, before, peak, perSession)
	if os.Getenv("PDND_HY2_MEM_REPORT") != "" {
		return
	}
	if replies < int64(sessions) {
		t.Fatalf("回包太少：%d", replies)
	}
	if perSession > 64<<10 {
		t.Fatalf("每个活跃会话常驻 %d 字节，超过 64KB", perSession)
	}
}

// 写回客户端卡住（QUIC 连接拥塞，或客户端故意不回 ACK）时，冷态会话只占着它复制
// 出来的那几个包：收包组在写回之前就还了（VPC 复测里一条连接上几百个会话同时突发、
// 一起卡在写回，零拷贝时每个占 128KB 小组）。预算每个卡住的会话 32KB；改回零拷贝
// 写回，每个会话约 135KB。
func TestHy2BlockedUDPSessionHeap(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	hy2UDPBatchSupported = true

	const sessions = 512
	slots := cap(hy2DownlinkBatchSlots)
	echo := hy2EchoUDP(t)
	block := make(chan struct{})
	before := heapAfterGC()
	_, stop := memTestSessions(t, sessions, echo, block)
	after := heapAfterGC()
	close(block)
	stop()
	delta := int64(after) - int64(before)
	limit := int64(sessions) * (32 << 10)
	t.Logf("%d 个会话卡在写回：存活堆 %d → %d 字节（增 %d），批量组名额 %d，上限 %d", sessions, before, after, delta, slots, limit)
	if delta > limit {
		t.Fatalf("卡住的会话占了 %d 字节，超过 %d", delta, limit)
	}
	if len(hy2DownlinkBatchSlots) != 0 {
		t.Fatalf("会话收尾后批量组名额没还：%d", len(hy2DownlinkBatchSlots))
	}
}
