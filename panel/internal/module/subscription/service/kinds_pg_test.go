package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
)

// TestResetsUsedCountingPG 测试 ResetsUsed 的计数规则：
// 初始为 0；同一订单的多条重置日志去重后算 1 次；不同订单计入；周期起点移到日志之后重置为 0。
func TestResetsUsedCountingPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")

	// 1. 开通后为 0
	used, err := e.svc.ResetsUsed(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ResetsUsed: %v", err)
	}
	if used != 0 {
		t.Fatalf("初始已用次数应为 0，得 %d", used)
	}

	// 2. 插入 reason = 'order' 的重置日志：
	// 订单 1 有两条（模拟清多行或重复记录），订单 2 有一条
	order1 := "ORDER-1001"
	order2 := "ORDER-1002"
	e.exec(t, ctx, `INSERT INTO traffic_reset_logs (subscription_id, user_id, metric, reason, consumed_before, consumed_after, note, created_at)
		VALUES (?, ?, ?, 'order', 10, 0, ?, clock_timestamp())`, sub.ID, user, MetricTraffic, order1)
	e.exec(t, ctx, `INSERT INTO traffic_reset_logs (subscription_id, user_id, metric, reason, consumed_before, consumed_after, note, created_at)
		VALUES (?, ?, ?, 'order', 20, 0, ?, clock_timestamp())`, sub.ID, user, MetricTraffic, order1)
	e.exec(t, ctx, `INSERT INTO traffic_reset_logs (subscription_id, user_id, metric, reason, consumed_before, consumed_after, note, created_at)
		VALUES (?, ?, ?, 'order', 30, 0, ?, clock_timestamp())`, sub.ID, user, MetricTraffic, order2)

	used, err = e.svc.ResetsUsed(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ResetsUsed: %v", err)
	}
	if used != 2 {
		t.Fatalf("应去重计为 2 次，得 %d", used)
	}

	// 3. 把 cycle 行 period_start 改到这些日志之后 -> 0
	e.exec(t, ctx, `UPDATE quota_balances SET period_start = now() + interval '1 minute'
		WHERE subscription_id = ? AND metric = ? AND period = ?`, sub.ID, MetricTraffic, PeriodCycle)

	used, err = e.svc.ResetsUsed(ctx, sub.ID)
	if err != nil {
		t.Fatalf("ResetsUsed: %v", err)
	}
	if used != 0 {
		t.Fatalf("周期起点移到日志之后应为 0，得 %d", used)
	}
}

// TestRenewOneTimeForbiddenPG 测试一次性套餐禁止续费，返回 ErrRenewOneTime。
func TestRenewOneTimeForbiddenPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(50 * gib)})
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: oneTime(intp(30)), Actor: Actor{Kind: ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通: %v", err)
	}

	paid := time.Now().UTC().Truncate(time.Microsecond)
	_, err = e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: oneTime(intp(30)), PaidAt: paid,
		Actor: Actor{Kind: ActorPayment}, Source: "order",
	})
	if !errors.Is(err, ErrRenewOneTime) {
		t.Fatalf("一次性套餐续费应回 ErrRenewOneTime，得: %v", err)
	}
}

// TestRenewRestartSwitchVersionPG 订阅套餐过期恢复时当场切到当前版本，并重建配额、清空 bonus 与 consumed。
func TestRenewRestartSwitchVersionPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionV1 := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionV1, "")

	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`,
		10*gib, sub.ID, MetricTraffic, PeriodCycle)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 40*gib)

	versionV2 := newVersion(t, e, ctx, planID, 2, quotaSpec{MetricTraffic, PeriodCycle, i64(200 * gib)})

	lapse(t, e, ctx, sub.ID, 2)

	paid := time.Now().UTC().Truncate(time.Microsecond)
	res, err := e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: paid, OrderCreatedAt: &paid,
		Actor: Actor{Kind: ActorPayment}, Source: "order",
	})
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !res.Restarted {
		t.Fatal("应为过期恢复 (Restarted=true)")
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
		t.Fatalf("已用量和 bonus 应清零: %+v", q)
	}
	if q.Limit == nil || *q.Limit != 200*gib {
		t.Fatalf("上限按新版本应为 200 GiB，得 %v", q.Limit)
	}
}

// TestChangePlanAcrossKindsPG 跨类型换套餐：
// 1. 订阅套餐 -> 一次性套餐：建 total 行，原 cycle 行上限置空（不限），期末按新价。
// 2. 一次性套餐 -> 订阅套餐：cycle 行补上，total 行上限置空。
func TestChangePlanAcrossKindsPG(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
	}{
		{name: "订阅转一次性", from: catalogsvc.KindSubscription, to: catalogsvc.KindOneTime},
		{name: "一次性转订阅", from: catalogsvc.KindOneTime, to: catalogsvc.KindSubscription},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := e.txCtx()
			user := newUser(t, e, ctx)

			var sub Subscription
			var targetPlanID, targetVersionID uuid.UUID
			var targetPrice PriceSnapshot

			if tc.from == catalogsvc.KindSubscription {
				// 初始订阅套餐
				pID, vID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
				sub = provision(t, e, ctx, user, pID, vID, "")
				setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 15*gib)

				// 目标一次性套餐
				targetPlanID, targetVersionID = newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(50 * gib)})
				targetPrice = oneTime(intp(10))
			} else {
				// 初始一次性套餐
				pID, vID := newOneTimePlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodTotal, i64(60 * gib)})
				var err error
				sub, err = e.svc.Provision(ctx, Provision{
					UserID: user, PlanID: pID, PlanVersionID: vID,
					Price: oneTime(intp(30)), Actor: Actor{Kind: ActorSystem},
				})
				if err != nil {
					t.Fatalf("开通: %v", err)
				}
				setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal, 20*gib)

				// 目标订阅套餐
				targetPlanID, targetVersionID = newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(120 * gib)})
				targetPrice = monthly()
			}

			orderID := newOrder(t, e, ctx, user, "upgrade", &sub.ID)
			res, err := e.svc.ChangePlan(ctx, PlanChange{
				SubscriptionID: sub.ID, UserID: user, PlanID: targetPlanID, PlanVersionID: targetVersionID,
				Price: targetPrice, OrderID: &orderID, Actor: Actor{Kind: ActorPayment},
			})
			if err != nil {
				t.Fatalf("ChangePlan: %v", err)
			}

			got, err := e.svc.Get(ctx, sub.ID, &user)
			if err != nil {
				t.Fatal(err)
			}
			if got.PlanID != targetPlanID || got.PlanVersionID != targetVersionID {
				t.Fatalf("套餐/版本未切: plan=%s version=%s", got.PlanID, got.PlanVersionID)
			}
			if !got.PeriodEnd.Equal(res.PeriodEnd) {
				t.Fatalf("期末不一致: got=%s res=%s", got.PeriodEnd, res.PeriodEnd)
			}

			if tc.from == catalogsvc.KindSubscription {
				// 订阅转一次性:
				// 有 total 行且 limit = 50 GiB
				total := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
				if total.Limit == nil || *total.Limit != 50*gib || total.Consumed != 0 {
					t.Fatalf("total 行应补上且上限 50 GiB 已用 0，得: %+v", total)
				}
				// 原 cycle 行删掉（留着上限为空的行会被显示成不限）
				assertNoQuota(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
				// 有效期大约 10 天
				diff := res.PeriodEnd.Sub(got.PeriodStart)
				if diff < 9*24*time.Hour || diff > 11*24*time.Hour {
					t.Fatalf("一次性套餐期末偏离 10 天: %v", diff)
				}
			} else {
				// 一次性转订阅:
				// cycle 行补上且 limit = 120 GiB
				cycle := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
				if cycle.Limit == nil || *cycle.Limit != 120*gib || cycle.Consumed != 0 {
					t.Fatalf("cycle 行应补上且上限 120 GiB 已用 0，得: %+v", cycle)
				}
				// 原 total 行删掉
				assertNoQuota(t, e, ctx, sub.ID, MetricTraffic, PeriodTotal)
			}
		})
	}
}
