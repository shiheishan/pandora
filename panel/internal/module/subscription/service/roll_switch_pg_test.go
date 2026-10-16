package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestRollSwitchPlanVersionPG 订阅套餐滚进新周期时切到套餐当前版本，并按新版本写 cycle 上限、清零用量与送的量。
func TestRollSwitchPlanVersionPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionV1 := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionV1, "")
	now := rollNow()

	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`,
		10*gib, sub.ID, MetricTraffic, PeriodCycle)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 50*gib)

	versionV2 := newVersion(t, e, ctx, planID, 2, quotaSpec{MetricTraffic, PeriodCycle, i64(200 * gib)})

	oldEnd := now.Add(-time.Hour)
	subEnd := oldEnd.AddDate(0, 1, 0)
	rollSetSub(t, e, ctx, sub.ID, StatusActive, oldEnd.AddDate(0, -1, 0), subEnd)
	rollSetQuota(t, e, ctx, sub.ID, PeriodCycle, oldEnd.AddDate(0, -1, 0), oldEnd)

	st, err := e.svc.RollQuotas(ctx)
	if err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}
	if st.Switched < 1 {
		t.Fatalf("应切版本，Switched=%d", st.Switched)
	}

	got, err := e.svc.Get(ctx, sub.ID, &user)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanVersionID != versionV2 {
		t.Fatalf("plan_version_id=%s want %s", got.PlanVersionID, versionV2)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Consumed != 0 || q.Bonus != 0 {
		t.Fatalf("用量与送的量应清零: %+v", q)
	}
	if q.Limit == nil || *q.Limit != 200*gib {
		t.Fatalf("cycle 上限应为 200 GiB，得 %v", q.Limit)
	}
}

// TestRollCycleClearsBonusNoVersionChangePG 套餐当前版本未变时滚动只清零 bonus，版本与上限不动。
func TestRollCycleClearsBonusNoVersionChangePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	now := rollNow()

	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`,
		10*gib, sub.ID, MetricTraffic, PeriodCycle)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 33*gib)

	oldEnd := now.Add(-time.Hour)
	subEnd := oldEnd.AddDate(0, 1, 0)
	rollSetSub(t, e, ctx, sub.ID, StatusActive, oldEnd.AddDate(0, -1, 0), subEnd)
	rollSetQuota(t, e, ctx, sub.ID, PeriodCycle, oldEnd.AddDate(0, -1, 0), oldEnd)

	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}

	got, err := e.svc.Get(ctx, sub.ID, &user)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanVersionID != versionID {
		t.Fatalf("版本不应变: %s", got.PlanVersionID)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Bonus != 0 || q.Consumed != 0 {
		t.Fatalf("应清零 bonus 与 consumed: %+v", q)
	}
	if q.Limit == nil || *q.Limit != 100*gib {
		t.Fatalf("上限应保持 100 GiB: %v", q.Limit)
	}
}

// TestRollOneTimeTotalUnchangedPG 一次性套餐的 total 行无 period_end，不参与滚动，版本不变。
func TestRollOneTimeTotalUnchangedPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(80 * gib)})
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: oneTime(intp(30)), Actor: Actor{Kind: ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通: %v", err)
	}
	before := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal, 12*gib)
	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`,
		5*gib, sub.ID, MetricTraffic, PeriodTotal)

	if _, err := e.svc.RollQuotas(ctx); err != nil {
		t.Fatalf("RollQuotas: %v", err)
	}

	got, err := e.svc.Get(ctx, sub.ID, &user)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanVersionID != versionID {
		t.Fatalf("版本不应变: %s", got.PlanVersionID)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
	if before.PeriodEnd != nil || q.PeriodEnd != nil {
		t.Fatalf("total 行不应有 period_end: before=%v after=%v", before.PeriodEnd, q.PeriodEnd)
	}
	if q.Consumed != 12*gib || q.Bonus != 5*gib {
		t.Fatalf("total 行不应被滚动改动: %+v", q)
	}
	if q.Limit == nil || *q.Limit != 80*gib {
		t.Fatalf("上限不变: %v", q.Limit)
	}
}
