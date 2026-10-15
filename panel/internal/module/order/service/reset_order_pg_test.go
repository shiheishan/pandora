package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// setResetPrice 给套餐配该币种的流量重置价。
func (e *env) setResetPrice(t *testing.T, ctx context.Context, planID uuid.UUID, cents int64) {
	t.Helper()
	e.mustExec(t, ctx, `INSERT INTO plan_reset_prices (plan_id, currency, unit_amount) VALUES (?, 'CNY', ?)`,
		planID, money.FromCents(cents))
}

// liveSub 开通一份生效中的订阅（月付 cents 元）。
func (e *env) liveSub(t *testing.T, ctx context.Context, user uuid.UUID, cents int64) (sub subsvc.Subscription, planID, versionID, priceID uuid.UUID) {
	t.Helper()
	planID, versionID, priceID = e.mkPlan(t, ctx, cents, "month", nil, nil)
	sub = e.provision(t, ctx, user, planID, versionID, priceID, cents)
	return sub, planID, versionID, priceID
}

// lapseSub 把订阅改成已过期（照 change_more_pg_test 的写法：周期末挪到昨天）。
func (e *env) lapseSub(t *testing.T, ctx context.Context, subID uuid.UUID) {
	t.Helper()
	end := time.Now().UTC().Truncate(time.Microsecond).AddDate(0, 0, -1)
	e.mustExec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = ?, current_period_end = ? WHERE id = ?`, end.AddDate(0, -1, 0), end, subID)
}

// consumedTraffic 读 cycle 流量已用量。
func (e *env) consumedTraffic(t *testing.T, ctx context.Context, subID uuid.UUID) int64 {
	t.Helper()
	return e.count(t, ctx, `SELECT consumed FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`, subID)
}

// TestResetOrderQuoteAndNotSold 没配价报价空；配价后一条；没配价的套餐建单 conflict。
func TestResetOrderQuoteAndNotSold(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatalf("报价: %v", err)
	}
	if len(res.Quotes) != 0 {
		t.Fatalf("没配价应空 Quotes，得到 %d 条", len(res.Quotes))
	}

	e.setResetPrice(t, ctx, planID, 800)
	res, err = e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	q := res.Quotes[0]
	mustEq(t, "Subtotal", q.Subtotal, dec("8"))
	mustEq(t, "Total", q.Total, dec("8"))
	if q.PlanID == nil || *q.PlanID != planID {
		t.Fatalf("PlanID %v", q.PlanID)
	}
	if q.PriceID != nil {
		t.Fatalf("PriceID 应为 nil，得到 %v", q.PriceID)
	}

	otherPlan, _, _, _ := e.liveSub(t, ctx, user, 2000)
	_, err = e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: otherPlan.ID})
	if codeOf(err) != errs.Conflict {
		t.Fatalf("没配价建单应 conflict，得到 %v", err)
	}
}

// TestResetOrderPaidResetsTraffic 付清后履约清零流量并写重置日志。
func TestResetOrderPaidResetsTraffic(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		30*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil || c.Status != StatusPending {
		t.Fatalf("建单 %+v %v", c, err)
	}
	ord := e.order(t, ctx, c.OrderID)
	if ord.OrderNo == "" {
		t.Fatal("缺订单号")
	}
	e.payAndNotify(t, ctx, user, c.OrderID, code, "rst-"+randHex(8))

	if o := e.order(t, ctx, c.OrderID); o.Status != StatusFulfilled {
		t.Fatalf("订单 %s", o.Status)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 0 {
		t.Fatal("consumed 应清零")
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM traffic_reset_logs WHERE subscription_id = ? AND reason = 'order' AND note = ? AND consumed_before = ?`,
		sub.ID, ord.OrderNo, 30*gib); n != 1 {
		t.Fatalf("重置日志 %d 行", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_items WHERE order_id = ? AND price_id IS NULL AND snapshot_name LIKE '流量重置 · %'`, c.OrderID); n != 1 {
		t.Fatalf("订单行形状 %d", n)
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderZeroPayWithBalance 余额全额当场履约。
func TestResetOrderZeroPayWithBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.giveBalance(t, ctx, user, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		5*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{
		SubscriptionID: sub.ID,
		Checkout:       Checkout{UseBalance: dec("8")},
	})
	if err != nil || c.Status != StatusFulfilled {
		t.Fatalf("建单 %+v %v", c, err)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 0 {
		t.Fatal("consumed 应清零")
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("0"))
	e.assertNoDrift(t, ctx)
}

// TestResetOrderPendingConflict 有待付重置单时报价与建单都 order_pending。
func TestResetOrderPendingConflict(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	first, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID})
	if codeOf(err) != errs.OrderPending {
		t.Fatalf("报价应 order_pending，得到 %v", err)
	}
	_, err = e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	ee := errs.As(err)
	if ee.Code != errs.OrderPending || ee.Fields["order_id"] != first.OrderID.String() {
		t.Fatalf("建单应 order_pending 带 order_id，得到 %v", err)
	}
}

// TestResetOrderNotLive 订阅过期后报价空、建单 validation_failed。
func TestResetOrderNotLive(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.lapseSub(t, ctx, sub.ID)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID})
	if err != nil || len(res.Quotes) != 0 {
		t.Fatalf("过期报价应空，得到 %+v %v", res, err)
	}
	_, err = e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("建单应 validation_failed，得到 %v", err)
	}
}

// TestResetOrderLateAfterExpiry 待付单支付前订阅过期，回调进挂账 ineligible_subscription。
func TestResetOrderLateAfterExpiry(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		10*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	e.lapseSub(t, ctx, sub.ID)

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 800))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, c.OrderID)
	if kind != "ineligible_subscription" || ostatus != StatusPending {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 10*gib {
		t.Fatal("consumed 不应变")
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderCancelReleases 取消后预留释放、余额退回，可再建单。
// 重置单永远按原价收（用户 10-14 定），不收券，这里只验余额部分抵扣。
func TestResetOrderCancelReleases(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.giveBalance(t, ctx, user, 300)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{
		SubscriptionID: sub.ID,
		Checkout:       Checkout{UseBalance: dec("3")},
	})
	if err != nil || c.Status != StatusPending {
		t.Fatalf("建单 %+v %v", c, err)
	}
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID}); err != nil {
		t.Fatalf("取消: %v", err)
	}
	if o := e.order(t, ctx, c.OrderID); o.Status != StatusCancelled {
		t.Fatalf("订单 %s", o.Status)
	}
	if st := e.reservationState(t, ctx, c.OrderID); st != ReservationReleased {
		t.Fatalf("预留 %s", st)
	}
	mustEq(t, "余额退回", e.balanceOf(t, ctx, user), dec("3"))

	c2, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil || c2.OrderID == c.OrderID {
		t.Fatalf("应能再建一张 %+v %v", c2, err)
	}
}

// TestResetOrderRejectsCoupon 重置单不收任何券（用户 10-14 定）：限定周期的与不限的都拒。
func TestResetOrderRejectsCoupon(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)

	// 限 month 的券
	cid := e.mkCoupon(t, ctx, couponCode(), 200)
	e.mustExec(t, ctx, `INSERT INTO coupon_intervals (coupon_id, billing_interval) VALUES (?, 'month')`, cid)
	limited := e.strCol(t, ctx, `SELECT code FROM coupons WHERE id = ?`, cid)
	// 不限周期的券
	open := couponCode()
	e.mkCoupon(t, ctx, open, 200)

	for _, code := range []string{limited, open} {
		res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID, CouponCode: code})
		if err != nil || len(res.Quotes) != 1 {
			t.Fatalf("报价（%s）%+v %v", code, res, err)
		}
		q := res.Quotes[0]
		if q.CouponError == nil || *q.CouponError != "流量重置不能使用优惠码" {
			t.Fatalf("券码 %s 报价 CouponError=%v", code, q.CouponError)
		}
		if !q.Discount.IsZero() {
			t.Fatalf("券码 %s 报价 Discount=%s，应为 0", code, q.Discount)
		}
		mustEq(t, "报价 Total", q.Total, dec("8"))

		_, err = e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID, Checkout: Checkout{CouponCode: code}})
		if !errors.Is(err, promosvc.ErrCouponNotForReset) {
			t.Fatalf("券码 %s 建单应 ErrCouponNotForReset，得到 %v", code, err)
		}
		if codeOf(err) != errs.ValidationFailed {
			t.Fatalf("券码 %s 建单应 validation_failed，得到 %v", code, err)
		}
	}
}

// bumpVersion 给套餐建第 2 版（照 mkPlan 的形状：一条 cycle 流量配额 + 发布），把订阅挪过去。
func (e *env) bumpVersion(t *testing.T, ctx context.Context, subID, planID uuid.UUID) {
	t.Helper()
	var v2 uuid.UUID
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 2, 3) RETURNING id`,
		planID).Scan(&v2); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `INSERT INTO quota_definitions (plan_version_id, metric, period, limit_value) VALUES (?, ?, 'cycle', ?)`,
		v2, catalogsvc.MetricTraffic, 100*gib)
	e.mustExec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, v2)
	e.mustExec(t, ctx, `UPDATE subscriptions SET plan_version_id = ? WHERE id = ?`, v2, subID)
}

// TestResetOrderLateAfterVersionChange 建单后套餐换了版本，回调进挂账 ineligible_subscription，已用量不变。
func TestResetOrderLateAfterVersionChange(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		10*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	e.bumpVersion(t, ctx, sub.ID, planID)

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 800))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, c.OrderID)
	if kind != "ineligible_subscription" || ostatus != StatusPending {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 10*gib {
		t.Fatal("consumed 不应变")
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderLateAfterUnlimited 建单后流量配额改成不限额，回调进挂账，已用量不变。
func TestResetOrderLateAfterUnlimited(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		10*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	// 版本不变，专门打到「没有限额流量配额」这一支
	e.mustExec(t, ctx, `UPDATE quota_balances SET limit_value = NULL WHERE subscription_id = ? AND metric = 'traffic.bytes'`, sub.ID)

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 800))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, c.OrderID)
	if kind != "ineligible_subscription" || ostatus != StatusPending {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 10*gib {
		t.Fatal("consumed 不应变")
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderLateAfterPeriodRoll 建单后流量配额跨了周期（period_start 晚于建单时刻），回调进挂账，已用量不变。
func TestResetOrderLateAfterPeriodRoll(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		10*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	// 把周期起点挪到建单之后（建单后跨了周期，已自动清零过）
	e.mustExec(t, ctx, `UPDATE quota_balances SET period_start = (SELECT created_at FROM orders WHERE id = ?) + interval '1 millisecond'
WHERE subscription_id = ? AND metric = 'traffic.bytes'`, c.OrderID, sub.ID)

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 800))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, c.OrderID)
	if kind != "ineligible_subscription" || ostatus != StatusPending {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 10*gib {
		t.Fatal("consumed 不应变")
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderOpenIndexConflict 躲过建单的待付检查、插入时撞 orders_one_open_traffic_reset 回 errOpenReset。
// 注意：撞唯一索引是 PG 报错，会让整例回滚的外层事务进入 aborted 状态，之后任何查询都会失败，
// 所以 CreateResetOrder 必须是这个用例里最后一次碰库，之后只做 Go 侧断言，不调 assertNoDrift、不再查库。
func TestResetOrderOpenIndexConflict(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)

	// PendingResetOrderForSubscription 只查 status = 'pending_payment'，而索引条件是 status IN ('pending_payment', 'paid')，
	// 所以预插一张 status = 'paid' 的重置单能躲过待付检查、撞上索引
	e.mustExec(t, ctx, `
INSERT INTO orders (order_no, user_id, kind, status, currency, subscription_id,
                    subtotal_amount, discount_amount, proration_credit_amount, waived_amount,
                    total_amount, balance_applied, payable_amount, paid_amount,
                    expires_at, paid_at)
VALUES ('RST-' || upper(?), ?, 'traffic_reset', 'paid', 'CNY', ?,
        8, 0, 0, 0,
        8, 0, 8, 8,
        now() + interval '30 minutes', now())`,
		randHex(8), user, sub.ID)

	_, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if codeOf(err) != errs.Conflict {
		t.Fatalf("撞索引应 conflict，得到 %v", err)
	}
	if !errors.Is(err, errOpenReset) {
		t.Fatalf("撞索引应 errOpenReset，得到 %v", err)
	}
}

// TestResetOrderLateInRollGap 付款落在「这一期已结束、滚动任务还没推到下一期」的间隙里：回调进挂账 ineligible_subscription，
// 已用量不变。起点挪到建单之前，专门打到「这一期已结束」这一支，而不是「起点晚于建单」那一支。
func TestResetOrderLateInRollGap(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		10*gib, sub.ID)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	// 建单之后、回调之前，把 cycle 行改成这一期已结束（滚动任务至多约 10 分钟才推到下一期）
	e.mustExec(t, ctx, `UPDATE quota_balances
SET period_start = now() - interval '30 days', period_end = now() - interval '1 minute'
WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`, sub.ID)

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 800))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, c.OrderID)
	if kind != "ineligible_subscription" || ostatus != StatusPending {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 10*gib {
		t.Fatal("consumed 不应变")
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderRefusedInRollGap 建单落在「这一期已结束、还没滚」的间隙里：报价与建单都回 ErrResetPeriodSwitching，
// 不落订单。两次都是 Go 侧业务拒绝，不是 PG 报错，事务不会 aborted，之后照常查库。
func TestResetOrderRefusedInRollGap(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances
SET period_start = now() - interval '30 days', period_end = now() - interval '1 minute'
WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`, sub.ID)

	_, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID})
	if !errors.Is(err, ErrResetPeriodSwitching) {
		t.Fatalf("报价应 ErrResetPeriodSwitching，得到 %v", err)
	}
	if codeOf(err) != errs.Conflict {
		t.Fatalf("报价应 conflict，得到 %v", err)
	}
	_, err = e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if !errors.Is(err, ErrResetPeriodSwitching) {
		t.Fatalf("建单应 ErrResetPeriodSwitching，得到 %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE kind = 'traffic_reset' AND subscription_id = ?`, sub.ID); n != 0 {
		t.Fatalf("不该落订单，得到 %d 张", n)
	}
	e.assertNoDrift(t, ctx)
}

// TestResetOrderDayRollDoesNotBlock 同一份同时有 day 与 cycle 两条限额流量配额：作依据的是周期最长的 cycle 行，
// 建单后日行滚到新的一天不该把付款送进挂账，回调照常履约清零全部流量配额行。
func TestResetOrderDayRollDoesNotBlock(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		10*gib, sub.ID)
	// 同一份再加一条日行（周期比 cycle 短，不作依据）
	e.mustExec(t, ctx, `INSERT INTO quota_balances (subscription_id, metric, period, period_start, period_end, limit_value, consumed)
VALUES (?, 'traffic.bytes', 'day', now() - interval '1 hour', now() + interval '23 hours', ?, ?)`,
		sub.ID, 5*gib, 2*gib)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	// 建单之后、回调之前，日行滚到新的一天（起点变成建单之后；起点晚于建单的判断只看依据行，不受影响）
	e.mustExec(t, ctx, `UPDATE quota_balances
SET period_start = (SELECT created_at FROM orders WHERE id = ?) + interval '1 millisecond',
    period_end = (SELECT created_at FROM orders WHERE id = ?) + interval '1 day',
    consumed = 0
WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'day'`,
		c.OrderID, c.OrderID, sub.ID)

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 800))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, c.OrderID)
	if kind != "" {
		t.Fatalf("不该进挂账，kind=%q order_status=%q", kind, ostatus)
	}
	if o := e.order(t, ctx, c.OrderID); o.Status != StatusFulfilled {
		t.Fatalf("订单 %s", o.Status)
	}
	if e.consumedTraffic(t, ctx, sub.ID) != 0 {
		t.Fatal("cycle consumed 应清零")
	}
	e.assertNoDrift(t, ctx)
}
