package main

import (
	"strings"
	"testing"
	"time"

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

// 保留期清理一小时一次（w12period）：清理挂在 intervalGate 后面，按天汇总留在每个 10 分钟节拍上。
// 断了不会报错：闸门被去掉，清理就回到每 10 分钟空转一次；汇总被挪进闸门，日界后要等一小时才有数。
func TestAdminRetentionPurgesRunHourlyBehindTheGate(t *testing.T) {
	if retentionPurgeEvery != time.Hour || retentionTick != 10*time.Minute {
		t.Fatalf("retention cadence = purge every %v on %v ticks, want hourly purges on 10-minute ticks",
			retentionPurgeEvery, retentionTick)
	}
	run := sourcetest.Load(t, ".").Decl("run")
	gate := strings.Index(run, "purgeDue := purgeGate.due(time.Now())")
	if gate < 0 || !strings.Contains(run, "purgeGate := newIntervalGate(retentionPurgeEvery, retentionTick)") {
		t.Fatal("retention worker lost its hourly purge gate")
	}
	for _, daily := range []string{
		"opsSvc.RefreshActivityDaily(sctx, middleware.DefaultTenantID)",
		"nodeSvc.RefreshTrafficDaily(sctx, middleware.DefaultTenantID)",
	} {
		if at := strings.Index(run, daily); at < 0 || at > gate {
			t.Fatalf("%q must run on every tick, before the purge gate", daily)
		}
	}
	gated := run[gate:]
	for _, purge := range []string{
		"nodeSvc.PurgeStaleAlive(sctx,", "nodeSvc.PurgeMetrics(sctx,", "nodeSvc.PurgeTrafficRollups(sctx,",
		"opsSvc.PurgeActivityDaily(sctx,", "nodeSvc.PurgeTrafficReports(sctx,", "subscription.PurgeFetchLog(sctx,",
	} {
		if at := strings.Index(gated, purge); at < 0 || !strings.Contains(gated[:at], "if purgeDue {") {
			t.Fatalf("%q must run inside the hourly purge gate", purge)
		}
	}
	// 失败的一轮不记到点时刻，下一拍重试
	if !strings.Contains(run, "if purge.ok() {") {
		t.Fatal("a failed purge round must not close the gate")
	}
}

// 批量生成账号任务由进程内唤醒驱动（w12period），不再每 3 秒轮询。
func TestAdminUserGenerationLoopIsWokenInProcess(t *testing.T) {
	run := sourcetest.Load(t, ".").Decl("run")
	for _, want := range []string{
		"wake := opsSvc.UserGenerationWake()",
		"newLoopPacer(adminops.UserGenerationPollEvery)",
		"case <-wake:",
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("user generation worker lost %q", want)
		}
	}
	if strings.Contains(run, "newLoopPacer(3 * time.Second)") {
		t.Fatal("user generation worker went back to polling every 3 seconds")
	}
}
