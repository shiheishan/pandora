package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// setMemberDiscount 给用户设会员折扣百分比。
func (e *env) setMemberDiscount(t *testing.T, ctx context.Context, user uuid.UUID, percent int) {
	t.Helper()
	e.mustExec(t, ctx, `INSERT INTO member_discounts (user_id, percent) VALUES (?, ?)`, user, percent)
}

// TestMemberDiscountNewOrder 新购打会员折扣。
func TestMemberDiscountNewOrder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", nil, nil)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	mustEq(t, "Created.Discount", c.Discount, dec("3"))
	mustEq(t, "Created.MemberDiscount", c.MemberDiscount, dec("3"))
	mustEq(t, "Created.Total", c.Total, dec("27"))

	o := e.order(t, ctx, c.OrderID)
	mustEq(t, "discount_amount", o.DiscountAmount, dec("3"))
	mustEq(t, "member_discount_amount", o.MemberDiscountAmount, dec("3"))
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountWithCoupon 券与会员折扣叠加。
func TestMemberDiscountWithCoupon(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", nil, nil)
	code := couponCode()
	e.mkCoupon(t, ctx, code, 500)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{CouponCode: code})
	mustEq(t, "MemberDiscount", c.MemberDiscount, dec("3"))
	mustEq(t, "Discount", c.Discount, dec("8"))
	mustEq(t, "Total", c.Total, dec("22"))

	if n := e.count(t, ctx, `SELECT count(*) FROM coupon_redemptions WHERE order_id = ? AND discount_amount = 5`, c.OrderID); n != 1 {
		t.Fatalf("券占用折扣列应为 5.00，行数 %d", n)
	}
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountCappedBySubtotal 券加会员超过小计，会员被压。
func TestMemberDiscountCappedBySubtotal(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	code := couponCode()
	e.mkCoupon(t, ctx, code, 950)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{CouponCode: code})
	if c.Status != StatusFulfilled {
		t.Fatalf("状态 %s", c.Status)
	}
	mustEq(t, "Total", c.Total, dec("0"))
	mustEq(t, "MemberDiscount", c.MemberDiscount, dec("0.50"))
	mustEq(t, "Discount", c.Discount, dec("10"))
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountTruncates 向零截断到分。
func TestMemberDiscountTruncates(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 15)
	planID, _, priceID := e.mkPlan(t, ctx, 999, "month", nil, nil)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	mustEq(t, "MemberDiscount", c.MemberDiscount, dec("1.49"))
	mustEq(t, "Total", c.Total, dec("8.50"))
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountQuote 报价与建单会员折扣一致；无会员时 MemberPercent 为 0。
func TestMemberDiscountQuote(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", nil, nil)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteNew, PlanID: &planID})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	q := res.Quotes[0]
	mustEq(t, "报价 MemberDiscount", q.MemberDiscount, dec("3"))
	if q.MemberPercent != 10 {
		t.Fatalf("MemberPercent %d", q.MemberPercent)
	}
	mustEq(t, "报价 Total", q.Total, dec("27"))

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	mustEq(t, "建单 Total", c.Total, dec("27"))
	mustEq(t, "建单 MemberDiscount", c.MemberDiscount, dec("3"))

	other := e.mkUser(t, ctx)
	plan2, _, _ := e.mkPlan(t, ctx, 3000, "month", nil, nil)
	res2, err := e.svc.Quote(asUser(ctx, other), QuoteInput{Action: QuoteNew, PlanID: &plan2})
	if err != nil || len(res2.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res2, err)
	}
	if res2.Quotes[0].MemberPercent != 0 {
		t.Fatalf("无会员 MemberPercent %d", res2.Quotes[0].MemberPercent)
	}
}

// TestMemberDiscountNotOnTopupOrManual 充值与后台人工单不打会员折扣。
func TestMemberDiscountNotOnTopupOrManual(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	e.setMemberDiscount(t, ctx, user, 10)

	topup, err := e.svc.CreateTopup(asUser(ctx, user), TopupInput{Amount: dec("100")})
	if err != nil {
		t.Fatal(err)
	}
	if o := e.order(t, ctx, topup.OrderID); o.MemberDiscountAmount.IsPositive() {
		t.Fatalf("充值 member_discount_amount %s", o.MemberDiscountAmount)
	}

	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month", nil, nil)
	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID, Reason: manualReason, Settlement: SettlementPending,
	})
	if err != nil {
		t.Fatalf("人工单: %v", err)
	}
	mustEq(t, "人工单 Total", c.Total, dec("20"))
	if o := e.order(t, ctx, c.OrderID); o.MemberDiscountAmount.IsPositive() {
		t.Fatalf("人工单 member_discount_amount %s", o.MemberDiscountAmount)
	}
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountPlanChangeRefund 降级带会员折扣时报价 Refund 与履约退余额一致。
func TestMemberDiscountPlanChangeRefund(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	// 原套餐按原价买下（credit 80.00）之后再设会员折扣，换过去的新套餐 30.00 打九折减 3.00
	sub := e.activeSub(t, ctx, user, 10000, 20)
	e.setMemberDiscount(t, ctx, user, 10)
	planB, _, priceB := e.mkPlan(t, ctx, 3000, "month", nil, nil)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteChange, SubscriptionID: &sub.ID, PlanID: &planB})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	q := res.Quotes[0]
	mustEq(t, "报价 credit", q.Credit, dec("80"))
	mustEq(t, "报价 member", q.MemberDiscount, dec("3"))
	mustEq(t, "报价 total", q.Total, dec("0"))
	mustEq(t, "报价 refund", q.Refund, dec("53"))

	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB,
		Checkout: Checkout{Expect: &Expect{AsOf: res.AsOf, Total: q.Total,
			BalanceApplied: q.WithoutBalance.Applied, Payable: q.WithoutBalance.Payable}},
	})
	if err != nil || c.Status != StatusFulfilled || c.Change == nil {
		t.Fatalf("换套餐 %+v %v", c, err)
	}
	mustEq(t, "BalanceRefund", c.Change.BalanceRefund, dec("53"))
	o := e.order(t, ctx, c.OrderID)
	mustEq(t, "discount_amount", o.DiscountAmount, dec("3"))
	mustEq(t, "member_discount_amount", o.MemberDiscountAmount, dec("3"))
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("53"))
	e.assertNoDrift(t, ctx)
}

// TestCouponPeriodLimitNewOrder 券限定 year，月付新购不适用。
func TestCouponPeriodLimitNewOrder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", nil, nil)
	cid := e.mkCoupon(t, ctx, couponCode(), 500)
	e.mustExec(t, ctx, `INSERT INTO coupon_intervals (coupon_id, billing_interval) VALUES (?, 'year')`, cid)
	code := e.strCol(t, ctx, `SELECT code FROM coupons WHERE id = ?`, cid)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteNew, PlanID: &planID, CouponCode: code})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	if res.Quotes[0].CouponError == nil {
		t.Fatal("应有 CouponError")
	}

	_, err = e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, Checkout: Checkout{CouponCode: code}})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("建单应 validation_failed，得到 %v", err)
	}
}
