package service

// 门户按日用量的 PG 集成测试（测试站点时区是 UTC）。

import (
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestMyUsageCycle(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")

	today := dayOf(time.Now(), time.UTC)
	start := today.AddDate(0, 0, -2)
	de.exec(t, ctx, `UPDATE quota_balances SET period_start = ?, period_end = ?
		WHERE subscription_id = ? AND metric = ? AND period = ?`, start, today.AddDate(0, 0, 28), sub.ID, MetricTraffic, PeriodCycle)
	de.exec(t, ctx, `INSERT INTO subscription_usage_daily (subscription_id, day, bytes) VALUES (?, ?::date, 300), (?, ?::date, 100)`,
		sub.ID, today.Format(time.DateOnly), sub.ID, start.Format(time.DateOnly))

	u, err := de.svc.MyUsage(userCtx(ctx, user), sub.ID, 0)
	if err != nil {
		t.Fatalf("按日用量: %v", err)
	}
	if u.Timezone != "UTC" || len(u.Days) != 3 || u.PeriodEnd == nil {
		t.Fatalf("时区 %q，天数 %d，周期末 %v", u.Timezone, len(u.Days), u.PeriodEnd)
	}
	if u.Days[0].Bytes != 100 || u.Days[1].Bytes != 0 || u.Days[2].Bytes != 300 || u.Days[2].Date != today.Format(time.DateOnly) {
		t.Fatalf("逐日: %+v", u.Days)
	}
	if u.TodayBytes != 300 || u.AvgDailyBytes != 133 {
		t.Fatalf("今天 %d，日均 %d", u.TodayBytes, u.AvgDailyBytes)
	}
	if !u.PeriodStart.Equal(start) {
		t.Fatalf("周期起点 %v，想要 %v", u.PeriodStart, start)
	}
}

func TestMyUsageDays(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")

	u, err := de.svc.MyUsage(userCtx(ctx, user), sub.ID, 7)
	if err != nil || len(u.Days) != 7 {
		t.Fatalf("days=7: %v 天数 %d", err, len(u.Days))
	}
	for _, days := range []int{94, -1} {
		_, err := de.svc.MyUsage(userCtx(ctx, user), sub.ID, days)
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
			t.Fatalf("days=%d: %v", days, err)
		}
	}
	other := newUser(t, de.env, ctx)
	if _, err := de.svc.MyUsage(userCtx(ctx, other), sub.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别人的订阅: %v", err)
	}
}
