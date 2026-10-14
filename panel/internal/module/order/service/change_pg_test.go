package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// activeSub 让用户用余额全额买下一个 cents 元/月的套餐（零元单当场开通，开通事件挂着这张订单），
// 再把本期流量用掉 usedGiB（上限 100 GiB）。刚买下时时间比例接近 1，剩余价值由流量比例决定，金额是确定的。
func (e *env) activeSub(t *testing.T, ctx context.Context, user uuid.UUID, cents int64, usedGiB int64) subsvc.Subscription {
	t.Helper()
	planID, _, priceID := e.mkPlan(t, ctx, cents, "month", nil, nil)
	e.giveBalance(t, ctx, user, cents)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: money.FromCents(cents)})
	if c.Status != StatusFulfilled {
		t.Fatalf("买下原套餐：状态 %s", c.Status)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.SubscriptionID == nil {
		t.Fatal("原套餐没有开出订阅")
	}
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		usedGiB*gib, *o.SubscriptionID)
	sub, err := e.subs.Get(ctx, *o.SubscriptionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// changeQuote 取「把 sub 换成 planID」的报价（应只有一条），连同确认下单要带回的 expect（不用余额）。
func (e *env) changeQuote(t *testing.T, ctx context.Context, user uuid.UUID, subID, planID uuid.UUID) (Quote, *Expect) {
	t.Helper()
	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteChange, SubscriptionID: &subID, PlanID: &planID})
	if err != nil {
		t.Fatalf("换套餐报价: %v", err)
	}
	if len(res.Quotes) != 1 {
		t.Fatalf("报价 %d 条，期望 1", len(res.Quotes))
	}
	q := res.Quotes[0]
	return q, &Expect{AsOf: res.AsOf, Total: q.Total, BalanceApplied: q.WithoutBalance.Applied, Payable: q.WithoutBalance.Payable}
}

func (e *env) planOf(t *testing.T, ctx context.Context, subID uuid.UUID) uuid.UUID {
	t.Helper()
	sub, err := e.subs.Get(ctx, subID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sub.PlanID
}

// mustErrMsg 核对错误码与文案（service 的哨兵错误经事务返回后按码与文案比）。
func mustErrMsg(t *testing.T, what string, err error, want *errs.Error) {
	t.Helper()
	got := errs.As(err)
	if err == nil || got.Code != want.Code || got.Message != want.Message {
		t.Fatalf("%s：得到 %v，期望 %s「%s」", what, err, want.Code, want.Message)
	}
}

// 升级补差：30 元/月用掉一半流量，剩余价值 15.00；换成 100 元/月补 85.00，付款后换过去，不退余额。
func TestPlanChangeUpgradeTopUp(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month", nil, nil)

	q, exp := e.changeQuote(t, ctx, user, sub.ID, planB)
	mustEq(t, "报价 credit", q.Credit, dec("15"))
	mustEq(t, "报价 total", q.Total, dec("85"))
	mustEq(t, "报价 refund", q.Refund, dec("0"))
	if q.CreditDetail == nil || q.CreditDetail.RatioPPM != 500000 || !q.CreditDetail.Paid.Equal(dec("30")) {
		t.Fatalf("credit_detail %+v", q.CreditDetail)
	}

	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB, Checkout: Checkout{Expect: exp},
	})
	if err != nil {
		t.Fatalf("换套餐下单: %v", err)
	}
	if c.Status != StatusPending || c.Change == nil {
		t.Fatalf("结果 %+v", c)
	}
	mustEq(t, "Payable", c.Payable, dec("85"))
	mustEq(t, "ProrationCredit", c.Change.ProrationCredit, dec("15"))
	mustEq(t, "BalanceRefund", c.Change.BalanceRefund, dec("0"))
	o := e.order(t, ctx, c.OrderID)
	if o.Kind != KindUpgrade || o.SubscriptionID == nil || *o.SubscriptionID != sub.ID {
		t.Fatalf("订单 kind=%s sub=%v", o.Kind, o.SubscriptionID)
	}
	mustEq(t, "proration_credit_amount", o.ProrationCreditAmount, dec("15"))
	mustEq(t, "waived_amount", o.WaivedAmount, dec("0"))
	if p := e.planOf(t, ctx, sub.ID); p == planB {
		t.Fatal("付款前就换过去了")
	}

	code := payCode()
	e.mkPayProvider(t, ctx, code)
	e.payAndNotify(t, ctx, user, c.OrderID, code, "up-"+randHex(8))
	if o := e.order(t, ctx, c.OrderID); o.Status != StatusFulfilled {
		t.Fatalf("付款后状态 %s", o.Status)
	}
	if p := e.planOf(t, ctx, sub.ID); p != planB {
		t.Fatalf("订阅套餐 %s，期望换成 %s", p, planB)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 0 {
		t.Fatalf("升级不该退余额，plan_change_refund %d 笔", n)
	}
	e.assertNoDrift(t, ctx)
}

// 降级退余额：100 元/月用掉两成流量，剩余价值 80.00；换成 30 元/月应付 0、当场生效，差额 50.00 退进余额。
func TestPlanChangeDowngradeRefundsBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 10000, 20)
	planB, _, priceB := e.mkPlan(t, ctx, 3000, "month", nil, nil)

	q, exp := e.changeQuote(t, ctx, user, sub.ID, planB)
	mustEq(t, "报价 credit", q.Credit, dec("80"))
	mustEq(t, "报价 total", q.Total, dec("0"))
	mustEq(t, "报价 refund", q.Refund, dec("50"))
	mustEq(t, "换前余额", e.balanceOf(t, ctx, user), dec("0"))

	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB, Checkout: Checkout{Expect: exp},
	})
	if err != nil {
		t.Fatalf("换套餐下单: %v", err)
	}
	if c.Status != StatusFulfilled || c.Change == nil {
		t.Fatalf("结果 %+v", c)
	}
	mustEq(t, "Total", c.Total, dec("0"))
	mustEq(t, "BalanceRefund", c.Change.BalanceRefund, dec("50"))
	if p := e.planOf(t, ctx, sub.ID); p != planB {
		t.Fatalf("订阅套餐 %s，期望换成 %s", p, planB)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 1 {
		t.Fatalf("plan_change_refund %d 笔", n)
	}
	mustEq(t, "换后余额", e.balanceOf(t, ctx, user), dec("50"))
	e.assertNoDrift(t, ctx)
}

// 零元直接生效：剩余价值 15.00 恰好抵完 15 元的新价，应付 0、当场换过去，不退也不扣余额。
func TestPlanChangeZeroPayTakesEffect(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 1500, "month", nil, nil)

	_, exp := e.changeQuote(t, ctx, user, sub.ID, planB)
	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB, Checkout: Checkout{Expect: exp},
	})
	if err != nil {
		t.Fatalf("换套餐下单: %v", err)
	}
	if c.Status != StatusFulfilled {
		t.Fatalf("状态 %s，期望当场履约", c.Status)
	}
	mustEq(t, "Total", c.Total, dec("0"))
	mustEq(t, "Payable", c.Payable, dec("0"))
	mustEq(t, "BalanceRefund", c.Change.BalanceRefund, dec("0"))
	o := e.order(t, ctx, c.OrderID)
	if o.FulfilledAt == nil {
		t.Fatal("没有履约时间")
	}
	if p := e.planOf(t, ctx, sub.ID); p != planB {
		t.Fatalf("订阅套餐 %s，期望换成 %s", p, planB)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 0 {
		t.Fatalf("恰好抵完不该退余额，plan_change_refund %d 笔", n)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("0"))

	// 换过去之后再换回原价位：新周期刚开始、流量没用，剩余价值来自这张换套餐单（15 − 0 − 0），不是 0
	planC, _, _ := e.mkPlan(t, ctx, 5000, "month", nil, nil)
	q, _ := e.changeQuote(t, ctx, user, sub.ID, planC)
	if !q.Credit.IsPositive() || q.Credit.GreaterThan(dec("15")) {
		t.Fatalf("连换两次的剩余价值 %s，应在 (0, 15]", q.Credit.StringFixed(2))
	}
	e.assertNoDrift(t, ctx)
}

// 换套餐单与续费单互斥：同一份订阅同时只能有一张未完结的续费或变更单；同一套餐不能「换」（请续费）。
func TestPlanChangeAndRenewalMutuallyExclusive(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month", nil, nil)

	if _, err := e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: sub.PlanID, PriceID: *sub.Price.PriceID,
	}); err == nil {
		t.Fatal("换成同一个套餐应被拒")
	} else {
		mustErrMsg(t, "同套餐", err, ErrPlanChangeSamePlan)
	}

	change, err := e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB})
	if err != nil || change.Status != StatusPending {
		t.Fatalf("换套餐下单: %+v %v", change, err)
	}
	_, err = e.svc.CreateRenewalOrder(uctx, RenewalOrderInput{SubscriptionID: sub.ID})
	mustErrMsg(t, "有待付变更单时续费", err, ErrOpenChange)
	_, err = e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB})
	mustErrMsg(t, "有待付变更单时再换", err, ErrOpenChange)
	// 报价也不列这一份（按套餐展开时跳过有未完结单的订阅）
	res, err := e.svc.Quote(uctx, QuoteInput{Action: QuoteChange, PlanID: &planB})
	if err != nil || len(res.Quotes) != 0 {
		t.Fatalf("报价 %d 条 err %v，期望 0", len(res.Quotes), err)
	}

	if _, err := e.svc.Cancel(uctx, CancelInput{OrderID: change.OrderID}); err != nil {
		t.Fatalf("取消变更单: %v", err)
	}
	renewal, err := e.svc.CreateRenewalOrder(uctx, RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil || renewal.Status != StatusPending {
		t.Fatalf("取消后续费: %+v %v", renewal, err)
	}
	_, err = e.svc.CreatePlanChangeOrder(uctx, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB})
	mustErrMsg(t, "有待付续费单时换套餐", err, ErrOpenChange)
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE subscription_id = ? AND status = 'pending_payment'`, sub.ID); n != 1 {
		t.Fatalf("这份订阅上待付单 %d 张，期望 1", n)
	}
}
