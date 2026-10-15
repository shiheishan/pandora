package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

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
func TestResetOrderCancelReleases(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	cpn := couponCode()
	e.mkCoupon(t, ctx, cpn, 200)
	e.giveBalance(t, ctx, user, 300)

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{
		SubscriptionID: sub.ID,
		Checkout:       Checkout{CouponCode: cpn, UseBalance: dec("3")},
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

// TestResetOrderCouponPeriodLimited 限 month 的券对重置单不适用。
func TestResetOrderCouponPeriodLimited(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	cid := e.mkCoupon(t, ctx, couponCode(), 200)
	e.mustExec(t, ctx, `INSERT INTO coupon_intervals (coupon_id, billing_interval) VALUES (?, 'month')`, cid)
	code := e.strCol(t, ctx, `SELECT code FROM coupons WHERE id = ?`, cid)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID, CouponCode: code})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	q := res.Quotes[0]
	if q.CouponError == nil || !q.Discount.IsZero() {
		t.Fatalf("限周期券报价 CouponError=%v Discount=%s", q.CouponError, q.Discount)
	}
	_, err = e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID, Checkout: Checkout{CouponCode: code}})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("建单应 validation_failed，得到 %v", err)
	}

	okCode := couponCode()
	e.mkCoupon(t, ctx, okCode, 200)
	res, err = e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID, CouponCode: okCode})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("不限周期报价 %+v %v", res, err)
	}
	mustEq(t, "Discount", res.Quotes[0].Discount, dec("2"))
}
