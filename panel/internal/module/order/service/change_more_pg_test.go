package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// mkMinPay 插一条在收款的易支付渠道，只为让站点最低付款额变成 cents（MinPayment 不看 offline 渠道）。
func (e *env) mkMinPay(t *testing.T, ctx context.Context, cents int64) {
	t.Helper()
	e.mustExec(t, ctx, `
INSERT INTO payment_providers (code, adapter, display_name, min_amount, enabled, accepting_new)
VALUES (?, 'epay', 'MinPay', ?, true, true)`, "epay-"+randHex(8), money.FromCents(cents))
}

// auditField 读某订单上某个审计动作的 data 里的一项（没有时为空串）。
func (e *env) auditField(t *testing.T, ctx context.Context, action string, orderID uuid.UUID, key string) string {
	t.Helper()
	return e.strCol(t, ctx, `SELECT data->>? FROM audit_events WHERE action = ? AND resource_id = ? ORDER BY occurred_at DESC LIMIT 1`,
		key, action, orderID)
}

// 零头免除端到端：剩余价值 15.00 抵 15.50 的新价，余 0.50 低于最低额 1.00、用户没余额 → 门户换套餐免掉，当场生效。
// 库里 waived_amount 0.50、total 0；Created 的 Waived 带出（对外 discount_amount 含它）；审计记 small_due_waived 与方向；
// 下一次折算的基数是 15.50 − 0.50 = 15.00。
func TestPlanChangeSmallDueWaived(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	e.mkMinPay(t, ctx, 100)
	planB, _, priceB := e.mkPlan(t, ctx, 1550, "month")

	q, exp := e.changeQuote(t, ctx, user, sub.ID, planB)
	mustEq(t, "报价 total（免零头之前）", q.Total, dec("0.50"))
	if !q.WithoutBalance.SmallDue || !q.WithoutBalance.Waived.Equal(dec("0.50")) || !q.WithoutBalance.Payable.IsZero() {
		t.Fatalf("报价付法 %+v", q.WithoutBalance)
	}
	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB, Checkout: Checkout{Expect: exp},
	})
	if err != nil {
		t.Fatalf("换套餐下单: %v", err)
	}
	if c.Status != StatusFulfilled {
		t.Fatalf("状态 %s，期望免掉零头当场生效", c.Status)
	}
	mustEq(t, "Created.Total", c.Total, dec("0"))
	mustEq(t, "Created.Waived", c.Waived, dec("0.50"))
	mustEq(t, "Created.Discount", c.Discount, dec("0"))
	o := e.order(t, ctx, c.OrderID)
	mustEq(t, "waived_amount", o.WaivedAmount, dec("0.50"))
	mustEq(t, "total_amount", o.TotalAmount, dec("0"))
	mustEq(t, "payable_amount", o.PayableAmount, dec("0"))
	if v := e.auditField(t, ctx, ActionPlanChangeCreated, c.OrderID, "small_due_waived"); v != "0.50" {
		t.Fatalf("审计 small_due_waived=%q", v)
	}
	if v := e.auditField(t, ctx, ActionPlanChangeCreated, c.OrderID, "direction"); v != "upgrade" {
		t.Fatalf("审计 direction=%q", v)
	}
	if p := e.planOf(t, ctx, sub.ID); p != planB {
		t.Fatalf("订阅套餐 %s", p)
	}
	planC, _, _ := e.mkPlan(t, ctx, 5000, "month")
	next, _ := e.changeQuote(t, ctx, user, sub.ID, planC)
	if next.CreditDetail == nil || !next.CreditDetail.Paid.Equal(dec("15")) {
		t.Fatalf("下一次折算的付费基数 %+v，期望 15.00", next.CreditDetail)
	}
	e.assertNoDrift(t, ctx)
}

// 降级加券：剩余价值 80.00，新价 30.00 再用 5 元券，应付 0、退余额 80 − 25 = 55.00；券当场核销。
func TestPlanChangeDowngradeWithCoupon(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 10000, 20)
	planB, _, priceB := e.mkPlan(t, ctx, 3000, "month")
	code := couponCode()
	e.mkCoupon(t, ctx, code, 500)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteChange, SubscriptionID: &sub.ID, PlanID: &planB, CouponCode: code})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	q := res.Quotes[0]
	mustEq(t, "报价 discount", q.Discount, dec("5"))
	mustEq(t, "报价 refund", q.Refund, dec("55"))
	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB,
		Checkout: Checkout{CouponCode: code, Expect: &Expect{AsOf: res.AsOf, Total: q.Total,
			BalanceApplied: q.WithoutBalance.Applied, Payable: q.WithoutBalance.Payable}},
	})
	if err != nil {
		t.Fatalf("换套餐下单: %v", err)
	}
	if c.Status != StatusFulfilled || c.Change == nil {
		t.Fatalf("结果 %+v", c)
	}
	mustEq(t, "BalanceRefund", c.Change.BalanceRefund, dec("55"))
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("55"))
	if st := e.strCol(t, ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, c.OrderID); st != "captured" {
		t.Fatalf("券 %s", st)
	}
	if v := e.auditField(t, ctx, ActionPlanChangeCreated, c.OrderID, "direction"); v != "downgrade" {
		t.Fatalf("审计 direction=%q", v)
	}
	e.assertNoDrift(t, ctx)
}

// 归属与目录：别人的订阅 404（下单与报价）；目标套餐隐藏、不允许变更、价格属于别的套餐都被拒，不留订单。
func TestPlanChangeRejectsForeignAndCatalog(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	other := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month")

	_, err := e.svc.CreatePlanChangeOrder(asUser(ctx, other), PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB})
	if codeOf(err) != errs.NotFound {
		t.Fatalf("别人的订阅下单应 404，得到 %v", err)
	}
	if _, err := e.svc.Quote(asUser(ctx, other), QuoteInput{Action: QuoteChange, SubscriptionID: &sub.ID}); codeOf(err) != errs.NotFound {
		t.Fatalf("别人的订阅报价应 404，得到 %v", err)
	}

	uctx := asUser(ctx, user)
	try := func(what string, planID, priceID uuid.UUID, want errs.Code, msg string) {
		t.Helper()
		_, err := e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planID, PriceID: priceID})
		if err == nil || codeOf(err) != want || (msg != "" && errs.As(err).Message != msg) {
			t.Fatalf("%s：得到 %v，期望 %s %s", what, err, want, msg)
		}
	}
	hidden, _, hiddenPrice := e.mkPlan(t, ctx, 10000, "month")
	e.mustExec(t, ctx, `UPDATE plans SET visibility = 'hidden' WHERE id = ?`, hidden)
	try("目标套餐隐藏", hidden, hiddenPrice, errs.NotFound, "")

	locked, _, lockedPrice := e.mkPlan(t, ctx, 10000, "month")
	e.mustExec(t, ctx, `UPDATE plans SET allow_upgrade = false WHERE id = ?`, locked)
	try("不允许变更", locked, lockedPrice, catalogsvc.ErrChangeNotAllowed.Code, catalogsvc.ErrChangeNotAllowed.Message)

	if _, err := e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: lockedPrice}); err == nil {
		t.Fatal("价格属于别的套餐应被拒")
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE subscription_id = ? AND kind = 'upgrade'`, sub.ID); n != 0 {
		t.Fatalf("被拒的换套餐留下了 %d 张单", n)
	}
}

// 跨周期（审查 M1）：到期前下的续费单、过期之后才回调——续费接在原到期之后，不是重起，开通单仍在本周期，
// 剩余价值的付费基数是两张单的合计 60.00，不是只算续费单（那样付费总时长不够、剩余价值是 0）。
func TestPlanChangeAfterLateRenewalCallback(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	renewal, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil || renewal.Status != StatusPending {
		t.Fatalf("续费下单: %+v %v", renewal, err)
	}
	// 模拟：下单在原到期之前，到期扫描先把订阅标成 expired，回调随后才到
	now := time.Now().UTC().Truncate(time.Microsecond)
	oldEnd := now.AddDate(0, 0, -1)
	e.mustExec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = ?, current_period_end = ? WHERE id = ?`,
		oldEnd.AddDate(0, -1, 0), oldEnd, sub.ID)
	e.mustExec(t, ctx, `UPDATE orders SET created_at = ? WHERE id = ?`, oldEnd.Add(-time.Hour), renewal.OrderID)

	code := payCode()
	e.mkPayProvider(t, ctx, code)
	if ack := e.payAndNotify(t, ctx, user, renewal.OrderID, code, "late-"+randHex(8)); !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}
	after, err := e.subs.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !after.PeriodStart.Equal(oldEnd.AddDate(0, -1, 0)) {
		t.Fatalf("回调晚到不该重起周期：起点 %s", after.PeriodStart)
	}
	planB, _, _ := e.mkPlan(t, ctx, 10000, "month")
	q, _ := e.changeQuote(t, ctx, user, sub.ID, planB)
	if q.CreditDetail == nil || !q.CreditDetail.Paid.Equal(dec("60")) {
		t.Fatalf("付费基数 %+v，期望两张单合计 60.00", q.CreditDetail)
	}
	if !q.Credit.IsPositive() {
		t.Fatalf("剩余价值 %s，应为正", q.Credit.StringFixed(2))
	}
}

// 待付换套餐单挂着时 RemainingValue 拒（套餐卡不能先把同一份剩余价值退掉）；
// 订阅周期在建单之后重起（模拟套餐卡换套餐），再付这张单：钱进挂账 ineligible_subscription，不换不退。
func TestPlanChangeSettlementAfterPeriodRestart(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month")
	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB})
	if err != nil || c.Status != StatusPending {
		t.Fatalf("换套餐下单: %+v %v", c, err)
	}
	_, _, _, err = e.svc.RemainingValue(ctx, sub.ID, time.Now())
	mustErrMsg(t, "有待付变更单时 RemainingValue", err, ErrOpenChange)

	e.mustExec(t, ctx, `UPDATE subscriptions SET current_period_start = ? WHERE id = ?`, time.Now().UTC().Add(time.Second), sub.ID)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	if ack := e.payAndNotify(t, ctx, user, c.OrderID, code, "rs-"+randHex(8)); !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}
	kind, _, amt := e.lateCase(t, ctx, c.OrderID)
	if kind != "ineligible_subscription" {
		t.Fatalf("挂账 kind=%q", kind)
	}
	mustEq(t, "挂账金额", amt, dec("85"))
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单 %s，期望仍待付", st)
	}
	if p := e.planOf(t, ctx, sub.ID); p == planB {
		t.Fatal("周期已重起的换套餐单不该再换")
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 0 {
		t.Fatalf("不该退余额，plan_change_refund %d 笔", n)
	}
	e.assertNoDrift(t, ctx)
}
