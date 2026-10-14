package service

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestNodeUsageAccumulatesAndNoLimitPG 验证：
// 两份上报后 traffic_used_bytes 等于两份实际字节之和（含名单外条目），traffic_exceeded_at 仍为 NULL（没设上限）。
func TestNodeUsageAccumulatesAndNoLimitPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)

	// 第一份上报：含名单内条目 (100+200) 与名单外条目 uidB (10+20)
	r1 := TrafficReport{ReportID: "u-1", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 100, Download: 200},
		{NodeID: f.n1, UID: f.uidB, Upload: 10, Download: 20},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r1, f.members); err != nil {
		t.Fatalf("r1: %v", err)
	}

	// 第二份上报：200+300
	r2 := TrafficReport{ReportID: "u-2", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 200, Download: 300},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r2, f.members); err != nil {
		t.Fatalf("r2: %v", err)
	}

	var usedBytes int64
	var exceededAt sql.NullTime
	if err := e.row(ctx, `SELECT traffic_used_bytes, traffic_exceeded_at FROM nodes WHERE id = ?`, f.n1).
		Scan(&usedBytes, &exceededAt); err != nil {
		t.Fatalf("read node: %v", err)
	}
	expected := int64(300 + 30 + 500)
	if usedBytes != expected {
		t.Fatalf("usedBytes = %d, expected %d", usedBytes, expected)
	}
	if exceededAt.Valid {
		t.Fatalf("traffic_exceeded_at 应当为 NULL，实际得到 %v", exceededAt.Time)
	}
}

// TestNodeUsageLimitAndExceededPG 验证：
// 节点设 traffic_limit_bytes = 1000，一份上报 600 字节后未超限；再一份 500 字节后 traffic_exceeded_at 非空；
// 第三份不改变超限时刻（用 SQL 读出两次比较相等）。
func TestNodeUsageLimitAndExceededPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)

	e.mustExec(t, ctx, `UPDATE nodes SET traffic_limit_bytes = 1000, traffic_used_bytes = 0, traffic_exceeded_at = NULL WHERE id = ?`, f.n1)

	// 第一份：600 字节
	r1 := TrafficReport{ReportID: "lim-1", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 200, Download: 400},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r1, f.members); err != nil {
		t.Fatalf("r1: %v", err)
	}

	var used1 int64
	var exceeded1 sql.NullTime
	if err := e.row(ctx, `SELECT traffic_used_bytes, traffic_exceeded_at FROM nodes WHERE id = ?`, f.n1).
		Scan(&used1, &exceeded1); err != nil {
		t.Fatalf("read node after r1: %v", err)
	}
	if used1 != 600 {
		t.Fatalf("used1 = %d, expected 600", used1)
	}
	if exceeded1.Valid {
		t.Fatalf("未超限时不应有 exceeded_at: %v", exceeded1.Time)
	}

	// 第二份：500 字节（累计 1100 >= 1000）
	r2 := TrafficReport{ReportID: "lim-2", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 250, Download: 250},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r2, f.members); err != nil {
		t.Fatalf("r2: %v", err)
	}

	var used2 int64
	var exceeded2 sql.NullTime
	if err := e.row(ctx, `SELECT traffic_used_bytes, traffic_exceeded_at FROM nodes WHERE id = ?`, f.n1).
		Scan(&used2, &exceeded2); err != nil {
		t.Fatalf("read node after r2: %v", err)
	}
	if used2 != 1100 {
		t.Fatalf("used2 = %d, expected 1100", used2)
	}
	if !exceeded2.Valid {
		t.Fatal("超限后 traffic_exceeded_at 应非空")
	}

	// 第三份：100 字节（累计 1200），超限时刻不变
	r3 := TrafficReport{ReportID: "lim-3", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 50, Download: 50},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r3, f.members); err != nil {
		t.Fatalf("r3: %v", err)
	}

	var used3 int64
	var exceeded3 sql.NullTime
	if err := e.row(ctx, `SELECT traffic_used_bytes, traffic_exceeded_at FROM nodes WHERE id = ?`, f.n1).
		Scan(&used3, &exceeded3); err != nil {
		t.Fatalf("read node after r3: %v", err)
	}
	if used3 != 1200 {
		t.Fatalf("used3 = %d, expected 1200", used3)
	}
	if !exceeded3.Valid {
		t.Fatal("第三份后 traffic_exceeded_at 仍应非空")
	}
	if !exceeded2.Time.Equal(exceeded3.Time) {
		t.Fatalf("超限时刻发生改变：exceeded2=%v, exceeded3=%v", exceeded2.Time, exceeded3.Time)
	}
}

// TestNodeRateScheduleEffectivePG 验证时段倍率：
// UPDATE nodes SET traffic_rate = 1, traffic_rate_schedule = '[{"start":"00:00","end":"24:00","rate":"2"}]'，
// 上报 100 字节 → fakeCharger 收到的 Bytes 是 200；
// schedule 换成一个不覆盖当前时刻的时段（用 e.svc.now() 的 UTC 小时算一个避开它的区间）→ 收到 100。
func TestNodeRateScheduleEffectivePG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)

	// 全天倍率 2
	e.mustExec(t, ctx, `UPDATE nodes SET traffic_rate = 1, traffic_rate_schedule = '[{"start":"00:00","end":"24:00","rate":"2"}]' WHERE id = ?`, f.n1)

	r1 := TrafficReport{ReportID: "sched-1", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 40, Download: 60},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r1, f.members); err != nil {
		t.Fatalf("r1: %v", err)
	}
	if e.charger.callCount() != 1 {
		t.Fatalf("扣量次数应为 1，得 %d", e.charger.callCount())
	}
	var billed1 int64
	for _, c := range e.charger.calls[0] {
		if c.UID == f.uidA {
			billed1 += c.Bytes
		}
	}
	if billed1 != 200 {
		t.Fatalf("全天 2 倍率应扣 200 字节，得 %d", billed1)
	}

	// 计算一个避开 e.svc.now() 的时段
	curHour := e.svc.now().UTC().Hour()
	missStart := (curHour + 5) % 24
	missEnd := (curHour + 6) % 24
	if missStart > missEnd {
		missStart, missEnd = 0, 1
		if curHour == 0 {
			missStart, missEnd = 2, 3
		}
	}
	schedMiss := fmt.Sprintf(`[{"start":"%02d:00","end":"%02d:00","rate":"3"}]`, missStart, missEnd)
	e.mustExec(t, ctx, `UPDATE nodes SET traffic_rate = 1, traffic_rate_schedule = ? WHERE id = ?`, schedMiss, f.n1)

	r2 := TrafficReport{ReportID: "sched-2", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 40, Download: 60},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r2, f.members); err != nil {
		t.Fatalf("r2: %v", err)
	}
	if e.charger.callCount() != 2 {
		t.Fatalf("扣量次数应为 2，得 %d", e.charger.callCount())
	}
	var billed2 int64
	for _, c := range e.charger.calls[1] {
		if c.UID == f.uidA {
			billed2 += c.Bytes
		}
	}
	if billed2 != 100 {
		t.Fatalf("未命中时段倍率应按基础倍率 1 扣 100 字节，得 %d", billed2)
	}
}

// TestNodeRateScheduleBadJSONFallbackPG 验证：
// 坏的 schedule 文本（如 '[{"start":"x","end":"y","rate":"1"}]'）入账不报错，按基础倍率计。
func TestNodeRateScheduleBadJSONFallbackPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)

	e.mustExec(t, ctx, `UPDATE nodes SET traffic_rate = 1, traffic_rate_schedule = '[{"start":"x","end":"y","rate":"1"}]' WHERE id = ?`, f.n1)

	r := TrafficReport{ReportID: "bad-sched-1", Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 30, Download: 70},
	}}
	if err := e.svc.IngestTraffic(ctx, f.server, r, f.members); err != nil {
		t.Fatalf("坏的 schedule 入账不应报错: %v", err)
	}
	if e.charger.callCount() != 1 {
		t.Fatalf("扣量次数应为 1，得 %d", e.charger.callCount())
	}
	var billed int64
	for _, c := range e.charger.calls[0] {
		if c.UID == f.uidA {
			billed += c.Bytes
		}
	}
	if billed != 100 {
		t.Fatalf("解析失败应回退到基础倍率 1（扣 100 字节），得 %d", billed)
	}
}
