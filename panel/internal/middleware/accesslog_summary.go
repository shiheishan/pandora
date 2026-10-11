package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
)

// accessSummaryInterval 是 access_summary 的间隔。
const accessSummaryInterval = time.Minute

// maxRouteEntries 是一个网关统计表的条目上限：最大的后台网关约 200 条路由，乘上少数几种
// 方法与 404/405 的模板，正常远到不了；到了上限之后新出现的键全部并进一个溢出条目。
const maxRouteEntries = 1024

// accessLatencyBoundsMS 是耗时直方图的上界（毫秒，含）：照新标准里的各条线取（门户读
// p50 2 / p99 15，节点 push 20、其余 10，后台列表 80……），最后一格是更慢的全部。
var accessLatencyBoundsMS = []int64{1, 2, 3, 5, 10, 15, 20, 30, 50, 80, 100, 150, 200, 300, 500, 1000}

// routeStats 是一个网关按路由的累计统计（从进程启动起，不清零）。每隔 accessSummaryInterval，
// 由那之后的第一个请求顺手打出每条有流量的路由一行 info「access_summary」：
//
//	route        「方法 路由模板」；方法只认标准的九种，其余记 OTHER（normalizeMethod），
//	             条目到了上限（maxRouteEntries）后新键并进「* (overflow)」
//	n            请求数；s2xx / s3xx / s4xx / s5xx 按状态分类
//	le_ms, hist  耗时直方图：hist[i] 是耗时 ≤ le_ms[i] 毫秒的请求数（不累加），最后一格是更慢的
//	dur_us_sum   耗时总和
//	db_rt_sum / db_rt_max / kv_rt_sum / kv_rt_max  往返数的总和与最大值
//
// 都是累计值：复测取窗口首尾两行相减，就是窗口内的分布（p50 / p99 落在哪一格）。
// 逐请求的 access 行降级（QuietSuccess）时，分布只能从这里看。
type routeStats struct {
	every time.Duration
	max   int          // 条目上限（见 maxRouteEntries）
	next  atomic.Int64 // 下一次打统计的时刻（UnixNano）
	mu    sync.RWMutex
	byKey map[routeKey]*routeEntry
	// overflow 收下到了上限之后新出现的键
	overflow routeEntry
}

// routeKey 不拼字符串：chi 的路由模板分段（各级路由器的模板）原样做键，查表零分配；
// 拼好的名字只在第一次见到这条路由时算一次。
type routeKey struct {
	method string
	parts  [6]string
	n      int
}

type routeEntry struct {
	name                       string
	n, s2, s3, s4, s5          atomic.Int64
	hist                       [17]atomic.Int64
	durSum                     atomic.Int64
	dbSum, dbMax, kvSum, kvMax atomic.Int64
}

func newRouteStats(every time.Duration) *routeStats {
	s := &routeStats{every: every, max: maxRouteEntries, byKey: map[routeKey]*routeEntry{}}
	s.overflow.name = "* (overflow)"
	s.next.Store(time.Now().Add(every).UnixNano())
	return s
}

// entry 找到（或第一次建出）这个请求的路由条目。
func (s *routeStats) entry(r *http.Request) *routeEntry {
	method := normalizeMethod(r.Method)
	key := routeKey{method: method}
	rctx := chi.RouteContext(r.Context())
	overflow := false
	if rctx != nil {
		if len(rctx.RoutePatterns) > len(key.parts) {
			overflow = true
		} else {
			key.n = copy(key.parts[:], rctx.RoutePatterns)
		}
	}
	if overflow {
		// 嵌套超过 6 级的路由（目前没有）：退回按拼好的名字做键
		key = routeKey{method: method, parts: [6]string{rctx.RoutePattern()}, n: -1}
	}
	s.mu.RLock()
	e := s.byKey[key]
	s.mu.RUnlock()
	if e != nil {
		return e
	}
	pattern := ""
	if rctx != nil {
		pattern = rctx.RoutePattern()
	}
	if pattern == "" {
		pattern = "-" // 没匹配上任何路由（404 / 405）
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e = s.byKey[key]; e == nil {
		if len(s.byKey) >= s.max {
			// 纵深防御：方法归一之后键空间只剩路由器自己的模板，正常到不了这里
			return &s.overflow
		}
		e = &routeEntry{name: method + " " + pattern}
		s.byKey[key] = e
	}
	return e
}

func (e *routeEntry) record(status int, dur time.Duration, db, kv int) {
	e.n.Add(1)
	switch {
	case status >= 500:
		e.s5.Add(1)
	case status >= 400:
		e.s4.Add(1)
	case status >= 300:
		e.s3.Add(1)
	default:
		e.s2.Add(1)
	}
	ms := dur.Milliseconds()
	if dur%time.Millisecond != 0 {
		ms++ // 上界含：1.2ms 落进 ≤2 那一格
	}
	i := sort.Search(len(accessLatencyBoundsMS), func(i int) bool { return accessLatencyBoundsMS[i] >= ms })
	e.hist[i].Add(1)
	e.durSum.Add(dur.Microseconds())
	e.dbSum.Add(int64(db))
	e.kvSum.Add(int64(kv))
	atomicMax(&e.dbMax, int64(db))
	atomicMax(&e.kvMax, int64(kv))
}

func atomicMax(a *atomic.Int64, v int64) {
	for {
		cur := a.Load()
		if v <= cur || a.CompareAndSwap(cur, v) {
			return
		}
	}
}

// maybeFlush 到点了就打一轮统计；同一时刻只有抢到的那个请求打。
func (s *routeStats) maybeFlush(ctx context.Context, now time.Time, log *slog.Logger) {
	next := s.next.Load()
	if now.UnixNano() < next || !s.next.CompareAndSwap(next, now.Add(s.every).UnixNano()) {
		return
	}
	if !log.Enabled(ctx, slog.LevelInfo) {
		return
	}
	s.mu.RLock()
	entries := make([]*routeEntry, 0, len(s.byKey))
	for _, e := range s.byKey {
		entries = append(entries, e)
	}
	s.mu.RUnlock()
	if s.overflow.n.Load() > 0 {
		entries = append(entries, &s.overflow)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, e := range entries {
		hist := make([]int64, len(e.hist))
		for i := range e.hist {
			hist[i] = e.hist[i].Load()
		}
		log.LogAttrs(ctx, slog.LevelInfo, "access_summary",
			slog.String("route", e.name),
			slog.Int64("n", e.n.Load()),
			slog.Int64("s2xx", e.s2.Load()), slog.Int64("s3xx", e.s3.Load()),
			slog.Int64("s4xx", e.s4.Load()), slog.Int64("s5xx", e.s5.Load()),
			slog.Any("le_ms", accessLatencyBoundsMS),
			slog.Any("hist", hist),
			slog.Int64("dur_us_sum", e.durSum.Load()),
			slog.Int64("db_rt_sum", e.dbSum.Load()), slog.Int64("db_rt_max", e.dbMax.Load()),
			slog.Int64("kv_rt_sum", e.kvSum.Load()), slog.Int64("kv_rt_max", e.kvMax.Load()))
	}
}

// normalizeMethod 把客户端给的方法归到固定集合：标准方法原样，其余一律 OTHER。
// 方法名是客户端可控的（nginx 只校验字符，几 KB 长的自造方法也会转发进来、由 chi 回 405），
// 不归一的话每换一个名字统计表就多一个永久条目，日志里也会写进任意长的原文。
func normalizeMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return m
	}
	return "OTHER"
}
