package ltkit

import (
	"testing"
	"time"
)

// 只有落在稳态窗口里的观测进 steady；没设窗口或窗口已过时报告里没有 steady 段。
func TestRecorderSteadyWindow(t *testing.T) {
	r := NewRecorder("t", time.Second)
	for i := 0; i < 10; i++ { // 窗口外的起跑尖峰
		r.Observe(Observation{Endpoint: "e", Status: 200, Latency: 900 * time.Millisecond})
	}
	r.SetSteady(time.Now(), time.Hour)
	for i := 0; i < 99; i++ {
		r.Observe(Observation{Endpoint: "e", Status: 200, Latency: 10 * time.Millisecond})
	}
	r.Observe(Observation{Endpoint: "e", Status: 503, Latency: 20 * time.Millisecond})
	st := r.Snapshot().Endpoints[0]
	if st.Count != 110 || st.Steady == nil {
		t.Fatalf("count=%d steady=%v", st.Count, st.Steady)
	}
	if st.Steady.Count != 100 || st.Steady.Server5x != 1 {
		t.Fatalf("steady count=%d 5xx=%d", st.Steady.Count, st.Steady.Server5x)
	}
	if st.P99MS < 800 || st.Steady.P99MS > 25 {
		t.Fatalf("full p99=%.1f steady p99=%.1f: window did not exclude the outlier", st.P99MS, st.Steady.P99MS)
	}

	past := NewRecorder("t", time.Second)
	past.SetSteady(time.Now().Add(-2*time.Hour), time.Hour)
	past.Observe(Observation{Endpoint: "e", Status: 200, Latency: time.Millisecond})
	if past.Snapshot().Endpoints[0].Steady != nil {
		t.Fatal("observation outside the window produced steady stats")
	}
}
