package kernel

import (
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// replayFilter 是按时间分代的「见过即拒」集合，给 VMess 的 authID 防重放用
// （authID 自带 ±120 秒时间戳，记几分钟就够）。旧版 Shadowsocks 没有时间戳，
// 用按条数保留的 saltBloom（replay_filter_bloom.go）。
//
// 每 period 开一代新的，只保留最近 keep 代，查重看全部保留代。一个键从写入
// 起至少保留 (keep-1)*period、至多 keep*period。清理是整代丢弃，O(1)，不用像
// 以前那样每来一个连接就在全局锁下把整张表扫一遍。
//
// 内存有上限：当前代满 maxPerGen 条时提前换代（最老一代随之丢弃）。这会在
// 洪泛下缩短保留期，但不会让内存无限增长。
type replayFilter struct {
	mu        sync.Mutex
	period    time.Duration
	maxPerGen int
	gens      []map[string]struct{} // gens[0] 是当前代
	started   time.Time             // 当前代开始的时间
}

// replayFilterMaxPerGen 是 replayFilter 每代的条数上限。
const replayFilterMaxPerGen = 1 << 16

func newReplayFilter(period time.Duration, keep, maxPerGen int) *replayFilter {
	if keep < 2 {
		keep = 2
	}
	gens := make([]map[string]struct{}, keep)
	for i := range gens {
		gens[i] = make(map[string]struct{})
	}
	return &replayFilter{period: period, maxPerGen: maxPerGen, gens: gens}
}

// check 报告 key 是否没见过；没见过就记下并返回 true，见过返回 false。
func (f *replayFilter) check(key []byte, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotateLocked(now)
	for _, gen := range f.gens {
		if _, seen := gen[string(key)]; seen {
			return false
		}
	}
	if len(f.gens[0]) >= f.maxPerGen {
		f.shiftLocked()
		f.started = now
	}
	f.gens[0][string(key)] = struct{}{}
	return true
}

func (f *replayFilter) rotateLocked(now time.Time) {
	if f.started.IsZero() {
		f.started = now
		return
	}
	elapsed := now.Sub(f.started)
	if elapsed < f.period {
		return
	}
	steps := int(elapsed / f.period)
	if steps > len(f.gens) {
		steps = len(f.gens)
	}
	for i := 0; i < steps; i++ {
		f.shiftLocked()
	}
	f.started = f.started.Add(time.Duration(int(elapsed/f.period)) * f.period)
}

// shiftLocked 丢掉最老一代，开一代空的作为当前代。
func (f *replayFilter) shiftLocked() {
	last := len(f.gens) - 1
	copy(f.gens[1:], f.gens[:last])
	f.gens[0] = make(map[string]struct{})
}

// size 返回所有保留代里的条目数（测试与诊断用）。
func (f *replayFilter) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, gen := range f.gens {
		total += len(gen)
	}
	return total
}

// drainUntilPeerClose 在认证失败（含 salt / authID 重放、请求头没读全就撞上
// 读请求头截止）后把连接一直读空，直到对端关闭，然后才由调用方关闭。
//
// 关闭时机与探测内容、长度都无关：不按固定字节数断（以前 Shadowsocks 读完
// salt+18 字节、VMess 读完 16 字节就断，量一下就能认出协议），也不按固定时刻
// 断（以前统一在建连第 10 秒 FIN，10 秒不是常见值）。NDSS'20（Frolov 等）实测
// 互联网上最常见的「不回数据」行为就是永不超时、一直读，并建议探测失败的连接
// 不设超时。对端关了我们才关，此时接收缓冲已读空，发的是 FIN 不是 RST。
//
// 兜底都只在被灌流量或被占名额时才触发，触发时关的都是已读空的连接（FIN）：
//   - 读满 drainMaxBytes 即关，不给探测方当黑洞带宽用；
//   - 全进程同时排空的连接数到 drainMaxConcurrent 时，踢掉最老的一条放新的进来
//     （见 drainRegistry）：名额被占满时新来的探测照样排空，不会退回「读到固定
//     字节数就 RST」；被踢的那条早已读空，关它发的是 FIN；
//   - 同一来源（IPv4 地址、IPv6 /64）同时排空的连接数到 drainMaxPerSource 时，
//     踢掉同一来源最老的一条：一个来源占不满全进程的名额；
//   - 每条排空至多 drainMaxDuration 加一段随机抖动，与对端行为无关：对端内核
//     活着就会回应 TCP keepalive，只靠 keepalive 收尾的话挂着不发 FIN 的连接
//     永远不走。抖动让这个上界不是一个可量的固定值。
//
// 并发上限就是这条路径的内存上限（每条排空连接常驻一个 goroutine 栈与
// drainReadBuffer 的读缓冲，约 8KB，见 TestDrainMemoryPerConn）。入站 Close 会
// 关掉所有在册连接，排空随之结束。
func drainUntilPeerClose(conn net.Conn) {
	if conn == nil {
		return
	}
	// 读请求头的截止时间在这里换成排空总时限：认证失败与「数据还不够、继续等」
	// 在外面看是同一个样子，都是收下数据、不回字节、等对端先关。先设截止再登记，
	// 被踢时（登记之后）关连接不会被这里覆盖。
	_ = conn.SetReadDeadline(time.Now().Add(drainDuration()))
	entry, ok := drains.register(conn)
	if !ok {
		return
	}
	defer drains.unregister(entry)
	buf := make([]byte, drainReadBuffer)
	var total int64
	for total < drainMaxBytes {
		n, err := conn.Read(buf)
		total += int64(n)
		if err != nil {
			return
		}
	}
}

// drainAfterReport 标记「认证失败、关之前要排空」的会话错误。适配器的
// handleConn 先上报（不必等对端关才在日志里看到认证失败），再排空，最后关闭。
// Unwrap 保留原错误，分类与日志照旧。
type drainAfterReport struct{ err error }

func (e drainAfterReport) Error() string { return e.err.Error() }
func (e drainAfterReport) Unwrap() error { return e.err }

// finishSession 是 Shadowsocks / VMess 会话的收尾：错误要求排空的先排空，
// 然后关闭。
func finishSession(conn net.Conn, err error) {
	var drain drainAfterReport
	if errors.As(err, &drain) {
		drainUntilPeerClose(conn)
	}
	_ = conn.Close()
}

// drainMaxConcurrentDefault 是全进程同时排空的连接数上限。正常情况下排空中
// 的连接只有零星探测与扫描器；4096 条约占 35MB（每条约 8KB），满了就踢最老的。
const drainMaxConcurrentDefault = 4096

// drainMaxPerSourceDefault 是同一来源同时排空的连接数上限。探测方一般一个来源
// 一两条；16 条之外的同源连接只是在占名额。
const drainMaxPerSourceDefault = 16

// drainMaxDurationDefault 与 drainJitterDefault：每条排空在 [5, 10) 分钟里随机
// 一个时刻关（读空后 FIN）。远长于常见探测的等待时间，又让被占的名额与 fd 最终
// 一定归还。
const (
	drainMaxDurationDefault = 5 * time.Minute
	drainJitterDefault      = 5 * time.Minute
)

// drainReadBuffer 是排空时的读缓冲。刻意比 io.Discard 的 8KB 池化缓冲小：
// 排空连接绝大多数时间阻塞在 Read 上，缓冲一直被占着，实测每条连接常驻从约
// 14.5KB 降到约 8KB；代价只是被灌流量时多几次 read 系统调用（16MB 上限内）。
const drainReadBuffer = 2 << 10

// drainActive 是正在排空的连接数（在 drains.mu 内增减，读用 Load）；
// drainMaxConcurrent、drainMaxPerSource、drainMaxDuration、drainJitter 是上限
// 与时限（测试可改）。
var (
	drainActive, drainMaxConcurrent, drainMaxPerSource atomic.Int64
	drainMaxDuration, drainJitter                      atomic.Int64 // time.Duration
)

func init() {
	drainMaxConcurrent.Store(drainMaxConcurrentDefault)
	drainMaxPerSource.Store(drainMaxPerSourceDefault)
	drainMaxDuration.Store(int64(drainMaxDurationDefault))
	drainJitter.Store(int64(drainJitterDefault))
}

// drainDuration 是一条排空的总时限：drainMaxDuration 加 [0, drainJitter) 的随机量。
func drainDuration() time.Duration {
	d := time.Duration(drainMaxDuration.Load())
	if j := drainJitter.Load(); j > 0 {
		d += time.Duration(rand.Int64N(j))
	}
	return d
}

// requestHeaderTimeout 是 Shadowsocks / VMess 读请求头的截止时间；override
// 只给测试缩短用，零值取 10 秒。撞上它的连接同样转入 drainUntilPeerClose。
func requestHeaderTimeout(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return 10 * time.Second
}

// drainMaxBytes 是认证失败后最多读掉的字节数，防止被当成黑洞带宽放大。
const drainMaxBytes = 16 << 20
