//go:build unix

package kernel

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/ipv4"
)

// 下行收包逻辑的守卫：用可控的上游替身驱动 hy2Downlink，一次收包能返回多包，
// 批量组、名额、份额与热态在任何平台都走得到（真 socket 在 darwin 上一次只收一包，
// 批量路径只在 Linux CI 上走到）。

// fakeUpstream 是上游 socket 的替身：窥视、非阻塞、阻塞收包与读截止都按内核语义。
type fakeUpstream struct {
	mu       sync.Mutex
	queue    [][]byte
	closed   bool
	deadline time.Time
	timer    *time.Timer
	notify   chan struct{}
	// calls 是收包调用次数（含窥视），maxBatch 是非窥视收包一次给的最多条数。
	calls    atomic.Int64
	maxBatch atomic.Int64
}

var errFakeUpstreamClosed = errors.New("fake upstream closed")

func newFakeUpstream() *fakeUpstream { return &fakeUpstream{notify: make(chan struct{}, 1)} }

func (u *fakeUpstream) wake() {
	select {
	case u.notify <- struct{}{}:
	default:
	}
}

func (u *fakeUpstream) push(payloads ...[]byte) {
	u.mu.Lock()
	u.queue = append(u.queue, payloads...)
	u.mu.Unlock()
	u.wake()
}

func (u *fakeUpstream) close() {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
	u.wake()
}

// SetReadDeadline 与内核一样只设一次计时器，到点叫醒阻塞的读。
func (u *fakeUpstream) SetReadDeadline(t time.Time) error {
	u.mu.Lock()
	u.deadline = t
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	if !t.IsZero() {
		u.timer = time.AfterFunc(time.Until(t), u.wake)
	}
	u.mu.Unlock()
	u.wake()
	return nil
}

func (u *fakeUpstream) ReadBatch(ms []ipv4.Message, flags int) (int, error) {
	u.calls.Add(1)
	peek := flags&hy2UDPPeekFlag != 0
	if !peek {
		for {
			seen := u.maxBatch.Load()
			if int64(len(ms)) <= seen || u.maxBatch.CompareAndSwap(seen, int64(len(ms))) {
				break
			}
		}
	}
	for {
		u.mu.Lock()
		if u.closed {
			u.mu.Unlock()
			return 0, errFakeUpstreamClosed
		}
		deadline := u.deadline
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			u.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		if len(u.queue) > 0 {
			if peek {
				u.mu.Unlock()
				ms[0].N = 1
				return 1, nil
			}
			n := min(len(u.queue), len(ms))
			for i := range n {
				ms[i].N = copy(ms[i].Buffers[0], u.queue[i])
				ms[i].Addr = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 53}
			}
			u.queue = u.queue[n:]
			u.mu.Unlock()
			return n, nil
		}
		u.mu.Unlock()
		if flags&hy2UDPDontWaitFlag != 0 {
			return 0, syscall.EAGAIN
		}
		<-u.notify
	}
}

// downlinkTestConn 记写回客户端的包；hold 非 nil 时写回第 after 包起卡住，模拟
// QUIC 的 DATAGRAM 发送队列满。
type downlinkTestConn struct {
	*memTestClientConn
	writes atomic.Int64
	after  int64
	hold   chan struct{}
	every  int64
	delay  time.Duration
	check  func([]byte) bool
	bad    atomic.Int64
}

func (c *downlinkTestConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	n := c.writes.Add(1)
	if c.check != nil {
		runtime.Gosched()
		if !c.check(buffer.Bytes()) {
			c.bad.Add(1)
		}
	}
	if c.hold != nil && n > c.after {
		<-c.hold
	}
	if c.every > 0 && n%c.every == 0 {
		time.Sleep(c.delay)
	}
	return nil
}

// runTestDownlink 起一个会话的下行（批量 32 包），返回等它结束的函数。
func runTestDownlink(conn *downlinkTestConn, src *fakeUpstream, userID int64) func() {
	share := hy2DownlinkBatchShares.join(userID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer hy2DownlinkBatchShares.leave(userID)
		var down atomic.Int64
		d := &hy2Downlink{ctx: context.Background(), conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down}
		d.run()
	}()
	return func() { <-done }
}

func waitDownlink(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等不到：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 借还不重新分配（审查探针 1）：多个会话同时持组、写回不时停顿，批量组新分配
// 的次数不超过名额，小组不超过会话数。改回定长空闲表（表满即丢、表空即分配），
// 这里每秒要新分配上千组。
func TestHy2DownlinkBuffersNotReallocated(t *testing.T) {
	sessions := 2 * cap(hy2DownlinkBatchSlots)
	duration := time.Second
	if raceEnabled {
		duration = 300 * time.Millisecond
	}
	batchBefore, probeBefore := hy2DownlinkBatchStock.allocs.Load(), hy2DownlinkProbeStock.allocs.Load()
	srcs := make([]*fakeUpstream, sessions)
	var waits []func()
	var conns []*downlinkTestConn
	for i := range srcs {
		srcs[i] = newFakeUpstream()
		conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn(), every: 16, delay: 200 * time.Microsecond}
		conns = append(conns, conn)
		waits = append(waits, runTestDownlink(conn, srcs[i], int64(1000+i)))
	}
	stop := make(chan struct{})
	var feeders sync.WaitGroup
	payload := make([]byte, 1200)
	for _, src := range srcs {
		feeders.Add(1)
		go func() {
			defer feeders.Done()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					burst := make([][]byte, 8)
					for k := range burst {
						burst[k] = payload
					}
					src.push(burst...)
				}
			}
		}()
	}
	time.Sleep(duration)
	close(stop)
	feeders.Wait()
	for _, src := range srcs {
		src.close()
	}
	for _, wait := range waits {
		wait()
	}
	var packets int64
	for _, conn := range conns {
		packets += conn.writes.Load()
	}
	batchAllocs := hy2DownlinkBatchStock.allocs.Load() - batchBefore
	probeAllocs := hy2DownlinkProbeStock.allocs.Load() - probeBefore
	t.Logf("%d 个会话写回 %d 包：批量组新分配 %d 次（名额 %d），小组新分配 %d 次", sessions, packets, batchAllocs, cap(hy2DownlinkBatchSlots), probeAllocs)
	if packets < int64(sessions)*50 {
		t.Fatalf("写回太少：%d", packets)
	}
	if batchAllocs > int64(cap(hy2DownlinkBatchSlots)) {
		t.Fatalf("批量组新分配 %d 次，超过名额 %d：借还在重新分配", batchAllocs, cap(hy2DownlinkBatchSlots))
	}
	if probeAllocs > int64(sessions) {
		t.Fatalf("小组新分配 %d 次，超过会话数 %d：借还在重新分配", probeAllocs, sessions)
	}
	if len(hy2DownlinkBatchSlots) != 0 {
		t.Fatalf("名额没还：%d", len(hy2DownlinkBatchSlots))
	}
}

// 跨会话隔离（审查探针）：多个会话共用存货里的缓冲，各自的负载字节不同、写回时
// 让出调度；任何一包写回的内容不属于本会话即串数据。把「写回完才还」改成「收到
// 就还」，这里立刻出现串包。
func TestHy2DownlinkCrossSessionIsolation(t *testing.T) {
	const sessions = 16
	srcs := make([]*fakeUpstream, sessions)
	conns := make([]*downlinkTestConn, sessions)
	var waits []func()
	for i := range srcs {
		id := byte(i + 1)
		srcs[i] = newFakeUpstream()
		conns[i] = &downlinkTestConn{memTestClientConn: newMemTestClientConn(), check: func(b []byte) bool {
			time.Sleep(time.Microsecond)
			for _, x := range b {
				if x != id {
					return false
				}
			}
			return len(b) == 64
		}}
		waits = append(waits, runTestDownlink(conns[i], srcs[i], int64(2000+i%4)))
	}
	for range 200 {
		for i, src := range srcs {
			burst := make([][]byte, 8)
			for k := range burst {
				payload := make([]byte, 64)
				for j := range payload {
					payload[j] = byte(i + 1)
				}
				burst[k] = payload
			}
			src.push(burst...)
		}
		time.Sleep(200 * time.Microsecond)
	}
	waitDownlink(t, "全部写回", func() bool {
		var n int64
		for _, conn := range conns {
			n += conn.writes.Load()
		}
		return n == sessions*200*8
	})
	for _, src := range srcs {
		src.close()
	}
	for _, wait := range waits {
		wait()
	}
	var bad int64
	for _, conn := range conns {
		bad += conn.bad.Load()
	}
	if bad != 0 {
		t.Fatalf("%d / %d 包写回的内容不属于本会话", bad, sessions*200*8)
	}
}

// 名额占满（审查探针 2）：许多用户各有会话卡在写回、占满全局名额后，新会话照常
// 完整、按序转发（用小组收）；全部收尾后名额归零。
func TestHy2DownlinkSlotsExhaustedOthersStillForward(t *testing.T) {
	slots := cap(hy2DownlinkBatchSlots)
	hold := make(chan struct{})
	var holders []*fakeUpstream
	var waits []func()
	for i := range slots + 3 {
		src := newFakeUpstream()
		holders = append(holders, src)
		src.push(make([]byte, 100), make([]byte, 100), make([]byte, 100), make([]byte, 100))
		conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn(), after: 2, hold: hold}
		// 每个用户一个会话：各自的份额都够，占满的是全局名额。
		waits = append(waits, runTestDownlink(conn, src, int64(3000+i)))
	}
	waitDownlink(t, "名额占满", func() bool { return len(hy2DownlinkBatchSlots) == slots })

	src := newFakeUpstream()
	var mu sync.Mutex
	var got [][]byte
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn(), check: func(b []byte) bool {
		mu.Lock()
		got = append(got, append([]byte(nil), b...))
		mu.Unlock()
		return true
	}}
	wait := runTestDownlink(conn, src, 3999)
	for round := range 5 {
		burst := make([][]byte, 40)
		for k := range burst {
			burst[k] = []byte{byte(round), byte(k)}
		}
		src.push(burst...)
		time.Sleep(20 * time.Millisecond)
	}
	waitDownlink(t, "新会话收完 200 包", func() bool { return conn.writes.Load() == 200 })
	mu.Lock()
	for i, p := range got {
		if p[0] != byte(i/40) || p[1] != byte(i%40) {
			t.Fatalf("第 %d 包乱序：%v", i, p)
		}
	}
	mu.Unlock()
	src.close()
	wait()
	close(hold)
	for _, holder := range holders {
		holder.close()
	}
	for _, wait := range waits {
		wait()
	}
	if len(hy2DownlinkBatchSlots) != 0 {
		t.Fatalf("名额没还：%d", len(hy2DownlinkBatchSlots))
	}
}

// 每用户份额（审查第 2 条）：一个用户的会话全卡在写回，最多占 hy2DownlinkBatchPerUser
// 个名额；别的用户仍借得到批量组（一次收 32 包）。去掉份额，那个用户占满全局名额，
// 别的用户只能一次收 2 包。
func TestHy2DownlinkPerUserShare(t *testing.T) {
	hold := make(chan struct{})
	const hog = 7000
	var holders []*fakeUpstream
	var waits []func()
	for range cap(hy2DownlinkBatchSlots) + 2 {
		src := newFakeUpstream()
		holders = append(holders, src)
		src.push(make([]byte, 100), make([]byte, 100), make([]byte, 100), make([]byte, 100))
		conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn(), after: 2, hold: hold}
		waits = append(waits, runTestDownlink(conn, src, hog))
	}
	waitDownlink(t, "卡住的用户占满自己的份额", func() bool { return len(hy2DownlinkBatchSlots) == int(hy2DownlinkBatchPerUser) })
	time.Sleep(50 * time.Millisecond)
	if held := len(hy2DownlinkBatchSlots); held != int(hy2DownlinkBatchPerUser) {
		t.Fatalf("一个用户占了 %d 个名额，份额是 %d", held, hy2DownlinkBatchPerUser)
	}

	src := newFakeUpstream()
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	wait := runTestDownlink(conn, src, hog+1)
	for range 5 {
		burst := make([][]byte, 64)
		for k := range burst {
			burst[k] = make([]byte, 100)
		}
		src.push(burst...)
		time.Sleep(10 * time.Millisecond)
	}
	waitDownlink(t, "别的用户收完", func() bool { return conn.writes.Load() == 5*64 })
	if got := src.maxBatch.Load(); got != hy2UDPBatch {
		t.Fatalf("别的用户一次最多收 %d 包，期望借到批量组（%d 包）", got, hy2UDPBatch)
	}
	src.close()
	wait()
	close(hold)
	for _, holder := range holders {
		holder.close()
	}
	for _, wait := range waits {
		wait()
	}
	if len(hy2DownlinkBatchSlots) != 0 {
		t.Fatalf("名额没还：%d", len(hy2DownlinkBatchSlots))
	}
}

type panicTestConn struct {
	*memTestClientConn
	writes atomic.Int64
}

func (c *panicTestConn) WritePacket(*buf.Buffer, M.Socksaddr) error {
	if c.writes.Add(1) > 2 {
		panic("写回 panic")
	}
	return nil
}

// 写回 panic（会话由 goGuarded 兜住）时名额与份额照样归还（审查探针 3）。
func TestHy2DownlinkSlotReturnedOnPanic(t *testing.T) {
	src := newFakeUpstream()
	burst := make([][]byte, 10)
	for i := range burst {
		burst[i] = make([]byte, 10)
	}
	src.push(burst...)
	share := hy2DownlinkBatchShares.join(4000)
	defer hy2DownlinkBatchShares.leave(4000)
	func() {
		defer func() { _ = recover() }()
		var down atomic.Int64
		d := &hy2Downlink{ctx: context.Background(), conn: &panicTestConn{memTestClientConn: newMemTestClientConn()}, src: src, size: hy2UDPBatch, share: share, down: &down}
		d.run()
	}()
	if len(hy2DownlinkBatchSlots) != 0 || share.held.Load() != 0 {
		t.Fatalf("panic 后名额 %d、份额 %d 没还", len(hy2DownlinkBatchSlots), share.held.Load())
	}
}

// 中速、读得过来（审查第 3 条）：每次醒来只有 1 包时，热态持有小组阻塞读，每包
// 一次收包调用（真 socket 上是「落空 + 收到」两次 recvmmsg，与改前相同）；冷态
// 每包要「窥视 + 收」两次调用。去掉热态，这里每包约 2 次。
func TestHy2DownlinkMediumRateOneReadPerPacket(t *testing.T) {
	src := newFakeUpstream()
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	wait := runTestDownlink(conn, src, 5000)
	const packets = 1000
	start := time.Now()
	for i := range packets {
		// 每 300µs 一包（约 3300 包/秒）：忙等，不靠计时器精度。
		for time.Since(start) < time.Duration(i)*300*time.Microsecond {
		}
		src.push(make([]byte, 1200))
	}
	waitDownlink(t, "收完", func() bool { return conn.writes.Load() == packets })
	calls := src.calls.Load()
	src.close()
	wait()
	perPacket := float64(calls) / packets
	t.Logf("中速 %d 包：收包调用 %d 次，每包 %.2f 次", packets, calls, perPacket)
	if perPacket > 1.5 {
		t.Fatalf("每包 %.2f 次收包调用：中速时没进热态", perPacket)
	}
}

// 热态一段时间没包就还缓冲、回冷态：空闲会话不占缓冲。
func TestHy2DownlinkWarmCoolsDown(t *testing.T) {
	src := newFakeUpstream()
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	wait := runTestDownlink(conn, src, 5001)
	probeBefore := hy2DownlinkProbeStock.size()
	for range 50 {
		src.push(make([]byte, 100))
		time.Sleep(200 * time.Microsecond)
	}
	waitDownlink(t, "收完", func() bool { return conn.writes.Load() == 50 })
	waitDownlink(t, "热态还回小组、清掉读截止", func() bool {
		src.mu.Lock()
		cleared := src.deadline.IsZero()
		src.mu.Unlock()
		return cleared && hy2DownlinkProbeStock.size() >= probeBefore
	})
	src.close()
	wait()
}

// deadlineOnceUpstream 让第一次非阻塞收包报读截止已过（热态的 20ms 截止恰好在收
// 积压时到点）。
type deadlineOnceUpstream struct {
	*fakeUpstream
	fired atomic.Bool
}

func (u *deadlineOnceUpstream) ReadBatch(ms []ipv4.Message, flags int) (int, error) {
	if flags&hy2UDPDontWaitFlag != 0 && u.fired.CompareAndSwap(false, true) {
		return 0, os.ErrDeadlineExceeded
	}
	return u.fakeUpstream.ReadBatch(ms, flags)
}

// 读截止在收积压时到点（会话没被取消）不是会话结束：包照常收完。曾经的缺陷：
// 热态会话收积压时窗口到期，非阻塞收包报超时，整个会话被当成结束。
func TestHy2DownlinkDeadlineDuringDrainKeepsSession(t *testing.T) {
	src := &deadlineOnceUpstream{fakeUpstream: newFakeUpstream()}
	src.push(make([]byte, 10), make([]byte, 10), make([]byte, 10))
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	share := hy2DownlinkBatchShares.join(6000)
	defer hy2DownlinkBatchShares.leave(6000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var down atomic.Int64
		d := &hy2Downlink{ctx: context.Background(), conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down}
		d.run()
	}()
	waitDownlink(t, "超时之后照常收完", func() bool { return conn.writes.Load() == 3 })
	src.close()
	<-done
}

// 低速会话不常驻热态（复审 N1）：每秒 50–100 包（游戏、语音每 10ms / 19ms 一包）
// 的会话，开头或者来一串 3 包，或者先以每毫秒一包的速度来 30 包（进热态），之后
// 不该一直持着 128KB 的小组：处在热态的会话不超过 1/8；开头只是一串突发的，每会话
// 存活堆预算 32KB（空闲会话实测约 6–10KB）。存活堆扣掉存货里的缓冲：还回的组按
// 设计留在存货里到闲置回收（上限另有名额约束），这里量的是会话自己占着的。开头
// 先快的那种，快的那段里进热态是应该的，只看之后有没有回冷态。
// 改回「一串突发即进热态、段内来过一包就续期」，每会话约 200KB、全部处在热态；
// 只改回续期条件，先快后慢的会话一直处在热态。
func TestHy2DownlinkLowRateDoesNotStayWarm(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}
	const sessions = 256
	const budget = 32 << 10
	type lowRateCase struct {
		fastStart bool
		interval  time.Duration
	}
	for _, tc := range []lowRateCase{{false, 10 * time.Millisecond}, {false, 19 * time.Millisecond}, {true, 10 * time.Millisecond}, {true, 19 * time.Millisecond}} {
		interval := tc.interval
		name := "burst-then-" + interval.String()
		if tc.fastStart {
			name = "fast-then-" + interval.String()
		}
		t.Run(name, func(t *testing.T) {
			before := heapAfterGC() - downlinkStockBytes()
			srcs := make([]*fakeUpstream, sessions)
			conns := make([]*downlinkTestConn, sessions)
			var waits []func()
			for i := range srcs {
				srcs[i] = newFakeUpstream()
				conns[i] = &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
				waits = append(waits, runTestDownlink(conns[i], srcs[i], int64(41000+i)))
			}
			stop := make(chan struct{})
			var feeders sync.WaitGroup
			for i, src := range srcs {
				feeders.Add(1)
				go func() {
					defer feeders.Done()
					if tc.fastStart {
						for range 30 {
							src.push(make([]byte, 50))
							time.Sleep(time.Millisecond)
						}
					} else {
						src.push(make([]byte, 50), make([]byte, 50), make([]byte, 50))
					}
					// 错开各会话的相位。
					time.Sleep(time.Duration(i%10) * time.Millisecond)
					ticker := time.NewTicker(interval)
					defer ticker.Stop()
					for {
						select {
						case <-stop:
							return
						case <-ticker.C:
							src.push(make([]byte, 50))
						}
					}
				}()
			}
			var peak uint64
			warm := 0
			for range 6 {
				time.Sleep(250 * time.Millisecond)
				peak = max(peak, heapAfterGC()-downlinkStockBytes())
				now := 0
				for _, src := range srcs {
					src.mu.Lock()
					if !src.deadline.IsZero() {
						now++
					}
					src.mu.Unlock()
				}
				warm = max(warm, now)
			}
			close(stop)
			feeders.Wait()
			var writes int64
			for i, src := range srcs {
				src.close()
				waits[i]()
				writes += conns[i].writes.Load()
			}
			perSession := (int64(peak) - int64(before)) / sessions
			t.Logf("%s：%d 会话写回 %d 包，峰值存活堆每会话 %d 字节，同时处在热态最多 %d 个", name, sessions, writes, perSession, warm)
			if writes < sessions*int64(time.Second/interval) {
				t.Fatalf("写回太少：%d", writes)
			}
			if !tc.fastStart && perSession > budget {
				t.Fatalf("每会话常驻 %d 字节，超过预算 %d：低速会话占着热态的小组", perSession, budget)
			}
			if warm > sessions/8 {
				t.Fatalf("同时处在热态 %d 个会话（共 %d）：低速会话没回冷态", warm, sessions)
			}
		})
	}
}

// downlinkStockBytes 是下行存货里缓冲的字节数（批量组 2MB、小组 128KB）。
func downlinkStockBytes() uint64 {
	return uint64(hy2DownlinkBatchStock.size())*hy2UDPBatch*hy2UDPMaxDatagram +
		uint64(hy2DownlinkProbeStock.size())*hy2DownlinkProbeBatch*hy2UDPMaxDatagram
}

// cancelOnClearUpstream 在热态回冷态清读截止的那一刻，模拟转发收尾的「cancel +
// AfterFunc 把截止设为现在」恰好抢在清截止之前（复审 N2，复审员的确定性探针）。
type cancelOnClearUpstream struct {
	*fakeUpstream
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (u *cancelOnClearUpstream) SetReadDeadline(t time.Time) error {
	if t.IsZero() && u.fired.CompareAndSwap(false, true) {
		u.cancel()
		_ = u.fakeUpstream.SetReadDeadline(time.Now())
	}
	return u.fakeUpstream.SetReadDeadline(t)
}

// 清读截止与收尾撞车：清截止会盖掉 AfterFunc 刚设的截止，只能靠清完之后复查
// ctx 结束；不复查，下行卡在没有截止的窥视上，转发永不收尾。
func TestHy2DownlinkClearDeadlineRacesCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelOnClearUpstream{fakeUpstream: newFakeUpstream(), cancel: cancel}
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	share := hy2DownlinkBatchShares.join(7000)
	defer hy2DownlinkBatchShares.leave(7000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var down atomic.Int64
		d := &hy2Downlink{ctx: ctx, conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down}
		d.run()
	}()
	// 包来得密进热态，停下后等窗口到期回冷态、清截止。
	for range 20 {
		src.push(make([]byte, 10))
		time.Sleep(200 * time.Microsecond)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("清截止与收尾撞车后下行没有结束（卡在无截止的窥视上）")
	}
	if !src.fired.Load() {
		t.Fatal("没走到回冷态")
	}
}

// 热态名额：同时处在热态的会话每用户不超过份额、全进程不超过名额；占不到的照常
// 在冷态收完。去掉份额或名额，下面对应的子用例变红。
func TestHy2DownlinkWarmSlotsCapped(t *testing.T) {
	feed := func(srcs []*fakeUpstream, stop chan struct{}, wg *sync.WaitGroup) {
		for _, src := range srcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
						src.push(make([]byte, 100))
					}
				}
			}()
		}
	}
	warmOf := func(srcs []*fakeUpstream) int {
		n := 0
		for _, src := range srcs {
			src.mu.Lock()
			if !src.deadline.IsZero() {
				n++
			}
			src.mu.Unlock()
		}
		return n
	}
	run := func(t *testing.T, users []int64, perUser int32, slots int) map[int64]int {
		savedPerUser, savedSlots := hy2DownlinkWarmPerUser, hy2DownlinkWarmSlots
		hy2DownlinkWarmPerUser, hy2DownlinkWarmSlots = perUser, make(chan struct{}, slots)
		defer func() { hy2DownlinkWarmPerUser, hy2DownlinkWarmSlots = savedPerUser, savedSlots }()
		byUser := map[int64][]*fakeUpstream{}
		var all []*fakeUpstream
		var conns []*downlinkTestConn
		var waits []func()
		for _, user := range users {
			src := newFakeUpstream()
			conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
			byUser[user] = append(byUser[user], src)
			all = append(all, src)
			conns = append(conns, conn)
			waits = append(waits, runTestDownlink(conn, src, user))
		}
		stop := make(chan struct{})
		var feeders sync.WaitGroup
		feed(all, stop, &feeders)
		peak := map[int64]int{}
		total := 0
		for range 40 {
			time.Sleep(10 * time.Millisecond)
			now := 0
			for user, srcs := range byUser {
				n := warmOf(srcs)
				peak[user] = max(peak[user], n)
				now += n
			}
			total = max(total, now)
		}
		close(stop)
		feeders.Wait()
		for i, src := range all {
			src.close()
			waits[i]()
			if conns[i].writes.Load() == 0 {
				t.Errorf("第 %d 个会话一包没收到", i)
			}
		}
		if len(hy2DownlinkWarmSlots) != 0 {
			t.Fatalf("热态名额没还：%d", len(hy2DownlinkWarmSlots))
		}
		peak[-1] = total
		return peak
	}
	t.Run("per-user", func(t *testing.T) {
		users := []int64{8100, 8100, 8100, 8100, 8100, 8100, 8101}
		peak := run(t, users, 2, 64)
		t.Logf("每用户份额 2：用户甲同时热态最多 %d 个，用户乙 %d 个", peak[8100], peak[8101])
		if peak[8100] > 2 {
			t.Fatalf("一个用户同时 %d 个会话在热态，份额是 2", peak[8100])
		}
		if peak[8100] == 0 || peak[8101] == 0 {
			t.Fatalf("每毫秒一包的会话应进热态：甲 %d 乙 %d", peak[8100], peak[8101])
		}
	})
	t.Run("global", func(t *testing.T) {
		users := []int64{8200, 8201, 8202, 8203, 8204, 8205}
		peak := run(t, users, 64, 3)
		t.Logf("全局名额 3：同时热态最多 %d 个", peak[-1])
		if peak[-1] > 3 || peak[-1] == 0 {
			t.Fatalf("同时 %d 个会话在热态，全局名额是 3", peak[-1])
		}
	})
}
