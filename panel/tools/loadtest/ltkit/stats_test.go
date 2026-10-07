package ltkit

import (
	"bytes"
	"strings"
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

// 每单位每分钟：有稳态窗口就只数窗口内，分母取窗口与运行区间的交集；按状态码再拆。
func TestRecorderPerUnitRates(t *testing.T) {
	r := NewRecorder("t", time.Second)
	r.SetPerUnit("node", 2)
	r.start = time.Now().Add(-4 * time.Minute)
	for i := 0; i < 5; i++ { // 窗口外的起跑请求
		r.Observe(Observation{Endpoint: "cfg", Status: 200, Latency: time.Millisecond})
	}
	// 窗口从两分钟前开始、一小时长：运行到此刻为止，交集是两分钟
	r.SetSteady(time.Now().Add(-2*time.Minute), time.Hour)
	for i := 0; i < 15; i++ {
		r.Observe(Observation{Endpoint: "cfg", Status: 204, Latency: time.Millisecond})
	}
	r.Observe(Observation{Endpoint: "cfg", Status: 200, Latency: time.Millisecond})
	for i := 0; i < 4; i++ {
		r.Observe(Observation{Endpoint: "user", Status: 304, Latency: time.Millisecond})
	}
	r.Stop()
	rep := r.Snapshot()
	if rep.PerUnit == nil || rep.PerUnit.Window != "steady" || rep.PerUnit.Units != 2 ||
		rep.PerUnit.Minutes < 1.99 || rep.PerUnit.Minutes > 2.01 {
		t.Fatalf("per unit %+v", rep.PerUnit)
	}
	cfg := rep.Endpoints[0]
	if cfg.Endpoint != "cfg" || cfg.PerUnitPerMin != 4 || cfg.PerUnitPerMinByCode["204"] != 3.75 ||
		cfg.PerUnitPerMinByCode["200"] != 0.25 {
		t.Fatalf("cfg %v %v", cfg.PerUnitPerMin, cfg.PerUnitPerMinByCode)
	}
	if rep.Totals.PerUnitPerMin != 5 || rep.Totals.PerUnitPerMinByCode["304"] != 1 {
		t.Fatalf("totals %v %v", rep.Totals.PerUnitPerMin, rep.Totals.PerUnitPerMinByCode)
	}
	var buf bytes.Buffer
	WriteSummary(&buf, rep)
	if !strings.Contains(buf.String(), "per node per minute (steady window") ||
		!strings.Contains(buf.String(), "200=0.25 204=3.75") {
		t.Fatalf("summary lacks the per-node table:\n%s", buf.String())
	}

	// 没给窗口：按整个运行区间折
	run := NewRecorder("t", time.Second)
	run.SetPerUnit("node", 1)
	run.start = time.Now().Add(-time.Minute)
	run.Observe(Observation{Endpoint: "e", Status: 200, Latency: time.Millisecond})
	run.Stop()
	if pu := run.Snapshot().PerUnit; pu == nil || pu.Window != "run" {
		t.Fatalf("run window %+v", pu)
	}
	if NewRecorder("t", time.Second).Snapshot().PerUnit != nil {
		t.Fatal("per-unit stats must stay off unless SetPerUnit was called")
	}
}
