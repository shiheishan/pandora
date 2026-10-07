package main

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 追加写表 31 天保留期与节点 × uid 按天汇总（w5retain，00131 / 00133）：挂在现有保留期循环体里，
// 不另起循环。断了不会报错，只会让留档与拉取日志一直涨、按天表一直空——所以要钉住。
func TestAdminWiresAppendOnlyRetention(t *testing.T) {
	run := sourcetest.Load(t, ".").Decl("run")
	purge := strings.Index(run, "nodeSvc.PurgeTrafficRollups(sctx, middleware.DefaultTenantID)")
	if purge < 0 {
		t.Fatal("retention worker not found")
	}
	for _, want := range []string{
		"nodeSvc.RefreshTrafficDaily(sctx, middleware.DefaultTenantID)",
		"nodeSvc.PurgeTrafficReports(sctx, middleware.DefaultTenantID)",
		"subscription.PurgeFetchLog(sctx, pool, middleware.DefaultTenantID)",
	} {
		at := strings.Index(run, want)
		if at < 0 {
			t.Fatalf("admin gateway retention worker missing %q", want)
		}
		lo, hi := min(at, purge), max(at, purge)
		if strings.Contains(run[lo:hi], "go func()") {
			t.Fatalf("%q must run in the retention worker, not a new loop", want)
		}
		// 在本轮时限取消之前调用
		if cancelAt := strings.Index(run[at:], "cancel()"); cancelAt < 0 || strings.Contains(run[at:at+cancelAt], "go func()") {
			t.Fatalf("%q must run before the retention round's cancel()", want)
		}
	}
}
