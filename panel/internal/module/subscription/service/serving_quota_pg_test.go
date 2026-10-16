package service

import (
	"testing"

	"github.com/google/uuid"
)

// 一个指标可以有多个周期（total + day、cycle + month）：任一限量行用满就不可服务，
// 拉取展示取余量最小的那行。防的是只看 period_end 最晚的一行：那样总量用满后会被 day 行放行。
func TestServingMultiPeriodQuotaPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	// total 只属于一次性套餐（订阅套餐的流量行按类型落成 cycle）
	planTD, verTD := newOneTimePlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodTotal, i64(100 * gib)}, quotaSpec{MetricTraffic, PeriodDay, i64(10 * gib)})
	planCM, verCM := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)}, quotaSpec{MetricTraffic, PeriodMonth, i64(50 * gib)})

	// total 用满、day 还有余量 → 不可服务
	subTotalOut := provisionOneTime(t, e, ctx, newUser(t, e, ctx), planTD, verTD, nil)
	setConsumed(t, e, ctx, subTotalOut.ID, MetricTraffic, PeriodTotal, 100*gib)
	setConsumed(t, e, ctx, subTotalOut.ID, MetricTraffic, PeriodDay, 1*gib)

	// cycle 有余量、month 用满 → 不可服务
	subMonthOut := provision(t, e, ctx, newUser(t, e, ctx), planCM, verCM, "月量用满")
	setConsumed(t, e, ctx, subMonthOut.ID, MetricTraffic, PeriodCycle, 60*gib)
	setConsumed(t, e, ctx, subMonthOut.ID, MetricTraffic, PeriodMonth, 50*gib)

	// 两行都有余量 → 可服务
	subBothLeft := provisionOneTime(t, e, ctx, newUser(t, e, ctx), planTD, verTD, nil)
	setConsumed(t, e, ctx, subBothLeft.ID, MetricTraffic, PeriodTotal, 95*gib)
	setConsumed(t, e, ctx, subBothLeft.ID, MetricTraffic, PeriodDay, 2*gib)

	// total 用满但送的流量有余量 → 可服务
	uBonus := newUser(t, e, ctx)
	subBonus := provisionOneTime(t, e, ctx, uBonus, planTD, verTD, nil)
	setConsumed(t, e, ctx, subBonus.ID, MetricTraffic, PeriodTotal, 100*gib)
	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`,
		100, subBonus.ID, MetricTraffic, PeriodTotal)

	list, err := e.svc.ServingSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := servingByID(list)
	for id, want := range map[uuid.UUID]bool{
		subTotalOut.ID: false, subMonthOut.ID: false, subBothLeft.ID: true, subBonus.ID: true,
	} {
		if _, ok := byID[id]; ok != want {
			t.Fatalf("订阅 %s 应可服务=%v 实际=%v", id, want, ok)
		}
	}

	// 拉取展示取最紧的一行：都有余量那份 total 剩 5GiB、day 剩 8GiB → 显示 total
	var hash []byte
	if err := e.row(ctx, `SELECT token_hash FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`,
		subBothLeft.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	row, found, err := e.svc.repo.PullByTokenHash(ctx, hash, e.svc.clock())
	if err != nil || !found {
		t.Fatalf("按令牌哈希读: found=%v err=%v", found, err)
	}
	if row.TrafficLimit == nil || *row.TrafficLimit != 100*gib || row.TrafficConsumed == nil || *row.TrafficConsumed != 95*gib {
		t.Fatalf("应取余量最小的 total 行，得 limit=%v consumed=%v", row.TrafficLimit, row.TrafficConsumed)
	}
}
