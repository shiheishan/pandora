// [INPUT]: 依赖 sync、math、time 与 encoding/json，只用标准库
// [OUTPUT]: 对外提供 Recorder（NewRecorder、SetMeta、Observe、Stop、Snapshot、WriteFiles）、Observation、Report、EndpointStats、Window、WriteSummary
// [POS]: tools/loadtest/ltkit 的计量底座：nodes、users、burst 共用同一个记录器，保证三类场景的 QPS、分位数与错误码按同一口径计算
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package ltkit

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 直方图：对数分桶，24 小时混合场景也是常数内存
// ---------------------------------------------------------------------------
//
// 每桶宽 2%：分位数误差不超过 2%，对「p99 < 300ms」这种及格线足够。
// 下限 50µs，上限约 2 分钟，超出的落在两端桶里。

const (
	histBase    = 1.02
	histMinUS   = 50.0
	histBuckets = 760 // 50µs * 1.02^760 ≈ 170s
)

type histogram struct {
	counts [histBuckets]uint64
	total  uint64
	sumUS  float64
	maxUS  float64
}

func (h *histogram) add(d time.Duration) {
	us := float64(d) / float64(time.Microsecond)
	idx := 0
	if us > histMinUS {
		idx = int(math.Log(us/histMinUS) / math.Log(histBase))
		if idx >= histBuckets {
			idx = histBuckets - 1
		}
	}
	h.counts[idx]++
	h.total++
	h.sumUS += us
	if us > h.maxUS {
		h.maxUS = us
	}
}

// quantileMS 取桶的上沿，宁可报高不报低。
func (h *histogram) quantileMS(q float64) float64 {
	if h.total == 0 {
		return 0
	}
	want := uint64(math.Ceil(q * float64(h.total)))
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen >= want {
			upper := histMinUS * math.Pow(histBase, float64(i+1))
			return math.Min(upper, h.maxUS) / 1000
		}
	}
	return h.maxUS / 1000
}

// ---------------------------------------------------------------------------
// Recorder
// ---------------------------------------------------------------------------

// Observation 是一次请求的结果。Status 为 0 表示没拿到 HTTP 响应（连接错误、超时）。
type Observation struct {
	Endpoint string
	Status   int
	Latency  time.Duration
	// Err 只在 Status 为 0 时有意义，按 Error() 归类计数。
	Err error
	// Flag 是场景自定义的计数标签（例如 "sig_fail"、"etag_304"），可空。
	Flag string
}

type endpointAcc struct {
	hist   histogram
	codes  map[string]uint64
	flags  map[string]uint64
	window map[int64]*windowAcc
}

type windowAcc struct {
	count  uint64
	errors uint64 // 5xx 与无响应
	maxUS  float64
}

// Recorder 线程安全。Window 是时间线的粒度（默认 10 秒）：burst 靠它看出尖峰。
type Recorder struct {
	Scenario string
	Window   time.Duration

	mu        sync.Mutex
	start     time.Time
	stop      time.Time // 非零时 QPS 与时长按 [start, stop] 算，收尾排空的那几秒不摊薄 QPS
	endpoints map[string]*endpointAcc
	meta      map[string]any
}

func NewRecorder(scenario string, window time.Duration) *Recorder {
	if window <= 0 {
		window = 10 * time.Second
	}
	return &Recorder{
		Scenario:  scenario,
		Window:    window,
		start:     time.Now(),
		endpoints: map[string]*endpointAcc{},
		meta:      map[string]any{},
	}
}

// SetMeta 记录场景参数（档位、速率、节点数），原样进 JSON。
func (r *Recorder) SetMeta(key string, value any) {
	r.mu.Lock()
	r.meta[key] = value
	r.mu.Unlock()
}

// Stop 标记压测窗口结束（只认第一次）。之后到达的观测（排空中的在途请求、
// 节点退出前的最后一次上报）照常计数，但不再拉长分母。
func (r *Recorder) Stop() {
	r.mu.Lock()
	if r.stop.IsZero() {
		r.stop = time.Now()
	}
	r.mu.Unlock()
}

func (r *Recorder) Observe(o Observation) {
	now := time.Now()
	code := strconv.Itoa(o.Status)
	if o.Status == 0 {
		code = "transport"
		if o.Err != nil {
			code = "transport:" + classifyErr(o.Err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	acc := r.endpoints[o.Endpoint]
	if acc == nil {
		acc = &endpointAcc{codes: map[string]uint64{}, flags: map[string]uint64{}, window: map[int64]*windowAcc{}}
		r.endpoints[o.Endpoint] = acc
	}
	acc.hist.add(o.Latency)
	acc.codes[code]++
	if o.Flag != "" {
		acc.flags[o.Flag]++
	}
	slot := int64(now.Sub(r.start) / r.Window)
	w := acc.window[slot]
	if w == nil {
		w = &windowAcc{}
		acc.window[slot] = w
	}
	w.count++
	if o.Status == 0 || o.Status >= 500 {
		w.errors++
	}
	us := float64(o.Latency) / float64(time.Microsecond)
	if us > w.maxUS {
		w.maxUS = us
	}
}

func classifyErr(err error) string {
	type timeout interface{ Timeout() bool }
	if t, ok := err.(timeout); ok && t.Timeout() {
		return "timeout"
	}
	s := err.Error()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ---------------------------------------------------------------------------
// 报告
// ---------------------------------------------------------------------------

type Report struct {
	Scenario  string          `json:"scenario"`
	StartedAt time.Time       `json:"started_at"`
	DurationS float64         `json:"duration_s"`
	WindowS   float64         `json:"window_s"`
	Meta      map[string]any  `json:"meta"`
	Endpoints []EndpointStats `json:"endpoints"`
	Totals    EndpointStats   `json:"totals"`
}

type EndpointStats struct {
	Endpoint string            `json:"endpoint"`
	Count    uint64            `json:"count"`
	QPS      float64           `json:"qps"`
	MeanMS   float64           `json:"mean_ms"`
	P50MS    float64           `json:"p50_ms"`
	P95MS    float64           `json:"p95_ms"`
	P99MS    float64           `json:"p99_ms"`
	MaxMS    float64           `json:"max_ms"`
	Codes    map[string]uint64 `json:"codes"`
	Flags    map[string]uint64 `json:"flags,omitempty"`
	Server5x uint64            `json:"server_5xx"`
	Timeline []Window          `json:"timeline,omitempty"`
}

type Window struct {
	OffsetS float64 `json:"offset_s"`
	Count   uint64  `json:"count"`
	Errors  uint64  `json:"errors"`
	MaxMS   float64 `json:"max_ms"`
}

func (r *Recorder) Snapshot() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := time.Now()
	if !r.stop.IsZero() {
		end = r.stop
	}
	elapsed := end.Sub(r.start).Seconds()
	rep := Report{
		Scenario: r.Scenario, StartedAt: r.start, DurationS: elapsed,
		WindowS: r.Window.Seconds(), Meta: map[string]any{},
	}
	for k, v := range r.meta {
		rep.Meta[k] = v
	}
	var all histogram
	totals := EndpointStats{Endpoint: "TOTAL", Codes: map[string]uint64{}, Flags: map[string]uint64{}}
	names := make([]string, 0, len(r.endpoints))
	for name := range r.endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		acc := r.endpoints[name]
		st := statsOf(name, &acc.hist, elapsed)
		st.Codes, st.Flags = copyCounts(acc.codes), copyCounts(acc.flags)
		st.Server5x = count5xx(acc.codes)
		slots := make([]int64, 0, len(acc.window))
		for s := range acc.window {
			slots = append(slots, s)
		}
		sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
		for _, s := range slots {
			w := acc.window[s]
			st.Timeline = append(st.Timeline, Window{
				OffsetS: float64(s) * r.Window.Seconds(), Count: w.count, Errors: w.errors, MaxMS: w.maxUS / 1000,
			})
		}
		rep.Endpoints = append(rep.Endpoints, st)
		for i, c := range acc.hist.counts {
			all.counts[i] += c
		}
		all.total += acc.hist.total
		all.sumUS += acc.hist.sumUS
		all.maxUS = math.Max(all.maxUS, acc.hist.maxUS)
		for k, v := range acc.codes {
			totals.Codes[k] += v
		}
		for k, v := range acc.flags {
			totals.Flags[k] += v
		}
	}
	t := statsOf("TOTAL", &all, elapsed)
	t.Codes, t.Flags, t.Server5x = totals.Codes, totals.Flags, count5xx(totals.Codes)
	rep.Totals = t
	return rep
}

func statsOf(name string, h *histogram, elapsed float64) EndpointStats {
	st := EndpointStats{Endpoint: name, Count: h.total, MaxMS: h.maxUS / 1000}
	if elapsed > 0 {
		st.QPS = float64(h.total) / elapsed
	}
	if h.total > 0 {
		st.MeanMS = h.sumUS / float64(h.total) / 1000
	}
	st.P50MS, st.P95MS, st.P99MS = h.quantileMS(0.50), h.quantileMS(0.95), h.quantileMS(0.99)
	return st
}

func copyCounts(m map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func count5xx(codes map[string]uint64) uint64 {
	var n uint64
	for k, v := range codes {
		if c, err := strconv.Atoi(k); err == nil && c >= 500 {
			n += v
		}
	}
	return n
}

// WriteFiles 写 <dir>/<scenario>.json 与同名 .txt 的一页摘要（WriteSummary 的输出）。
func (r *Recorder) WriteFiles(dir string) (Report, error) {
	rep := r.Snapshot()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return rep, err
	}
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return rep, err
	}
	if err := os.WriteFile(dir+"/"+rep.Scenario+".json", body, 0o644); err != nil {
		return rep, err
	}
	f, err := os.Create(dir + "/" + rep.Scenario + ".txt")
	if err != nil {
		return rep, err
	}
	defer f.Close()
	WriteSummary(f, rep)
	return rep, nil
}

// WriteSummary 打一页文字摘要：每个端点一行，错误码只列非 2xx/304。
func WriteSummary(w io.Writer, rep Report) {
	fmt.Fprintf(w, "scenario %s  started %s  duration %.0fs\n", rep.Scenario, rep.StartedAt.Format(time.RFC3339), rep.DurationS)
	if len(rep.Meta) > 0 {
		keys := make([]string, 0, len(rep.Meta))
		for k := range rep.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s = %v\n", k, rep.Meta[k])
		}
	}
	fmt.Fprintf(w, "%-56s %9s %8s %8s %8s %8s %8s  %s\n", "endpoint", "count", "qps", "p50ms", "p95ms", "p99ms", "maxms", "non-2xx")
	for _, st := range append(rep.Endpoints, rep.Totals) {
		fmt.Fprintf(w, "%-56s %9d %8.2f %8.1f %8.1f %8.1f %8.1f  %s\n",
			st.Endpoint, st.Count, st.QPS, st.P50MS, st.P95MS, st.P99MS, st.MaxMS, oddCodes(st.Codes, st.Flags))
	}
}

func oddCodes(codes, flags map[string]uint64) string {
	keys := make([]string, 0, len(codes))
	for k := range codes {
		if c, err := strconv.Atoi(k); err == nil && (c < 300 || c == 304) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for _, k := range keys {
		s += fmt.Sprintf("%s=%d ", k, codes[k])
	}
	fkeys := make([]string, 0, len(flags))
	for k := range flags {
		fkeys = append(fkeys, k)
	}
	sort.Strings(fkeys)
	for _, k := range fkeys {
		s += fmt.Sprintf("[%s=%d] ", k, flags[k])
	}
	return s
}
