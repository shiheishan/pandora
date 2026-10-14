package stats

import (
	"encoding/json"
	"math"
	"sync"
	"testing"
)

func TestPercentileSmallSample(t *testing.T) {
	sorted := make([]float64, 100)
	for i := range sorted {
		sorted[i] = float64(i + 1)
	}
	if Percentile(sorted, 0.5) != 50 {
		t.Fatalf("p50 got %v", Percentile(sorted, 0.5))
	}
	if Percentile(sorted, 0.99) != 99 {
		t.Fatalf("p99 got %v", Percentile(sorted, 0.99))
	}
	if Percentile(nil, 0.5) != 0 {
		t.Fatal("空样本应 0")
	}
}

func TestRecorderConcurrentAdd(t *testing.T) {
	r := NewRecorder()
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < n/10; j++ {
				r.Add(1)
			}
		}()
	}
	wg.Wait()
	if r.Summary().N != n {
		t.Fatalf("N=%d", r.Summary().N)
	}
}

func TestHistPercentileVsExact(t *testing.T) {
	r := NewRecorder()
	const count = 10000
	for i := 1; i <= count; i++ {
		r.Add(float64(i))
	}
	s := r.Summary()
	exactP50 := Percentile(makeSorted1ToN(count), 0.5)
	histP50 := HistPercentile(s.Hist, 0.5)
	if relErr(histP50, exactP50) >= 0.02 {
		t.Fatalf("p50 相对误差过大: hist=%v exact=%v", histP50, exactP50)
	}
	exactP99 := Percentile(makeSorted1ToN(count), 0.99)
	histP99 := HistPercentile(s.Hist, 0.99)
	if relErr(histP99, exactP99) >= 0.02 {
		t.Fatalf("p99 相对误差过大: hist=%v exact=%v", histP99, exactP99)
	}
	if HistPercentile(map[string]int64{}, 0.5) != 0 {
		t.Fatal("空 hist")
	}
}

func makeSorted1ToN(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i + 1)
	}
	return out
}

func relErr(a, b float64) float64 {
	if b == 0 {
		if a == 0 {
			return 0
		}
		return math.Abs(a)
	}
	return math.Abs(a-b) / math.Abs(b)
}

func TestSummaryJSON(t *testing.T) {
	r := NewRecorder()
	r.Add(1.5)
	s := r.Summary()
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
	empty := NewRecorder().Summary()
	if empty.Hist == nil {
		t.Fatal("N=0 时 Hist 应为非 nil 空 map")
	}
	if empty.N != 0 || empty.P50 != 0 {
		t.Fatalf("空 Summary %+v", empty)
	}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
}
