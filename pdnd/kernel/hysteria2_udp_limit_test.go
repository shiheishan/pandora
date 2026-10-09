//go:build unix

package kernel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 第 5 轮复审（review-r4）L1–L3 的守卫。

// 卡在写回的冷态会话的常驻上限（L1）：上游连发大包（TUIC 单包可到 0xffff，hy2 上限
// 4096），写回卡住。冷态每次复制按已复制的容量封顶 128KB，超出的那几包留在批量组里
// 零拷贝写回（批量组占着名额，全局受名额约束），所以：
//   - 每个卡住会话复制出的缓冲 ≤ 128KB（hy2：32×4096 恰好 128KB，不会超出）；
//   - 另有至多「批量名额」个会话各占着一个 2MB 批量组。
//
// 改前复制完就还批量组，名额一还，下一个会话又能借来复制 32 个大包：每个卡住的
// 会话各占 32×64KB=2MB 复制缓冲，不受名额约束。
func TestHy2DownlinkStuckLargePacketsBounded(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}
	const sessions = 128
	const perSessionBudget = 128<<10 + 32<<10 // 复制上限 128KB，另给会话本身的结构留 32KB
	for _, tc := range []struct {
		name string
		size int
	}{{"tuic-33000", 33000}, {"tuic-60000", 60000}, {"hy2-4096", 4096}} {
		t.Run(tc.name, func(t *testing.T) {
			payload := make([]byte, tc.size)
			hold := make(chan struct{})
			before := heapAfterGC() - downlinkStockBytes()
			srcs := make([]*fakeUpstream, sessions)
			conns := make([]*downlinkTestConn, sessions)
			var waits []func()
			for i := range srcs {
				srcs[i] = newFakeUpstream()
				burst := make([][]byte, 36)
				for k := range burst {
					burst[k] = payload
				}
				srcs[i].push(burst...)
				// 前两轮小组各 2 包照常写回，第三轮（批量组）写回第 5 包时卡住。
				conns[i] = &downlinkTestConn{memTestClientConn: newMemTestClientConn(), after: 4, hold: hold}
				waits = append(waits, runTestDownlinkWarm(conns[i], srcs[i], int64(63000+i), newHy2WarmLimit(1)))
			}
			waitDownlink(t, "全部卡在写回", func() bool {
				for _, conn := range conns {
					if conn.writes.Load() < 5 {
						return false
					}
				}
				return true
			})
			heldBatch := len(hy2DownlinkBatchSlots)
			after := heapAfterGC() - downlinkStockBytes()
			close(hold)
			for i, src := range srcs {
				src.close()
				waits[i]()
			}
			delta := int64(after) - int64(before) - int64(heldBatch)*hy2UDPBatch*hy2UDPMaxDatagram
			perSession := delta / sessions
			t.Logf("%s：%d 个会话卡在写回，扣掉占着名额的 %d 个批量组后每会话 %d 字节（上限 %d）", tc.name, sessions, heldBatch, perSession, perSessionBudget)
			if perSession > perSessionBudget {
				t.Fatalf("每个卡住的会话常驻 %d 字节，超过 %d：复制出的大包不受名额约束", perSession, perSessionBudget)
			}
			if len(hy2DownlinkBatchSlots) != 0 {
				t.Fatalf("批量名额没还：%d", len(hy2DownlinkBatchSlots))
			}
		})
	}
}

// 成串到达的中低速流量不进热态（L2，审查员的成串到达探针）：256 个会话每 33ms 来一串
// 12 包、串内相隔约 250µs（约每秒 360 包，视频帧、游戏快照常见）。进热态按整段判：
// 段龄满 20ms 时按「段内包数 ≥ 段龄 / 2ms」（每秒 500 包）才进，这类会话一串 12 包摊在
// 33ms 上不够。热态名额平均占用不超过 3（另有一个每毫秒一包的会话应在热态，占 1 个）。
// race 下会话数减半。
// 改回「段起点起收够 10 包就进」（半段判），每一串都进一段热态，名额平均被占约 63/64。
func TestHy2DownlinkPacedBurstStaysCold(t *testing.T) {
	sessions := 256
	if raceEnabled {
		// race 下 256 个发包 goroutine 在小机器上会被拖慢，减半免得测的是调度。
		sessions = 128
	}
	limit := newHy2WarmLimit(64)
	var srcs []*fakeUpstream
	var waits []func()
	for i := range sessions {
		src := newFakeUpstream()
		srcs = append(srcs, src)
		waits = append(waits, runTestDownlinkWarm(&downlinkTestConn{memTestClientConn: newMemTestClientConn()}, src, int64(64000+i%8), limit))
	}
	stop := make(chan struct{})
	var feeders sync.WaitGroup
	start := time.Now()
	for i, src := range srcs {
		feeders.Add(1)
		go func() {
			defer feeders.Done()
			// 按绝对时刻排串：机器忙、某一串晚了 5ms 以上就跳过，不补发，免得两串挤在
			// 一起（那就真成了高速流量），测的是串的节奏而不是调度延迟。
			next := start.Add(time.Duration(i%33) * time.Millisecond)
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Until(next)):
				}
				if time.Since(next) <= 5*time.Millisecond {
					for k := range 12 {
						if k > 0 {
							time.Sleep(250 * time.Microsecond)
						}
						src.push(make([]byte, 60))
					}
				}
				next = next.Add(33 * time.Millisecond)
				for time.Until(next) < 0 {
					next = next.Add(33 * time.Millisecond)
				}
			}
		}()
	}
	fast := newFakeUpstream()
	fastConn := &downlinkTestConn{memTestClientConn: newMemTestClientConn()}
	fastWait := runTestDownlinkWarm(fastConn, fast, 64999, limit)
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
	close(stop)
	feeders.Wait()
	for i, src := range srcs {
		src.close()
		waits[i]()
	}
	fast.close()
	fastWait()
	average := float64(occupied) / samples
	t.Logf("成串到达 %d 会话：热态名额平均占用 %.2f、最高 %d（名额 %d）；每毫秒一包的会话 %d/%d 次采样在热态", sessions, average, peak, cap(limit.slots), fastWarm, samples)
	if average > 3 {
		t.Fatalf("成串到达的中低速会话平均占着 %.2f 个热态名额：进入没按整段判", average)
	}
	if fastWarm < samples*7/10 {
		t.Fatalf("每毫秒一包的会话只有 %d/%d 次采样在热态", fastWarm, samples)
	}
}

// 生产入口用全进程共享的热态名额（L3）：经 hy2DownlinkUDP 真入口跑一个每毫秒一包的
// 会话，进热态期间 hy2DownlinkWarm.inUse() > 0。生产调用处改成每会话一份名额，这里
// 永远看不到共享名额被占，变红。
func TestHy2DownlinkUDPUsesSharedWarmLimit(t *testing.T) {
	if !hy2UDPPeekSupported {
		t.Skip("非 unix 没有窥视收包")
	}
	upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	u := newHy2UDPUpstream(upstream)
	conn := newMemTestClientConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	share := hy2DownlinkBatchShares.join(65000)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer hy2DownlinkBatchShares.leave(65000)
		var down atomic.Int64
		hy2DownlinkUDP(ctx, conn, u, share, &down)
	}()
	if used := hy2DownlinkWarm.inUse(); used != 0 {
		t.Fatalf("开始前共享热态名额已被占 %d 个（别的用例没收尾）", used)
	}
	stop := make(chan struct{})
	var feeder sync.WaitGroup
	feeder.Add(1)
	go func() {
		defer feeder.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = sender.WriteTo(make([]byte, 100), upstream.LocalAddr())
			}
		}
	}()
	seen := false
	deadline := time.Now().Add(3 * time.Second)
	for !seen && time.Now().Before(deadline) {
		seen = hy2DownlinkWarm.inUse() > 0
		time.Sleep(time.Millisecond)
	}
	close(stop)
	feeder.Wait()
	cancel()
	_ = upstream.SetDeadline(time.Now())
	<-done
	if !seen {
		t.Fatal(fmt.Sprintf("每毫秒一包的会话经生产入口跑了 3 秒，共享热态名额一次都没被占：生产路径没用 hy2DownlinkWarm"))
	}
	if used := hy2DownlinkWarm.inUse(); used != 0 {
		t.Fatalf("会话结束后共享热态名额还占着 %d 个", used)
	}
}

// trim 的语义：只回收 before 之前就没再被借过的存货，之后还回的保留，次序不变。
func TestHy2StockTrimKeepsRecent(t *testing.T) {
	type item struct{ id int }
	s := &hy2Stock[item]{alloc: func() *item { return &item{} }}
	for i := range 5 {
		s.items = append(s.items, hy2Stocked[item]{item: &item{id: i}, used: int64(10 * (i + 1))})
	}
	s.trim(30) // used 10、20 早于 30，回收；30、40、50 保留
	if got := s.size(); got != 3 {
		t.Fatalf("回收后剩 %d 件，期望 3", got)
	}
	for i, want := range []int{2, 3, 4} {
		if s.items[i].item.id != want {
			t.Fatalf("第 %d 件是 %d，期望 %d", i, s.items[i].item.id, want)
		}
	}
	s.trim(0)
	if s.size() != 3 {
		t.Fatal("before 早于全部存货时不该回收")
	}
	s.trim(100)
	if s.size() != 0 {
		t.Fatalf("before 晚于全部存货时应全部回收，剩 %d", s.size())
	}
	if got := s.get(); got == nil || s.allocs.Load() != 1 {
		t.Fatal("存货空时 get 应新分配一件")
	}
}

// 各存货表按自己的回收时长回收：闲置 10 秒的下行小组（5 秒）被回收，上行凑批缓冲
// （30 秒）保留。上行改成和下行一样 5 秒，这里变红（上行周期性重新分配，见
// hy2UplinkStockIdle 的探针数据）。
func TestHy2StockJanitorPerStockIdle(t *testing.T) {
	now := hy2StockNow()
	probe := &hy2DownlinkGroup{}
	uplink := &hy2UplinkBatch{}
	hy2DownlinkProbeStock.mu.Lock()
	hy2DownlinkProbeStock.items = append([]hy2Stocked[hy2DownlinkGroup]{{item: probe, used: now - int64(10*time.Second)}}, hy2DownlinkProbeStock.items...)
	hy2DownlinkProbeStock.mu.Unlock()
	hy2UplinkBatchStock.mu.Lock()
	hy2UplinkBatchStock.items = append([]hy2Stocked[hy2UplinkBatch]{{item: uplink, used: now - int64(10*time.Second)}}, hy2UplinkBatchStock.items...)
	hy2UplinkBatchStock.mu.Unlock()
	hy2StockJanitor.run(now)
	contains := func(items int, at func(int) bool) bool {
		for i := range items {
			if at(i) {
				return true
			}
		}
		return false
	}
	hy2DownlinkProbeStock.mu.Lock()
	probeKept := contains(len(hy2DownlinkProbeStock.items), func(i int) bool { return hy2DownlinkProbeStock.items[i].item == probe })
	hy2DownlinkProbeStock.mu.Unlock()
	hy2UplinkBatchStock.mu.Lock()
	uplinkKept := contains(len(hy2UplinkBatchStock.items), func(i int) bool { return hy2UplinkBatchStock.items[i].item == uplink })
	hy2UplinkBatchStock.mu.Unlock()
	hy2UplinkBatchStock.trim(now) // 收尾：去掉测试放进去的那件
	if probeKept {
		t.Fatal("闲置 10 秒的下行小组没被回收（下行 5 秒）")
	}
	if !uplinkKept {
		t.Fatal("闲置 10 秒的上行凑批缓冲被回收了（上行 30 秒）")
	}
}
