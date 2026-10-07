package nodefabric

import (
	"strings"
	"testing"
)

func TestNormalizeRuntimeState(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct {
		status, reason string
		want           RuntimeState
		score          int
	}{
		{"", "", RuntimeState{}, 90},
		{"", "port_in_use:443/tcp:other", RuntimeState{}, 90}, // 老节点不报状态：原因不收
		{"running", "stale", RuntimeState{Status: "running"}, 90},
		{" Running ", "", RuntimeState{Status: "running"}, 90},
		{"degraded", "port_in_use:443/tcp:other", RuntimeState{Status: "degraded", Reason: "port_in_use:443/tcp:other"}, 40},
		{"stopped", "not_started", RuntimeState{Status: "degraded", Reason: "not_started"}, 40},
		{"degraded", "bad\x00\nreasoné", RuntimeState{Status: "degraded", Reason: "badreason"}, 40},
		{"degraded", long, RuntimeState{Status: "degraded", Reason: long[:maxRuntimeReasonBytes]}, 40},
	} {
		got := NormalizeRuntimeState(tc.status, tc.reason)
		if got != tc.want || got.HealthScore() != tc.score {
			t.Errorf("NormalizeRuntimeState(%q, %q) = %+v score %d, want %+v score %d",
				tc.status, tc.reason, got, got.HealthScore(), tc.want, tc.score)
		}
	}
}

func TestRuntimeFailingSQLOnlyForKnownAlias(t *testing.T) {
	if sql := RuntimeFailingSQL("n"); !strings.Contains(sql, "starts_with(n.runtime_reason, 'port_in_use:')") ||
		!strings.Contains(sql, "n.runtime_reason = 'not_started'") || !strings.HasPrefix(sql, "coalesce(") {
		t.Fatalf("RuntimeFailingSQL lost a condition: %s", sql)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("RuntimeFailingSQL accepted an arbitrary alias")
		}
	}()
	_ = RuntimeFailingSQL("n; DROP")
}

func TestNormalizeTrafficReportID(t *testing.T) {
	for raw, want := range map[string]bool{
		"0b6a2c1e-3f4d-4b5a-9c8d-7e6f5a4b3c2d": true,
		"node-1:report_2.x":                    true,
		"":                                     false,
		strings.Repeat("a", 64):                true,
		strings.Repeat("a", 65):                false,
		"has space":                            false,
		"中文":                                   false,
		"semi;colon":                           false,
	} {
		id, ok := NormalizeTrafficReportID(raw)
		if ok != want || (ok && id != raw) || (!ok && id != "") {
			t.Errorf("NormalizeTrafficReportID(%q) = %q, %v; want ok=%v", raw, id, ok, want)
		}
	}
}
