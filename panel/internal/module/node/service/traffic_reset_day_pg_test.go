package service

// 设或改重置日不清零已用（用户 2026-10-14 定）：traffic_reset_at 推到 max(原值, 按新重置日算的本周期起点)，
// 定时任务只在下一个重置日清零。单个 PATCH 与批量修改走同一段 patchNodeTx。

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

const resetDayGiB = int64(1) << 30

func resetDayIntPtr(v int) *int       { return &v }
func resetDayInt64Ptr(v int64) *int64 { return &v }

// resetDaySetUsage 用 SQL 把节点的已用与周期起点改成给定值（不限流量，免撞超限形状约束）。
func resetDaySetUsage(t *testing.T, e *invEnv, ctx context.Context, id uuid.UUID, used int64, resetAt time.Time) {
	t.Helper()
	if err := e.exec(ctx, `UPDATE nodes SET traffic_limit_bytes = NULL, traffic_exceeded_at = NULL, traffic_reset_day = NULL,
		traffic_used_bytes = ?, traffic_reset_at = ? WHERE id = ?`, used, resetAt, id); err != nil {
		t.Fatalf("设用量: %v", err)
	}
}

func resetDayPatch(t *testing.T, e *invEnv, ctx context.Context, id uuid.UUID, mutate func(*NodePatch)) AdminNode {
	t.Helper()
	cur, err := e.svc.GetAdminNode(ctx, id)
	if err != nil {
		t.Fatalf("GetAdminNode: %v", err)
	}
	in := NodePatch{RowVersion: cur.RowVersion}
	mutate(&in)
	got, err := e.svc.PatchNode(ctx, id, in)
	if err != nil {
		t.Fatalf("PatchNode: %v", err)
	}
	return got
}

func resetDayUsed(t *testing.T, e *invEnv, ctx context.Context, id uuid.UUID) int64 {
	t.Helper()
	var used int64
	if err := e.row(ctx, `SELECT traffic_used_bytes FROM nodes WHERE id = ?`, id).Scan(&used); err != nil {
		t.Fatalf("读已用: %v", err)
	}
	return used
}

// 例：节点 9-01 上线，10-14 已用 600 GB，此时设上限 1 TB、重置日 20 → 600 GB 照算，10-20 才清零。
func TestSetResetDayKeepsUsagePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "rd-srv1")
	n := e.ssNode(t, ctx, srv.ID, "rd-n1", 23001)

	resetDaySetUsage(t, e, ctx, n.ID, 600*resetDayGiB, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	e.svc.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }

	got := resetDayPatch(t, e, ctx, n.ID, func(p *NodePatch) {
		p.TrafficLimitBytes = OptionalInt64{Set: true, Value: resetDayInt64Ptr(1 << 40)}
		p.TrafficResetDay = OptionalInt{Set: true, Value: resetDayIntPtr(20)}
	})
	if got.Traffic.UsedBytes != 600*resetDayGiB {
		t.Fatalf("已用 = %d want 600 GiB", got.Traffic.UsedBytes)
	}
	if want := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC); !got.Traffic.ResetAt.Equal(want) {
		t.Fatalf("traffic_reset_at = %v want %v", got.Traffic.ResetAt, want)
	}
	if got.Traffic.ExceededAt != nil {
		t.Fatalf("600 GiB < 1 TiB 不应超限")
	}

	// 当天定时任务不清零
	if _, err := e.svc.ResetDueNodeTraffic(ctx); err != nil {
		t.Fatalf("ResetDueNodeTraffic: %v", err)
	}
	if used := resetDayUsed(t, e, ctx, n.ID); used != 600*resetDayGiB {
		t.Fatalf("设重置日当天被清零: used=%d", used)
	}

	// 只调低上限、重置日不变：超限按锁住的行照常重算，周期起点不动
	got = resetDayPatch(t, e, ctx, n.ID, func(p *NodePatch) {
		p.TrafficLimitBytes = OptionalInt64{Set: true, Value: resetDayInt64Ptr(500 * resetDayGiB)}
	})
	if got.Traffic.ExceededAt == nil {
		t.Fatalf("600 GiB >= 500 GiB 应超限")
	}
	if want := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC); !got.Traffic.ResetAt.Equal(want) {
		t.Fatalf("只改上限不应动 traffic_reset_at: %v", got.Traffic.ResetAt)
	}

	// 10-20 0 点后定时任务清零
	e.svc.now = func() time.Time { return time.Date(2026, 10, 20, 0, 0, 1, 0, time.UTC) }
	if _, err := e.svc.ResetDueNodeTraffic(ctx); err != nil {
		t.Fatalf("ResetDueNodeTraffic: %v", err)
	}
	if used := resetDayUsed(t, e, ctx, n.ID); used != 0 {
		t.Fatalf("10-20 应清零: used=%d", used)
	}
}

// 短月：2-14 设重置日 31 → 本周期起点是 1-31（2 月没有 31 日，2-28 还没到），2-28 0 点才清零。
// 同一天重复设不推周期起点（免得顶掉已到期、待任务清零的周期）。
func TestSetResetDayShortMonthPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "rd-srv2")
	n := e.ssNode(t, ctx, srv.ID, "rd-n2", 23002)

	resetDaySetUsage(t, e, ctx, n.ID, 10*resetDayGiB, time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC))
	e.svc.now = func() time.Time { return time.Date(2027, 2, 14, 12, 0, 0, 0, time.UTC) }

	got := resetDayPatch(t, e, ctx, n.ID, func(p *NodePatch) {
		p.TrafficResetDay = OptionalInt{Set: true, Value: resetDayIntPtr(31)}
	})
	if want := time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC); !got.Traffic.ResetAt.Equal(want) {
		t.Fatalf("traffic_reset_at = %v want %v", got.Traffic.ResetAt, want)
	}
	if got.Traffic.UsedBytes != 10*resetDayGiB {
		t.Fatalf("已用 = %d want 10 GiB", got.Traffic.UsedBytes)
	}

	// 同一天重复设：周期起点不动
	if err := e.exec(ctx, `UPDATE nodes SET traffic_reset_at = ? WHERE id = ?`, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), n.ID); err != nil {
		t.Fatal(err)
	}
	got = resetDayPatch(t, e, ctx, n.ID, func(p *NodePatch) {
		p.TrafficResetDay = OptionalInt{Set: true, Value: resetDayIntPtr(31)}
	})
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !got.Traffic.ResetAt.Equal(want) {
		t.Fatalf("重置日没变不应动 traffic_reset_at: %v", got.Traffic.ResetAt)
	}
	if err := e.exec(ctx, `UPDATE nodes SET traffic_reset_at = ? WHERE id = ?`, time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC), n.ID); err != nil {
		t.Fatal(err)
	}

	// 2-27 不清零，2-28 0 点后清零
	e.svc.now = func() time.Time { return time.Date(2027, 2, 27, 23, 59, 0, 0, time.UTC) }
	if _, err := e.svc.ResetDueNodeTraffic(ctx); err != nil {
		t.Fatalf("ResetDueNodeTraffic: %v", err)
	}
	if used := resetDayUsed(t, e, ctx, n.ID); used != 10*resetDayGiB {
		t.Fatalf("2-27 不应清零: used=%d", used)
	}
	e.svc.now = func() time.Time { return time.Date(2027, 2, 28, 0, 0, 1, 0, time.UTC) }
	if _, err := e.svc.ResetDueNodeTraffic(ctx); err != nil {
		t.Fatalf("ResetDueNodeTraffic: %v", err)
	}
	if used := resetDayUsed(t, e, ctx, n.ID); used != 0 {
		t.Fatalf("2-28 应清零: used=%d", used)
	}
}

// 批量修改设重置日走同一逻辑。
func TestBatchSetResetDayKeepsUsagePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "rd-srv3")
	n1 := e.ssNode(t, ctx, srv.ID, "rd-n3", 23003)
	n2 := e.ssNode(t, ctx, srv.ID, "rd-n4", 23004)

	for _, id := range []uuid.UUID{n1.ID, n2.ID} {
		resetDaySetUsage(t, e, ctx, id, 600*resetDayGiB, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	}
	e.svc.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }

	refs := make([]NodeVersionRef, 0, 2)
	for _, id := range []uuid.UUID{n1.ID, n2.ID} {
		cur, err := e.svc.GetAdminNode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, NodeVersionRef{ID: id, RowVersion: cur.RowVersion})
	}
	if _, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items: refs,
		Set: NodeBatchFields{
			TrafficLimitBytes: OptionalInt64{Set: true, Value: resetDayInt64Ptr(1 << 40)},
			TrafficResetDay:   OptionalInt{Set: true, Value: resetDayIntPtr(20)},
		},
		Reason: "批量设重置日",
	}); err != nil {
		t.Fatalf("BatchEditNodes: %v", err)
	}
	for _, id := range []uuid.UUID{n1.ID, n2.ID} {
		got, err := e.svc.GetAdminNode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Traffic.UsedBytes != 600*resetDayGiB {
			t.Errorf("%s 已用 = %d want 600 GiB", id, got.Traffic.UsedBytes)
		}
		if want := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC); !got.Traffic.ResetAt.Equal(want) {
			t.Errorf("%s traffic_reset_at = %v want %v", id, got.Traffic.ResetAt, want)
		}
	}
}
