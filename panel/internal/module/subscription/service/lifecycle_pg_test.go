package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// lapse 把订阅改成 days 天前已到期（状态改 expired），cycle 行跟到旧到期，day 行停在到期前一天。
// endSub 把订阅改成已过期且关了续费窗口（期末在 40 天前）。
func endSub(t *testing.T, e *env, ctx context.Context, subID uuid.UUID) {
	t.Helper()
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '90 days',
		current_period_end = now() - interval '40 days', renewal_closed_at = now() WHERE id = ?`, subID)
}

func lapse(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, days int) time.Time {
	t.Helper()
	end := time.Now().UTC().Truncate(time.Microsecond).AddDate(0, 0, -days)
	start := end.AddDate(0, -1, 0)
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = ?, current_period_end = ? WHERE id = ?`, start, end, subID)
	e.exec(t, ctx, `UPDATE quota_balances SET period_start = ?, period_end = ? WHERE subscription_id = ? AND period = 'cycle'`, start, end, subID)
	e.exec(t, ctx, `UPDATE quota_balances SET period_start = ?, period_end = ? WHERE subscription_id = ? AND period = 'day'`, end.AddDate(0, 0, -1), end, subID)
	return end
}

func TestProvisionPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)},
		quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})

	sub := provision(t, e, ctx, user, planID, versionID, "Work")
	if sub.Status != StatusActive || sub.Label == nil || *sub.Label != "Work" || sub.NodeUID == 0 || sub.ProxyUUID == uuid.Nil {
		t.Fatalf("订阅: %+v", sub)
	}
	if want := AddInterval(sub.PeriodStart, "month", 1); !sub.PeriodEnd.Equal(want) {
		t.Fatalf("周期末 %s want %s", sub.PeriodEnd, want)
	}
	cycle := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if cycle.PeriodEnd == nil || !cycle.PeriodEnd.Equal(sub.PeriodEnd) || *cycle.Limit != 100*gib {
		t.Fatalf("cycle 行: %+v", cycle)
	}
	day := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay)
	if day.PeriodEnd == nil || !day.PeriodEnd.Equal(sub.PeriodStart.AddDate(0, 0, 1)) {
		t.Fatalf("day 行: %+v", day)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'
		AND octet_length(token_hash) = 32 AND char_length(token_prefix) = 8 AND octet_length(token_encrypted) > 0`, sub.ID); n != 1 {
		t.Fatalf("凭据 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'activated' AND from_status IS NULL`, sub.ID); n != 1 {
		t.Fatalf("activated 事件 %d", n)
	}
	// 同名自动加序号
	sub2 := provision(t, e, ctx, user, planID, versionID, "work")
	if sub2.Label == nil || *sub2.Label != "work 2" {
		t.Fatalf("同名加序号: %v", sub2.Label)
	}

	// 版本不属于该套餐
	otherPlan, _ := newPlan(t, e, ctx)
	if _, err := e.svc.Provision(ctx, Provision{UserID: user, PlanID: otherPlan, PlanVersionID: versionID, Price: monthly(), Actor: Actor{Kind: ActorSystem}}); !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("错版本: %v", err)
	}
}

func TestRenewEarlyPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 7)

	orderID := newOrder(t, e, ctx, user, "renewal", &sub.ID)
	res, err := e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: time.Now(),
		OrderID: &orderID, Actor: Actor{Kind: ActorPayment}, Source: "order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Restarted || !res.PeriodEnd.Equal(AddInterval(sub.PeriodEnd, "month", 1).Truncate(time.Microsecond)) || res.FromStatus != StatusActive {
		t.Fatalf("提前续费: %+v", res)
	}
	if q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle); q.Consumed != 7 {
		t.Fatalf("提前续费不动配额: %+v", q)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ?`, sub.ID); n != 0 {
		t.Fatalf("提前续费不写重置日志: %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'renewed' AND order_id = ?`, sub.ID, orderID); n != 1 {
		t.Fatalf("renewed 事件 %d", n)
	}
	// 不是本人的
	if _, err := e.svc.Renew(ctx, Renewal{SubscriptionID: sub.ID, UserID: uuid.New(), Price: monthly(), PaidAt: time.Now(), Actor: Actor{Kind: ActorPayment}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("非本人: %v", err)
	}
}

func TestRenewRestartPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)},
		quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	lapse(t, e, ctx, sub.ID, 3)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 99)
	e.exec(t, ctx, `UPDATE quota_balances SET limit_value = limit_value + 10 WHERE subscription_id = ? AND period = 'cycle'`, sub.ID)

	paid := time.Now().UTC().Truncate(time.Microsecond)
	res, err := e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: paid, OrderCreatedAt: &paid,
		Actor: Actor{Kind: ActorPayment}, Source: "order",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Restarted || res.FromStatus != StatusExpired || !res.PeriodEnd.Equal(AddInterval(paid, "month", 1)) {
		t.Fatalf("过期恢复: %+v", res)
	}
	got, err := e.svc.Get(ctx, sub.ID, &user)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive || !got.PeriodStart.Equal(paid) {
		t.Fatalf("订阅: %+v", got)
	}
	cycle := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if cycle.Consumed != 0 || *cycle.Limit != 100*gib || !cycle.PeriodStart.Equal(paid) || !cycle.PeriodEnd.Equal(res.PeriodEnd) {
		t.Fatalf("cycle 行: %+v", cycle)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ? AND reason = 'renewal'`, sub.ID); n != 2 {
		t.Fatalf("renewal 重置日志 %d", n)
	}

	// 关窗之后不能续
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '90 days',
		current_period_end = now() - interval '40 days', renewal_closed_at = now() WHERE id = ?`, sub.ID)
	if _, err := e.svc.Renew(ctx, Renewal{SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: time.Now(), Actor: Actor{Kind: ActorPayment}}); !errors.Is(err, ErrRenewalWindowClosed) {
		t.Fatalf("关窗: %v", err)
	}
}

func TestExtendRescuePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)},
		quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	oldEnd := lapse(t, e, ctx, sub.ID, 2)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 40)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 3)

	res, err := e.svc.Extend(ctx, Extension{
		SubscriptionID: sub.ID, UserID: &user, Days: 7,
		Actor: Actor{Kind: ActorUser, ID: &user}, Source: ExtendSourceGiftCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rescued || !res.Restarted || !res.PreviousEnd.Equal(oldEnd) || res.FromStatus != StatusExpired {
		t.Fatalf("救回: %+v", res)
	}
	added := res.Prorated[MetricTraffic]
	if added <= 0 || added >= 100*gib {
		t.Fatalf("折算 %d", added)
	}
	cycle := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if *cycle.Limit != 100*gib+added || cycle.Consumed != 40 || !cycle.PeriodEnd.Equal(res.PeriodEnd) {
		t.Fatalf("cycle 行: %+v", cycle)
	}
	day := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay)
	if day.Consumed != 0 {
		t.Fatalf("day 行应清零: %+v", day)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ? AND reason = 'gift_card' AND consumed_before = 3`, sub.ID); n != 1 {
		t.Fatalf("gift_card 重置日志 %d", n)
	}
	got, _ := e.svc.Get(ctx, sub.ID, nil)
	if got.Status != StatusActive || got.StateAt(time.Now()) != StateLive {
		t.Fatalf("应生效: %+v", got)
	}

	// 未到期再延：接着到期往后，不折算
	res2, err := e.svc.Extend(ctx, Extension{SubscriptionID: sub.ID, Days: 1, Actor: Actor{Kind: ActorUser, ID: &user}, Source: ExtendSourceGiftCard})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Rescued || !res2.PeriodEnd.Equal(res.PeriodEnd.AddDate(0, 0, 1)) || len(res2.Prorated) != 0 {
		t.Fatalf("续延: %+v", res2)
	}

	// 后台加时长：管理员入口写审计；关窗后拒绝
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	out, err := e.svc.AdminExtend(actx, sub.ID, 3, "补偿线路故障")
	if err != nil {
		t.Fatal(err)
	}
	if out.UserEmail == "" || out.Days != 3 {
		t.Fatalf("后台加时长: %+v", out)
	}
	if _, err := e.svc.AdminExtend(actx, sub.ID, 3, "短"); errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("原因太短: %v", err)
	}
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '90 days',
		current_period_end = now() - interval '40 days', renewal_closed_at = now() WHERE id = ?`, sub.ID)
	if _, err := e.svc.AdminExtend(actx, sub.ID, 3, "补偿线路故障"); !errors.Is(err, ErrExtendWindowClosed) {
		t.Fatalf("关窗: %v", err)
	}
}

func TestChangePlanPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planA, versionA := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)},
		quotaSpec{MetricTraffic, PeriodDay, i64(5 * gib)})
	planB, versionB := newPlan(t, e, ctx,
		quotaSpec{MetricTraffic, PeriodCycle, i64(200 * gib)},
		quotaSpec{MetricTraffic, PeriodMonth, i64(150 * gib)})
	sub := provision(t, e, ctx, user, planA, versionA, "")
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 11)
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodDay, 2)

	// 来源必须恰好一个
	if _, err := e.svc.ChangePlan(ctx, PlanChange{SubscriptionID: sub.ID, UserID: user, PlanID: planB, PlanVersionID: versionB, Price: monthly(), Actor: Actor{Kind: ActorPayment}}); errs.As(err).Code != errs.Internal {
		t.Fatalf("无来源: %v", err)
	}
	orderID := newOrder(t, e, ctx, user, "upgrade", &sub.ID)
	yearly := monthly()
	yearly.Interval = "year"
	res, err := e.svc.ChangePlan(ctx, PlanChange{
		SubscriptionID: sub.ID, UserID: user, PlanID: planB, PlanVersionID: versionB,
		Price: yearly, OrderID: &orderID, Actor: Actor{Kind: ActorPayment},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.FromPlanID != planA || res.FromPlanVersionID != versionA {
		t.Fatalf("结果: %+v", res)
	}
	got, _ := e.svc.Get(ctx, sub.ID, &user)
	if got.PlanID != planB || got.PlanVersionID != versionB || got.Price.Interval != "year" || !got.PeriodEnd.Equal(res.PeriodEnd) {
		t.Fatalf("订阅: %+v", got)
	}
	cycle := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if *cycle.Limit != 200*gib || cycle.Consumed != 0 {
		t.Fatalf("cycle 行: %+v", cycle)
	}
	day := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodDay)
	if day.Limit != nil || day.Consumed != 0 {
		t.Fatalf("新版本没有的 day 行应不限: %+v", day)
	}
	month := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodMonth)
	if *month.Limit != 150*gib {
		t.Fatalf("补的 month 行: %+v", month)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ? AND reason = 'plan_change'`, sub.ID); n != 2 {
		t.Fatalf("plan_change 重置日志 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id = ? AND event_type = 'plan_changed' AND order_id = ?`, sub.ID, orderID); n != 1 {
		t.Fatalf("plan_changed 事件 %d", n)
	}
}
