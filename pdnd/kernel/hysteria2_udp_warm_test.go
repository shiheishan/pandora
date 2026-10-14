//go:build unix

package kernel

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/internal/udprecv"
)

// 中速、读得过来（审查第 3 条）：每次醒来只有 1 包时，每包的收包系统调用是「落空 +
// 收到」两次（替身按系统调用计），与持有缓冲阻塞读相同；会话进热态（持小组、零
// 拷贝）。冷态每包同样两次（复审 review-r6 第 1 条之后），真 socket 上的同一上限见
// TestHy2DownlinkRecvSyscallsPerPacket。
func TestHy2DownlinkMediumRateTwoRecvsPerPacket(t *testing.T) {
	src := newFakeUpstream()
	conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	wait := runTestDownlink(conn, src, 5000)
	// 3000 包（约 0.9 秒）：进热态要先在冷态满一段 100ms，量的是稳态。
	const packets = 3000
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
	t.Logf("中速 %d 包：收包系统调用 %d 次，每包 %.3f 次；设热态读截止 %d 次", packets, calls, perPacket, src.armed.Load())
	if perPacket > 2.05 {
		t.Fatalf("每包 %.3f 次收包系统调用，超过阻塞读的 2 次", perPacket)
	}
	if src.armed.Load() == 0 {
		t.Fatal("中速会话没进热态")
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

// deadlineOnceUpstream 让第一次非阻塞收包报读截止已过（热态的读截止恰好在收积压
// 时到点；真 socket 上非阻塞收经 RawConn.Control、不看读截止，这里守的是收到这种
// 错误时的处理）。
type deadlineOnceUpstream struct {
	*fakeUpstream
	fired atomic.Bool
}

func (u *deadlineOnceUpstream) Recv(b *udprecv.Batch, size int, wait bool) (int, error) {
	if !wait && u.fired.CompareAndSwap(false, true) {
		u.calls.Add(1)
		return 0, os.ErrDeadlineExceeded
	}
	return u.fakeUpstream.Recv(b, size, wait)
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
		d := &hy2Downlink{ctx: context.Background(), conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down, warmLimit: hy2DownlinkWarm}
		d.run()
	}()
	waitDownlink(t, "超时之后照常收完", func() bool { return conn.writes.Load() == 3 })
	src.close()
	<-done
}

// 低速会话不常驻热态（复审 N1）：每秒 50–100 包（游戏、语音每 10ms / 19ms 一包）
// 的会话，开头或者来一串 3 包，或者先以每毫秒一包的速度来 150 包（超过一段，进
// 热态，用例核对确实进过），之后
// 不该一直持着 128KB 的小组：处在热态的会话不超过 1/8；开头只是一串突发的，每会话
// 存活堆预算 32KB（空闲会话实测约 6–10KB）。存活堆扣掉存货里的缓冲：还回的组按
// 设计留在存货里到闲置回收（上限另有名额约束），这里量的是会话自己占着的。开头
// 先快的那种，快的那段里进热态是应该的，等快的那段结束、再过三段之后才采样。
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
			var feeders, fastDone sync.WaitGroup
			for i, src := range srcs {
				feeders.Add(1)
				fastDone.Add(1)
				go func() {
					defer feeders.Done()
					if tc.fastStart {
						for range 150 {
							src.push(make([]byte, 50))
							time.Sleep(time.Millisecond)
						}
						fastDone.Done()
					} else {
						fastDone.Done()
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
			if tc.fastStart {
				fastDone.Wait()
				time.Sleep(3 * hy2DownlinkWarmIdle)
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
			if tc.fastStart {
				entered := 0
				for _, src := range srcs {
					if src.armed.Load() > 0 {
						entered++
					}
				}
				if entered < sessions/2 {
					t.Fatalf("先快的会话只有 %d/%d 个进过热态，用例没测到回冷态", entered, sessions)
				}
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
// ctx 结束；不复查，下行卡在没有截止的空闲等待上，转发永不收尾。
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
		d := &hy2Downlink{ctx: ctx, conn: conn, src: src, size: hy2UDPBatch, share: share, down: &down, warmLimit: hy2DownlinkWarm}
		d.run()
	}()
	// 包来得密进热态，停下后等窗口到期回冷态、清截止。
	feedUntilWarm(t, src.fakeUpstream)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("清截止与收尾撞车后下行没有结束（卡在无截止的空闲等待上）")
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
		// 注入一份自己的名额，不动全进程那份（复审 P2）。
		limit := &hy2WarmLimit{slots: make(chan struct{}, slots), perUser: perUser}
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
			waits = append(waits, runTestDownlinkWarm(conn, src, user, limit))
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
		if limit.inUse() != 0 {
			t.Fatalf("热态名额没还：%d", limit.inUse())
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

// 成对到达的低速会话不进热态（复审 P1，审查员的成对到达探针）：256 个会话每 15ms
// 来一对、对内相隔 0.5ms（约每秒 133 包，游戏、语音常见），进热态按一段（100ms）的
// 平均速率判，这类会话一段只有两三包，热态名额的平均占用应接近 0；同时一个每毫秒一包的
// 会话（另一个用户）照常处在热态。改回「两次醒来相隔不到 2ms 就进」，每一对都进
// 一段热态，名额平均被占几十个、最高占满。
func TestHy2DownlinkPairedLowRateStaysCold(t *testing.T) {
	const sessions = 256
	limit := newHy2WarmLimit(64)
	var srcs []*fakeUpstream
	var waits []func()
	for i := range sessions {
		src := newFakeUpstream()
		srcs = append(srcs, src)
		waits = append(waits, runTestDownlinkWarm(&downlinkTestConn{memTestClientConn: newMemTestClientConn()}, src, int64(60000+i%8), limit))
	}
	stop := make(chan struct{})
	var feeders sync.WaitGroup
	for i, src := range srcs {
		feeders.Add(1)
		go func() {
			defer feeders.Done()
			time.Sleep(time.Duration(i%15) * time.Millisecond)
			ticker := time.NewTicker(15 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					src.push(make([]byte, 60))
					time.Sleep(500 * time.Microsecond)
					src.push(make([]byte, 60))
				}
			}
		}()
	}
	fast := newFakeUpstream()
	fastConn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	fastWait := runTestDownlinkWarm(fastConn, fast, 69999, limit)
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
				fast.push(make([]byte, 60))
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	calls0, writes0 := fast.calls.Load(), fastConn.writes.Load()
	const samples = 100
	occupied, peak, fastWarm := 0, 0, 0
	for range samples {
		time.Sleep(10 * time.Millisecond)
		n := limit.inUse()
		occupied += n
		peak = max(peak, n)
		fast.mu.Lock()
		if !fast.deadline.IsZero() {
			fastWarm++
		}
		fast.mu.Unlock()
	}
	calls, writes := fast.calls.Load()-calls0, fastConn.writes.Load()-writes0
	close(stop)
	feeders.Wait()
	for i, src := range srcs {
		src.close()
		waits[i]()
	}
	fast.close()
	fastWait()
	average := float64(occupied) / samples
	t.Logf("成对到达 %d 会话：热态名额平均占用 %.2f、最高 %d（名额 %d）；每毫秒一包的会话 %d/%d 次采样在热态，每包收包调用 %.2f",
		sessions, average, peak, cap(limit.slots), fastWarm, samples, float64(calls)/float64(writes))
	if average > 3 {
		t.Fatalf("成对到达的低速会话平均占着 %.2f 个热态名额：进入门槛没按包数算", average)
	}
	if fastWarm < samples*7/10 {
		t.Fatalf("每毫秒一包的会话只有 %d/%d 次采样在热态", fastWarm, samples)
	}
	if limit.inUse() != 0 {
		t.Fatalf("热态名额没还：%d", limit.inUse())
	}
}

// 冷态计包段按段龄折算速率：一串比一段的门槛多两成的包之后静默五段再来一包，不该凭
// 那串旧包进热态；每毫秒一包持续一段以上则应进。去掉「按段龄折算」（只看段内包数），
// 前一种也进。
func TestHy2DownlinkStaleColdCountDoesNotWarm(t *testing.T) {
	t.Run("stale-burst", func(t *testing.T) {
		src := newFakeUpstream()
		conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
		wait := runTestDownlinkWarm(conn, src, 61000, newHy2WarmLimit(8))
		burst := make([][]byte, hy2DownlinkWarmMinPackets*6/5)
		for i := range burst {
			burst[i] = make([]byte, 10)
		}
		src.push(burst...)
		waitDownlink(t, "收完那一串", func() bool { return conn.writes.Load() == int64(len(burst)) })
		time.Sleep(5 * hy2DownlinkWarmIdle)
		src.push(make([]byte, 10))
		waitDownlink(t, "收到后来的一包", func() bool { return conn.writes.Load() == int64(len(burst))+1 })
		src.close()
		wait()
		if src.armed.Load() > 0 {
			t.Fatalf("一串 %d 包、静默 %v 后再来一包：进了热态", len(burst), 5*hy2DownlinkWarmIdle)
		}
	})
	t.Run("sustained", func(t *testing.T) {
		src := newFakeUpstream()
		conn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
		wait := runTestDownlinkWarm(conn, src, 61001, newHy2WarmLimit(8))
		pushed := feedUntilWarm(t, src)
		waitDownlink(t, "收完", func() bool { return conn.writes.Load() == pushed })
		src.close()
		wait()
		t.Logf("每毫秒一包，第 %d 包后进热态", pushed)
	})
}
