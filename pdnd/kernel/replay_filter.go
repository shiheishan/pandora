package kernel

import (
	"io"
	"net"
	"sync"
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

// drainUntilDeadline 在认证失败后把连接一直读空，直到对端关闭或撞上连接上
// 已有的读截止时间（读请求头的那个 10 秒），然后才由调用方关闭。
//
// 以前 Shadowsocks 读完 salt+18 字节、VMess 读完 16 字节就断：探测方只要量
// 一下「发多少字节后被断」就能认出协议。现在认证失败与「数据还不够、继续
// 等」在外面看是同一个样子：收下所有数据、不回任何字节、到点关闭。
func drainUntilDeadline(conn net.Conn) {
	if conn == nil {
		return
	}
	// 兜底：WS / gRPC / XHTTP 这类包装连接的 SetReadDeadline 可能不生效，
	// 到点直接关，最多多等一个请求头读超时。
	timer := time.AfterFunc(inboundHandshakeTimeout, func() { _ = conn.Close() })
	defer timer.Stop()
	// 撞上字节上限说明对端在灌流量，直接关即可；超时、EOF 本来就该结束。
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, drainMaxBytes))
}

// requestHeaderTimeout 是 Shadowsocks / VMess 读请求头的截止时间；override
// 只给测试缩短用，零值取 10 秒。认证失败后的读空也到这个点为止。
func requestHeaderTimeout(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return 10 * time.Second
}

// drainMaxBytes 是认证失败后最多读掉的字节数，防止被当成黑洞带宽放大。
const drainMaxBytes = 16 << 20
