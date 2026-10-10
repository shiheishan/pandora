package kernel

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// replayFilter 是按时间分代的「见过即拒」集合，给 Shadowsocks 的 TCP salt 与
// VMess 的 authID 防重放用。
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
// 兜底只有两条，都只在被灌流量时才会触发：
//   - 读满 drainMaxBytes 即关，不给探测方当黑洞带宽用；
//   - 全进程同时排空的连接数到 drainMaxConcurrent 时，新的失败连接立即关。
//     并发上限就是这条路径的内存上限（每条排空连接常驻一个 goroutine 栈与
//     drainReadBuffer 的读缓冲，约 8KB，见 TestDrainMemoryPerConn）。
//
// 对端失联（不发 FIN）的连接靠 TCP keepalive（Go 监听缺省 15 秒起探）收尾；
// 入站 Close 会关掉所有在册连接，排空随之结束。
func drainUntilPeerClose(conn net.Conn) {
	if conn == nil {
		return
	}
	if drainActive.Add(1) > drainMaxConcurrent.Load() {
		drainActive.Add(-1)
		return
	}
	defer drainActive.Add(-1)
	// 读请求头的截止时间在这里清掉：认证失败与「数据还不够、继续等」在外面
	// 看是同一个样子，都是收下数据、不回字节、等对端先关。
	_ = conn.SetReadDeadline(time.Time{})
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

// drainMaxConcurrentDefault 是全进程同时排空的连接数上限。正常情况下排空中
// 的连接只有零星探测与扫描器；4096 条约占 35MB（每条约 8KB），超过即说明在
// 被洪泛，退回立即关。
const drainMaxConcurrentDefault = 4096

// drainReadBuffer 是排空时的读缓冲。刻意比 io.Discard 的 8KB 池化缓冲小：
// 排空连接绝大多数时间阻塞在 Read 上，缓冲一直被占着，实测每条连接常驻从约
// 14.5KB 降到约 8KB；代价只是被灌流量时多几次 read 系统调用（16MB 上限内）。
const drainReadBuffer = 2 << 10

// drainActive 是正在排空的连接数；drainMaxConcurrent 是其上限（测试可改）。
var drainActive, drainMaxConcurrent atomic.Int64

func init() { drainMaxConcurrent.Store(drainMaxConcurrentDefault) }

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
