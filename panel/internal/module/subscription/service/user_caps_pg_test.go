package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestHeldCountsByUser(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	userA := newUser(t, e, ctx)
	userB := newUser(t, e, ctx)
	userC := newUser(t, e, ctx)
	userD := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})

	// A：两份生效 + 一份 3 天前过期（能续回，算）+ 一份关了窗（不算）
	provision(t, e, ctx, userA, planID, versionID, "")
	provision(t, e, ctx, userA, planID, versionID, "第二份")
	revivable := provision(t, e, ctx, userA, planID, versionID, "能续回")
	lapse(t, e, ctx, revivable.ID, 3)
	ended := provision(t, e, ctx, userA, planID, versionID, "已停用")
	endSub(t, e, ctx, ended.ID)

	// B：status 还停在 active、但已走过到期日（过期扫描没赶上）：StateRevivable，算
	subB := provision(t, e, ctx, userB, planID, versionID, "")
	e.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '2 days', current_period_end = now() - interval '1 day' WHERE id = ?`, subB.ID)

	// C：只有一份关了窗的：不在结果里
	endedC := provision(t, e, ctx, userC, planID, versionID, "")
	endSub(t, e, ctx, endedC.ID)

	got, err := e.svc.HeldCountsByUser(ctx, []uuid.UUID{userA, userB, userC, userD})
	if err != nil {
		t.Fatalf("批量读: %v", err)
	}
	if got[userA] != 3 {
		t.Fatalf("用户 A 应算 3 份（2 生效 + 1 能续回），得到 %d，map=%v", got[userA], got)
	}
	if got[userB] != 1 {
		t.Fatalf("用户 B 走过到期日但没关窗，应算 1 份，map=%v", got)
	}
	if _, ok := got[userC]; ok {
		t.Fatalf("用户 C 只有关了窗的订阅，不应计入，map=%v", got)
	}
	if _, ok := got[userD]; ok {
		t.Fatalf("用户 D 无订阅不应在 map 里，map=%v", got)
	}

	for _, ids := range [][]uuid.UUID{nil, {}} {
		m, err := e.svc.HeldCountsByUser(ctx, ids)
		if err != nil {
			t.Fatalf("空 userIDs: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("空 userIDs 应回空 map，得到 %v", m)
		}
	}
}
