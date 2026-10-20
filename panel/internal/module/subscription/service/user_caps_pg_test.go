package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestLiveSubscriptionCounts(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	now := e.svc.clock()

	userA := newUser(t, e, ctx)
	userB := newUser(t, e, ctx)
	userC := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})

	provision(t, e, ctx, userA, planID, versionID, "")
	provision(t, e, ctx, userA, planID, versionID, "第二份")
	expired := provision(t, e, ctx, userA, planID, versionID, "已过期")
	lapse(t, e, ctx, expired.ID, 30)

	subB := provision(t, e, ctx, userB, planID, versionID, "")
	// status 还停在 active、但已走过到期日（过期扫描没赶上）：不算生效（关窗要求 status = expired，由 A 的过期那份覆盖）
	e.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '2 days', current_period_end = now() - interval '1 day' WHERE id = ?`, subB.ID)

	got, err := e.svc.LiveSubscriptionCounts(ctx, []uuid.UUID{userA, userB, userC}, now)
	if err != nil {
		t.Fatalf("批量读: %v", err)
	}
	if got[userA] != 2 {
		t.Fatalf("用户 A 应有 2 份生效订阅，得到 %d，map=%v", got[userA], got)
	}
	if _, ok := got[userB]; ok {
		t.Fatalf("用户 B 已走过到期日不应计入，map=%v", got)
	}
	if _, ok := got[userC]; ok {
		t.Fatalf("用户 C 无订阅不应在 map 里，map=%v", got)
	}

	for _, ids := range [][]uuid.UUID{nil, {}} {
		m, err := e.svc.LiveSubscriptionCounts(ctx, ids, now)
		if err != nil {
			t.Fatalf("空 userIDs: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("空 userIDs 应回空 map，得到 %v", m)
		}
	}
}
