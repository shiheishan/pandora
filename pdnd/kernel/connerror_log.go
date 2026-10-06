// [INPUT]: 依赖 adapter.go 的 ConnError，依赖 connerror.go 的 classifyConnError / sanitizeConnErrorReason / maskRemoteAddr，依赖 log/slog
// [OUTPUT]: 包内提供 connErrorLogSink（newConnErrorLogSink、Report、Close）与缺省限流参数 connErrorLogBurst / connErrorLogWindow
// [POS]: kernel 连接失败观测链的出口：nativecore.go 每个 NativeCore 持有一个，把 Report 接到所有入站的 AdapterHooks.OnConnError，Close 时补打最后一轮抑制摘要

package kernel

import (
	"log/slog"
	"sort"
	"sync"
	"time"
)

// 缺省限流：每个 (入站, 协议, 阶段, 分类) 每分钟最多逐条记 5 条，其余只计数。
//
// 公网节点上扫描器的流量是常态，一个端口一分钟挨几千次探测并不稀奇。逐条
// 落盘会把磁盘和 journald 一起打满，真正有用的那几条也会被淹掉。按分类分桶
// 的好处是：扫描器刷爆 protocol 桶时，某个用户的 auth 失败仍然有自己的配额。
const (
	connErrorLogBurst  = 5
	connErrorLogWindow = time.Minute
)

type connErrorKey struct {
	tag, protocol, stage, category string
}

type connErrorCount struct {
	logged, suppressed int
}

// connErrorLogSink 把 ConnError 写成结构化日志，按固定窗口限流。
//
// 窗口从这一轮的第一条失败开始计时，到点由 time.AfterFunc 打一轮抑制摘要并
// 清零；没有失败时不挂任何定时器，也就没有常驻 goroutine 要回收。Report 在
// 连接自己的 goroutine 里被调用，锁内只做计数，日志写在锁外：被限流的那条
// 失败只花一次加锁与一次 map 查找。
type connErrorLogSink struct {
	log    *slog.Logger
	burst  int
	window time.Duration

	mu     sync.Mutex
	counts map[connErrorKey]*connErrorCount
	timer  *time.Timer
	closed bool
}

func newConnErrorLogSink(log *slog.Logger, burst int, window time.Duration) *connErrorLogSink {
	if log == nil {
		log = slog.Default()
	}
	return &connErrorLogSink{log: log, burst: burst, window: window, counts: make(map[connErrorKey]*connErrorCount)}
}

// Report 是 AdapterHooks.OnConnError 的实现，并发安全，不阻塞在锁以外的任何东西上。
func (s *connErrorLogSink) Report(ev ConnError) {
	category := classifyConnError(ev.Err)
	key := connErrorKey{tag: ev.Tag, protocol: ev.Protocol, stage: ev.Stage, category: category}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(s.window, s.flush)
	}
	count := s.counts[key]
	if count == nil {
		count = &connErrorCount{}
		s.counts[key] = count
	}
	if count.logged >= s.burst {
		count.suppressed++
		s.mu.Unlock()
		return
	}
	count.logged++
	s.mu.Unlock()

	// 刻意不记 err.Error() 原文、不记完整对端地址：reason 已抹掉凭据、目标
	// 地址与域名，remote 只留网段。见 connerror.go。
	attrs := []any{
		"inbound", ev.Tag, "protocol", ev.Protocol, "stage", ev.Stage,
		"category", category, "reason", sanitizeConnErrorReason(ev.Err),
	}
	if remote := maskRemoteAddr(ev.Remote); remote != "" {
		attrs = append(attrs, "remote", remote)
	}
	s.log.Info("入站连接失败", attrs...)
}

// flush 是窗口到点的回调：打出本轮被抑制的条数，然后开始新一轮。
func (s *connErrorLogSink) flush() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	counts := s.counts
	s.counts = make(map[connErrorKey]*connErrorCount)
	s.timer = nil
	s.mu.Unlock()
	s.logSuppressed(counts)
}

// Close 停掉定时器并补打最后一轮摘要；之后的 Report 一律丢弃。
func (s *connErrorLogSink) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	counts := s.counts
	s.counts = nil
	s.mu.Unlock()
	s.logSuppressed(counts)
}

func (s *connErrorLogSink) logSuppressed(counts map[connErrorKey]*connErrorCount) {
	keys := make([]connErrorKey, 0, len(counts))
	for key, count := range counts {
		if count.suppressed > 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.tag != b.tag {
			return a.tag < b.tag
		}
		if a.protocol != b.protocol {
			return a.protocol < b.protocol
		}
		if a.stage != b.stage {
			return a.stage < b.stage
		}
		return a.category < b.category
	})
	for _, key := range keys {
		s.log.Info("入站连接失败已限流",
			"inbound", key.tag, "protocol", key.protocol, "stage", key.stage, "category", key.category,
			"suppressed", counts[key].suppressed, "window", s.window.String())
	}
}
