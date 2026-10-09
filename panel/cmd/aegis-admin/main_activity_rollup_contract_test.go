package main

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 行为趋势按天汇总（00114）：重算最近 2 个已结束日与 400 天保留期清理挂在保留期清理循环里，
// 不另起循环。读路径只用可用行，这一步断了趋势图仍然正确（回到实时算），只是变慢——所以要钉住。
func TestAdminWiresActivityDailyRollup(t *testing.T) {
	run := sourcetest.Load(t, ".").Decl("run")
	purge := strings.Index(run, "nodeSvc.PurgeTrafficRollups(sctx, middleware.DefaultTenantID)")
	if purge < 0 {
		t.Fatal("retention worker not found")
	}
	for _, want := range []string{
		"opsSvc.RefreshActivityDaily(sctx, middleware.DefaultTenantID)",
		"opsSvc.PurgeActivityDaily(sctx, middleware.DefaultTenantID)",
	} {
		at := strings.Index(run, want)
		if at < 0 {
			t.Fatalf("admin gateway retention worker missing %q", want)
		}
		// 与流量汇总清理同一个循环体：两者之间没有另起的 goroutine
		lo, hi := min(at, purge), max(at, purge)
		if strings.Contains(run[lo:hi], "go func()") {
			t.Fatalf("%q must run in the retention worker, not a new loop", want)
		}
	}
	if strings.Count(run, "workers.Add(") != 1 || !strings.Contains(run, "workers.Add(8)") {
		t.Fatal("admin gateway must keep exactly eight background workers")
	}
}
