//go:build unix

package kernel

import (
	"context"
	"errors"
	"fmt"
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
	// armed 是设过非零读截止的次数（即进过热态）。
	armed atomic.Int64
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
	if !t.IsZero() {
		u.armed.Add(1)
	}
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
	// blockAt 非 0 时写回第 blockAt+1 包起卡住（运行中设定，配合 hold）。
	blockAt atomic.Int64
	every   int64
	delay   time.Duration
	check   func([]byte) bool
	bad     atomic.Int64
}

func (c *downlinkTestConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	n := c.writes.Add(1)
	if c.check != nil {
		runtime.Gosched()
		if !c.check(buffer.Bytes()) {
			c.bad.Add(1)
		}
	}
	if c.hold != nil && (n > c.after || (c.blockAt.Load() > 0 && n > c.blockAt.Load())) {
		<-c.hold
	}
	if c.every > 0 && n%c.every == 0 {
		time.Sleep(c.delay)
	}
	return nil
}

// runTestDownlink 起一个会话的下行（批量 32 包、全进程热态名额），返回等它结束的函数。
func runTestDownlink(conn *downlinkTestConn, src *fakeUpstream, userID int64) func() {
	return runTestDownlinkWarm(conn, src, userID, hy2DownlinkWarm)
}

// runTestDownlinkWarm 同上，热态名额由测试注入。
func runTestDownlinkWarm(conn *downlinkTestConn, src *fakeUpstream, userID int64, warm *hy2WarmLimit) func() {
	share := hy2DownlinkBatchShares.join(userID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer hy2DownlinkBatchShares.leave(userID)
		var down atomic.Int64
		d := &hy2Downlink{ctx: context.Background(), conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down, warmLimit: warm}
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

// holderWarm 是卡住会话用的热态名额（足够大，不让热态名额成为变量）。
var holderWarm = newHy2WarmLimit(4096)

// startWarmHolder 起一个占着批量组卡在写回的会话：每毫秒一包直到进热态，再来 6 包；
// 热态小组收满 2 包后借批量组收剩下 4 包，写回其中第一包时卡住，占着批量组直到 hold
// 关闭。冷态复制写回、不占收包组，只有热态会这样占着。
func startWarmHolder(t *testing.T, user int64, hold chan struct{}) (*fakeUpstream, func()) {
	t.Helper()
	src := newFakeUpstream()
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn(), after: 1 << 62, hold: hold}
	wait := runTestDownlinkWarm(conn, src, user, holderWarm)
	pushed := feedUntilWarm(t, src)
	waitDownlink(t, "写回进热态之前的包", func() bool { return conn.writes.Load() == pushed })
	conn.blockAt.Store(pushed + 2)
	burst := make([][]byte, 6)
	for i := range burst {
		burst[i] = make([]byte, 100)
	}
	src.push(burst...)
	return src, wait
}

// feedUntilWarm 每毫秒给会话一包，直到它进了热态（设过读截止），返回推了多少包。
// 进热态要一段 20ms 里平均每秒 500 包以上，所以至少推满一段。
func feedUntilWarm(t *testing.T, src *fakeUpstream) int64 {
	t.Helper()
	var pushed int64
	deadline := time.Now().Add(5 * time.Second)
	for src.armed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("每毫秒一包推了 5 秒还没进热态")
		}
		src.push(make([]byte, 100))
		pushed++
		time.Sleep(time.Millisecond)
	}
	return pushed
}

// 名额占满（审查探针 2）：许多用户各有会话卡在写回、占满全局名额后，新会话照常
// 完整、按序转发（用小组收）；全部收尾后名额归零。
func TestHy2DownlinkSlotsExhaustedOthersStillForward(t *testing.T) {
	slots := cap(hy2DownlinkBatchSlots)
	hold := make(chan struct{})
	var holders []*fakeUpstream
	var waits []func()
	for i := range slots + 3 {
		// 每个用户一个会话：各自的份额都够，占满的是全局名额。
		src, wait := startWarmHolder(t, int64(3000+i), hold)
		holders = append(holders, src)
		waits = append(waits, wait)
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
		src, wait := startWarmHolder(t, hog, hold)
		holders = append(holders, src)
		waits = append(waits, wait)
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
	// panicAt 非 0 时写回第 panicAt+1 包起 panic。
	panicAt atomic.Int64
}

func (c *panicTestConn) WritePacket(*buf.Buffer, M.Socksaddr) error {
	n := c.writes.Add(1)
	if at := c.panicAt.Load(); at > 0 && n > at {
		panic("写回 panic")
	}
	return nil
}

// 写回 panic（会话由 goGuarded 兜住）时名额、份额与热态名额照样归还（审查探针 3）：
// 每毫秒一包直到进热态，再来 6 包，借批量组收的那批写回时 panic。
func TestHy2DownlinkSlotReturnedOnPanic(t *testing.T) {
	src := newFakeUpstream()
	conn := &panicTestConn{memTestClientConn: newMemTestClientConn()}
	share := hy2DownlinkBatchShares.join(4000)
	defer hy2DownlinkBatchShares.leave(4000)
	warm := newHy2WarmLimit(8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		var down atomic.Int64
		d := &hy2Downlink{ctx: context.Background(), conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down, warmLimit: warm}
		d.run()
	}()
	pushed := feedUntilWarm(t, src)
	waitDownlink(t, "写回进热态之前的包", func() bool { return conn.writes.Load() == pushed })
	conn.panicAt.Store(pushed + 2)
	burst := make([][]byte, 6)
	for i := range burst {
		burst[i] = make([]byte, 10)
	}
	src.push(burst...)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("写回 panic 之后下行没有结束")
	}
	if conn.writes.Load() <= pushed+2 {
		t.Fatalf("没走到热态批量组写回（写回 %d 次）", conn.writes.Load())
	}
	if len(hy2DownlinkBatchSlots) != 0 || share.held.Load() != 0 || warm.inUse() != 0 || share.warm.Load() != 0 {
		t.Fatalf("panic 后批量名额 %d、份额 %d、热态名额 %d、热态份额 %d 没还", len(hy2DownlinkBatchSlots), share.held.Load(), warm.inUse(), share.warm.Load())
	}
}

// 冷态里的一小串（3 包）只用小组收，不借批量组；积压大（4 包以上，小组连收两次都满）
// 才借。改回「小组一次收满就借」，3 包也借批量组（一次收 32 包）。
func TestHy2DownlinkSmallBurstStaysOnProbeGroup(t *testing.T) {
	for _, tc := range []struct {
		packets int
		batch   bool
	}{{3, false}, {4, true}, {8, true}} {
		t.Run(fmt.Sprint(tc.packets), func(t *testing.T) {
			src := newFakeUpstream()
			conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
			wait := runTestDownlinkWarm(conn, src, 62000, newHy2WarmLimit(8))
			burst := make([][]byte, tc.packets)
			for i := range burst {
				burst[i] = make([]byte, 10)
			}
			src.push(burst...)
			waitDownlink(t, "收完那一串", func() bool { return conn.writes.Load() == int64(tc.packets) })
			src.close()
			wait()
			if borrowed := src.maxBatch.Load() == hy2UDPBatch; borrowed != tc.batch {
				t.Fatalf("一串 %d 包：借了批量组=%v，期望 %v（一次最多收 %d 包）", tc.packets, borrowed, tc.batch, src.maxBatch.Load())
			}
		})
	}
}
