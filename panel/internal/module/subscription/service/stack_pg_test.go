package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// provisionOneTime 开一份一次性订阅（days 为 nil 时永久）。
func provisionOneTime(t *testing.T, e *env, ctx context.Context, user, planID, versionID uuid.UUID, days *int) Subscription {
	t.Helper()
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: oneTime(days), Actor: Actor{Kind: ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通一次性: %v", err)
	}
	return sub
}

// stackOneTime 以一张叠加单把 versionID 叠到 sub 上。
func stackOneTime(t *testing.T, e *env, ctx context.Context, user uuid.UUID, sub Subscription, versionID uuid.UUID, days *int) RenewalResult {
	t.Helper()
	orderID := newOrder(t, e, ctx, user, "stack", &sub.ID)
	res, err := e.svc.Stack(ctx, Stacking{
		SubscriptionID: sub.ID,
		UserID:         user,
		PlanVersionID:  versionID,
		Price:          oneTime(days),
		PaidAt:         time.Now(),
		OrderID:        &orderID,
		Actor:          Actor{Kind: ActorSystem},
		Source:         "order",
	})
	if err != nil {
		t.Fatalf("叠加: %v", err)
	}
	return res
}

// assertPeriodEndNear 断言期末在容差内。
func assertPeriodEndNear(t *testing.T, got, want time.Time, tol time.Duration) {
	t.Helper()
	d := got.Sub(want)
	if d < -tol || d > tol {
		t.Fatalf("期末 %s，期望 %s（容差 %s）", got, want, tol)
	}
}

// TestStackProvisionOneTimePG 一次性开通：total 行、期末与天数、无 cycle 行。
func TestStackProvisionOneTimePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(100 * gib)})
	sub := provisionOneTime(t, e, ctx, user, planID, versionID, intp(30))

	wantEnd := sub.PeriodStart.AddDate(0, 0, 30).Truncate(time.Microsecond)
	assertPeriodEndNear(t, sub.PeriodEnd, wantEnd, time.Second)

	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
	if q.Limit == nil || *q.Limit != 100*gib || q.PeriodEnd != nil {
		t.Fatalf("total 行: %+v", q)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM quota_balances WHERE subscription_id = ? AND period = ?`, sub.ID, PeriodCycle); n != 0 {
		t.Fatalf("不应有 cycle 行: %d", n)
	}
}

// TestStackAddsQuotaAndExtendsEndPG 叠加后上限相加、已用与 bonus 不动、期末顺延。
func TestStackAddsQuotaAndExtendsEndPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(100 * gib)})
	sub := provisionOneTime(t, e, ctx, user, planID, versionID, intp(30))
	oldEnd := sub.PeriodEnd

	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal, 40*gib)
	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`,
		5*gib, sub.ID, MetricTraffic, PeriodTotal)

	res := stackOneTime(t, e, ctx, user, sub, versionID, intp(30))
	wantEnd := oldEnd.AddDate(0, 0, 30).Truncate(time.Microsecond)
	assertPeriodEndNear(t, res.PeriodEnd, wantEnd, time.Second)

	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
	if q.Limit == nil || *q.Limit != 200*gib || q.Consumed != 40*gib || q.Bonus != 5*gib {
		t.Fatalf("叠加后 total: %+v", q)
	}
}

// TestStackPermanentPG 永久的一次性叠加有天数的价，期末不变。
func TestStackPermanentPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(50 * gib)})
	sub := provisionOneTime(t, e, ctx, user, planID, versionID, nil)
	if !IsPermanent(sub.PeriodEnd) {
		t.Fatalf("永久开通期末应是哨兵: %s", sub.PeriodEnd)
	}
	permEnd := sub.PeriodEnd

	res := stackOneTime(t, e, ctx, user, sub, versionID, intp(30))
	if !res.PeriodEnd.Equal(permEnd) {
		t.Fatalf("永久叠加有天数价后期末不变: got %s want %s", res.PeriodEnd, permEnd)
	}
}

// TestStackErrorsPG 订阅套餐、非一次性价、别的套餐的版本都被拒。
func TestStackErrorsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)

	t.Run("订阅套餐不能叠加", func(t *testing.T) {
		planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
		sub := provision(t, e, ctx, user, planID, versionID, "")
		orderID := newOrder(t, e, ctx, user, "stack", &sub.ID)
		_, err := e.svc.Stack(ctx, Stacking{
			SubscriptionID: sub.ID, UserID: user, PlanVersionID: versionID,
			Price: oneTime(intp(30)), PaidAt: time.Now(), OrderID: &orderID,
			Actor: Actor{Kind: ActorSystem}, Source: "order",
		})
		if !errors.Is(err, ErrStackNotOneTime) {
			t.Fatalf("期望 ErrStackNotOneTime: %v", err)
		}
	})

	t.Run("价格须为 one_time", func(t *testing.T) {
		planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(10 * gib)})
		sub := provisionOneTime(t, e, ctx, user, planID, versionID, intp(7))
		orderID := newOrder(t, e, ctx, user, "stack", &sub.ID)
		_, err := e.svc.Stack(ctx, Stacking{
			SubscriptionID: sub.ID, UserID: user, PlanVersionID: versionID,
			Price: monthly(), PaidAt: time.Now(), OrderID: &orderID,
			Actor: Actor{Kind: ActorSystem}, Source: "order",
		})
		if !errors.Is(err, ErrStackNotOneTime) {
			t.Fatalf("期望 ErrStackNotOneTime: %v", err)
		}
	})

	t.Run("版本不属于该套餐", func(t *testing.T) {
		planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(10 * gib)})
		sub := provisionOneTime(t, e, ctx, user, planID, versionID, intp(7))
		_, otherVer := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(20 * gib)})
		orderID := newOrder(t, e, ctx, user, "stack", &sub.ID)
		_, err := e.svc.Stack(ctx, Stacking{
			SubscriptionID: sub.ID, UserID: user, PlanVersionID: otherVer,
			Price: oneTime(intp(30)), PaidAt: time.Now(), OrderID: &orderID,
			Actor: Actor{Kind: ActorSystem}, Source: "order",
		})
		if !errors.Is(err, ErrVersionUnavailable) {
			t.Fatalf("期望 ErrVersionUnavailable: %v", err)
		}
	})
}

// TestStackSwitchesVersionPG 叠加时切到新版本，上限为旧上限加新版本流量。
func TestStackSwitchesVersionPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, v1 := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(100 * gib)})
	sub := provisionOneTime(t, e, ctx, user, planID, v1, intp(30))
	v2 := newVersion(t, e, ctx, planID, 2, quotaSpec{MetricTraffic, PeriodTotal, i64(50 * gib)})

	stackOneTime(t, e, ctx, user, sub, v2, intp(30))

	got, err := e.svc.Get(ctx, sub.ID, &user)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanVersionID != v2 {
		t.Fatalf("版本应切到 v2: %s", got.PlanVersionID)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
	if q.Limit == nil || *q.Limit != 150*gib {
		t.Fatalf("上限 100+50 GiB: %+v", q)
	}
}
