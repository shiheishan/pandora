// Package stats 提供延迟样本分位数与对数直方图。
package stats

import (
	"math"
	"sort"
	"strconv"
	"sync"
)

// Recorder 并发安全地记录毫秒浮点延迟。
type Recorder struct {
	mu   sync.Mutex
	samp []float64
	hist map[string]int64
}

// NewRecorder 创建空的 Recorder。
func NewRecorder() *Recorder {
	return &Recorder{hist: make(map[string]int64)}
}

// Add 追加一条延迟样本（毫秒）。
func (r *Recorder) Add(ms float64) {
	r.mu.Lock()
	r.samp = append(r.samp, ms)
	us := math.Max(ms*1000, 1)
	b := int(math.Floor(math.Log(us) / math.Log(1.01)))
	key := strconv.Itoa(b)
	r.hist[key]++
	r.mu.Unlock()
}

// Summary 返回当前样本的汇总（N=0 时各数值为 0、Hist 为空 map）。
func (r *Recorder) Summary() Summary {
	r.mu.Lock()
	n := len(r.samp)
	if n == 0 {
		h := make(map[string]int64)
		r.mu.Unlock()
		return Summary{N: 0, P50: 0, P99: 0, Mean: 0, Max: 0, Hist: h}
	}
	sorted := append([]float64(nil), r.samp...)
	histCopy := make(map[string]int64, len(r.hist))
	for k, v := range r.hist {
		histCopy[k] = v
	}
	r.mu.Unlock()

	sort.Float64s(sorted)
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	return Summary{
		N:    n,
		P50:  Percentile(sorted, 0.5),
		P99:  Percentile(sorted, 0.99),
		Mean: sum / float64(n),
		Max:  sorted[n-1],
		Hist: histCopy,
	}
}

// Summary 是延迟分布的汇总。
type Summary struct {
	N    int              `json:"n"`
	P50  float64          `json:"p50"`
	P99  float64          `json:"p99"`
	Mean float64          `json:"mean"`
	Max  float64          `json:"max"`
	Hist map[string]int64 `json:"hist"`
}

// Percentile 在最近秩 sorted 上取分位数：sorted[ceil(q*n)-1]，n=0 返回 0。
func Percentile(sorted []float64, q float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// HistPercentile 从对数直方图还原分位数；hist 为空返回 0。
func HistPercentile(hist map[string]int64, q float64) float64 {
	if len(hist) == 0 {
		return 0
	}
	var total int64
	for _, c := range hist {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := int64(math.Ceil(q * float64(total)))

	keys := make([]int, 0, len(hist))
	for k := range hist {
		b, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		keys = append(keys, b)
	}
	sort.Ints(keys)

	var cum int64
	for _, b := range keys {
		cum += hist[strconv.Itoa(b)]
		if cum >= target {
			upperUs := math.Pow(1.01, float64(b+1))
			return upperUs / 1000.0
		}
	}
	return 0
}
