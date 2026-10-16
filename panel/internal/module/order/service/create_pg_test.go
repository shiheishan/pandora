package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 新购、不用余额、无券：待付，金额恒等，预留 held，订单行快照正确。
func TestCreateNewOrderPlain(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 3000, "month")

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	if c.Status != StatusPending {
		t.Fatalf("状态 %s，期望 pending_payment", c.Status)
	}
	if !strings.HasPrefix(c.OrderNo, "AO") {
		t.Fatalf("订单号 %s 应以 AO 开头", c.OrderNo)
	}
	mustEq(t, "Total", c.Total, dec("30"))
	mustEq(t, "Payable", c.Payable, dec("30"))
	mustEq(t, "BalanceApplied", c.BalanceApplied, dec("0"))

	o := e.order(t, ctx, c.OrderID)
	if o.Kind != KindNew || o.UserID != user || o.SubscriptionID != nil || o.CouponID != nil {
		t.Fatalf("订单形状不对: %+v", o)
	}
	mustEq(t, "subtotal", o.SubtotalAmount, dec("30"))
	mustEq(t, "payable = total - balance", o.PayableAmount, o.TotalAmount.Sub(o.BalanceApplied))
	if o.StateVersion != 1 {
		t.Fatalf("state_version %d", o.StateVersion)
	}
	if d := time.Until(o.ExpiresAt); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("付款期限应在 30 分钟后，实际还剩 %s", d)
	}
	if st := e.reservationState(t, ctx, c.OrderID); st != ReservationHeld {
		t.Fatalf("预留 %s", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND event_kind = 'reserve' AND actor_kind = 'user' AND actor_id = ?`, c.OrderID, user); n != 1 {
		t.Fatalf("reserve 流水 %d 条", n)
	}

	var name, interval, quotas string
	var ver, ic int
	var gotPrice, gotVersion uuid.UUID
	var unit, line string
	if err := e.row(ctx, `
SELECT snapshot_name, snapshot_plan_version, snapshot_interval, snapshot_interval_count, snapshot_quotas::text,
       price_id, plan_version_id, unit_amount::text, line_amount::text
FROM order_items WHERE order_id = ?`, c.OrderID).Scan(&name, &ver, &interval, &ic, &quotas, &gotPrice, &gotVersion, &unit, &line); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "测试套餐-") || ver != 1 || interval != "month" || ic != 1 {
		t.Fatalf("快照 name=%s ver=%d interval=%s count=%d", name, ver, interval, ic)
	}
	if gotPrice != priceID || gotVersion != versionID {
		t.Fatalf("订单行指向 price=%s version=%s", gotPrice, gotVersion)
	}
	if !strings.Contains(quotas, catalogsvc.MetricTraffic) {
		t.Fatalf("配额快照缺流量: %s", quotas)
	}
	mustEq(t, "unit_amount", dec(unit), dec("30"))
	mustEq(t, "line_amount", dec(line), dec("30"))
}

// 新购 + 券 + 部分余额：折扣、余额、应付正确；冻结与券各 held 一笔。
func TestCreateNewOrderCouponAndPartialBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
	code := couponCode()
	couponID := e.mkCoupon(t, ctx, code, 500)
	e.giveBalance(t, ctx, user, 1000)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("10"), CouponCode: strings.ToLower(code)})
	mustEq(t, "Discount", c.Discount, dec("5"))
	mustEq(t, "Total", c.Total, dec("25"))
	mustEq(t, "BalanceApplied", c.BalanceApplied, dec("10"))
	mustEq(t, "Payable", c.Payable, dec("15"))

	o := e.order(t, ctx, c.OrderID)
	if o.CouponID == nil || *o.CouponID != couponID {
		t.Fatalf("订单券 %v，期望 %s", o.CouponID, couponID)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM balance_holds WHERE order_id = ? AND status = 'held' AND amount = 10`, c.OrderID); n != 1 {
		t.Fatalf("冻结 held 行 %d", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM coupon_redemptions WHERE order_id = ? AND status = 'held' AND discount_amount = 5`, c.OrderID); n != 1 {
		t.Fatalf("券占用 held 行 %d", n)
	}
	mustEq(t, "冻结后可用余额", e.balanceOf(t, ctx, user), dec("0"))
	e.assertNoDrift(t, ctx)
}

// 新购、余额全额付（应付 0）：当场履约，开出订阅，order_paid 1 笔，券 captured。
func TestCreateNewOrderZeroPayFulfils(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
	code := couponCode()
	e.mkCoupon(t, ctx, code, 500)
	e.giveBalance(t, ctx, user, 5000)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("50"), CouponCode: code})
	if c.Status != StatusFulfilled {
		t.Fatalf("状态 %s，期望 fulfilled", c.Status)
	}
	mustEq(t, "Total", c.Total, dec("15"))
	mustEq(t, "BalanceApplied", c.BalanceApplied, dec("15"))
	mustEq(t, "Payable", c.Payable, dec("0"))

	o := e.order(t, ctx, c.OrderID)
	mustEq(t, "paid_amount", o.PaidAmount, dec("15"))
	if o.PaidAt == nil || o.FulfilledAt == nil || o.SubscriptionID == nil {
		t.Fatalf("履约形状不对: paid_at=%v fulfilled_at=%v sub=%v", o.PaidAt, o.FulfilledAt, o.SubscriptionID)
	}
	var subID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM subscriptions WHERE source_order_id = ?`, c.OrderID).Scan(&subID); err != nil {
		t.Fatalf("没有开出订阅: %v", err)
	}
	if subID != *o.SubscriptionID {
		t.Fatalf("订单记的订阅 %s ≠ 开出的 %s", *o.SubscriptionID, subID)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 1 {
		t.Fatalf("order_paid %d 笔", n)
	}
	if st := e.strCol(t, ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, c.OrderID); st != "captured" {
		t.Fatalf("券 %s", st)
	}
	if st := e.reservationState(t, ctx, c.OrderID); st != ReservationCaptured {
		t.Fatalf("预留 %s", st)
	}
	mustEq(t, "剩余余额", e.balanceOf(t, ctx, user), dec("35"))
	e.assertNoDrift(t, ctx)
}

// 同套餐已有可续的订阅：不带 new_copy 回 ErrSamePlanUseRenewal；带 new_copy 并起名成功。
func TestCreateNewOrderSamePlanNeedsNewCopy(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month")
	e.provision(t, ctx, user, planID, versionID, priceID, 1000)

	_, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID})
	if !errors.Is(err, ErrSamePlanUseRenewal) {
		t.Fatalf("期望 ErrSamePlanUseRenewal，得到 %v", err)
	}
	// 另买一份不起名：已有一份没起名的同款，要求先起名
	_, err = e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, NewCopy: true})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("不起名另买一份应 422，得到 %v", err)
	}
	c, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, NewCopy: true, Label: "第二份"})
	if err != nil {
		t.Fatalf("new_copy 新购: %v", err)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.SubscriptionLabel == nil || *o.SubscriptionLabel != "第二份" {
		t.Fatalf("备注名 %v", o.SubscriptionLabel)
	}
}

// 同套餐已有待付新购单：回 order_pending，Fields 带 order_id。
func TestCreateNewOrderPendingConflict(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	first := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	_, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID})
	ee := errs.As(err)
	if ee.Code != errs.OrderPending {
		t.Fatalf("期望 order_pending，得到 %v", err)
	}
	if ee.Fields["order_id"] != first.OrderID.String() {
		t.Fatalf("Fields.order_id=%q，期望 %s", ee.Fields["order_id"], first.OrderID)
	}
}

// 已有同款一次性套餐：不带 new_copy 回 ErrSamePlanUseStack（让用户选叠加或另开）；带 new_copy 另开一份成功。
func TestCreateNewOrderSameOneTimeNeedsStack(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
	e.giveBalance(t, ctx, user, 1000)
	first := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("10")})
	if first.Status != StatusFulfilled {
		t.Fatalf("第一单应余额全额付清，状态 %s", first.Status)
	}

	_, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID})
	if !errors.Is(err, ErrSamePlanUseStack) {
		t.Fatalf("期望 ErrSamePlanUseStack，得到 %v", err)
	}
	c, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, NewCopy: true, Label: "另一份"})
	if err != nil {
		t.Fatalf("new_copy 新购一次性套餐: %v", err)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.SubscriptionLabel == nil || *o.SubscriptionLabel != "另一份" {
		t.Fatalf("备注名 %v", o.SubscriptionLabel)
	}
}

// Expect：金额不符或报价时刻早于 10 分钟回 ErrQuoteChanged；与报价一致时成功。
func TestCreateNewOrderExpect(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
	code := couponCode()
	e.mkCoupon(t, ctx, code, 500)
	e.giveBalance(t, ctx, user, 800)

	q, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteNew, PlanID: &planID, CouponCode: code})
	if err != nil {
		t.Fatalf("报价: %v", err)
	}
	if len(q.Quotes) != 1 {
		t.Fatalf("报价 %d 条，期望 1", len(q.Quotes))
	}
	line := q.Quotes[0]
	mustEq(t, "报价 Total", line.Total, dec("25"))
	mustEq(t, "报价可用余额", q.Balance, dec("8"))
	mustEq(t, "报价用余额", line.WithBalance.Applied, dec("8"))
	if line.CouponError != nil {
		t.Fatalf("券不该报错: %s", *line.CouponError)
	}

	wrong := &Expect{AsOf: q.AsOf, Total: line.Total, BalanceApplied: line.WithBalance.Applied, Payable: line.WithBalance.Payable.Add(dec("0.01"))}
	_, err = e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID,
		Checkout: Checkout{UseBalance: q.Balance, CouponCode: code, Expect: wrong}})
	if !errors.Is(err, ErrQuoteChanged) {
		t.Fatalf("金额不符应 ErrQuoteChanged，得到 %v", err)
	}
	stale := &Expect{AsOf: time.Now().Add(-11 * time.Minute), Total: line.Total, BalanceApplied: line.WithBalance.Applied, Payable: line.WithBalance.Payable}
	_, err = e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID,
		Checkout: Checkout{UseBalance: q.Balance, CouponCode: code, Expect: stale}})
	if !errors.Is(err, ErrQuoteChanged) {
		t.Fatalf("报价过旧应 ErrQuoteChanged，得到 %v", err)
	}
	good := &Expect{AsOf: q.AsOf, Total: line.Total, BalanceApplied: line.WithBalance.Applied, Payable: line.WithBalance.Payable}
	c, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID,
		Checkout: Checkout{UseBalance: q.Balance, CouponCode: code, Expect: good}})
	if err != nil {
		t.Fatalf("与报价一致应成功: %v", err)
	}
	mustEq(t, "Payable", c.Payable, line.WithBalance.Payable)
}

// 续费：订单指向订阅、订单行用订阅自己的版本；同一订阅第二张续费单回 ErrOpenChange。
func TestCreateRenewalOrder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, v1, priceID := e.mkPlan(t, ctx, 1000, "month")
	sub := e.provision(t, ctx, user, planID, v1, priceID, 1000)

	// 套餐发布了第二版并切为当前版本：续费仍用订阅自己的第一版
	var v2 uuid.UUID
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 2, 5) RETURNING id`, planID).Scan(&v2); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, v2)
	e.mustExec(t, ctx, `UPDATE plans SET current_version_id = ? WHERE id = ?`, v2, planID)

	c, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("续费建单: %v", err)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.Kind != KindRenewal || o.SubscriptionID == nil || *o.SubscriptionID != sub.ID {
		t.Fatalf("续费单形状不对: kind=%s sub=%v", o.Kind, o.SubscriptionID)
	}
	var itemVersion uuid.UUID
	var vno int
	if err := e.row(ctx, `SELECT plan_version_id, snapshot_plan_version FROM order_items WHERE order_id = ?`, c.OrderID).Scan(&itemVersion, &vno); err != nil {
		t.Fatal(err)
	}
	if itemVersion != v1 || vno != 1 {
		t.Fatalf("订单行版本 %s(v%d)，期望订阅自己的 %s(v1)", itemVersion, vno, v1)
	}

	_, err = e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID, PriceID: &priceID})
	if !errors.Is(err, ErrOpenChange) {
		t.Fatalf("第二张续费单应 ErrOpenChange，得到 %v", err)
	}
	// 别人的订阅：404
	_, err = e.svc.CreateRenewalOrder(asUser(ctx, e.mkUser(t, ctx)), RenewalOrderInput{SubscriptionID: sub.ID, PriceID: &priceID})
	if codeOf(err) != errs.NotFound {
		t.Fatalf("续别人的订阅应 404，得到 %v", err)
	}
}

// 充值：1.00 成功、0.99 与 50000.01 回 422。
func TestCreateTopup(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)

	c, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("1.00")})
	if err != nil {
		t.Fatalf("充值 1.00: %v", err)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.Kind != KindTopup || o.Status != StatusPending || o.SubscriptionID != nil {
		t.Fatalf("充值单形状不对: %+v", o)
	}
	mustEq(t, "payable", o.PayableAmount, dec("1"))
	if n := e.count(t, ctx, `SELECT count(*) FROM order_items WHERE order_id = ?`, c.OrderID); n != 0 {
		t.Fatalf("充值单不该有订单行，得到 %d", n)
	}

	for _, amt := range []string{"0.99", "50000.01"} {
		_, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec(amt)})
		if codeOf(err) != errs.ValidationFailed {
			t.Fatalf("充值 %s 应 422，得到 %v", amt, err)
		}
	}
	if _, err := e.svc.CreateTopup(uctx, TopupInput{Amount: dec("50000.00")}); err != nil {
		t.Fatalf("充值上限 50000.00 应成功: %v", err)
	}
}

// 幂等：ctx 里有真认领时，建单在同一事务里把认领完成并绑定到订单。
func TestCreateNewOrderCompletesIdempotencyClaim(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	uctx := asUser(ctx, user)

	key := "idem-" + randHex(16)
	fp := idemsvc.NewFingerprint([]byte("test-fp-key"), "POST", "/api/v1/orders", []byte(`{"plan_id":"x"}`))
	claim, replay, err := e.idem.Claim(uctx, "order_create", key, fp)
	if err != nil || claim == nil || replay != nil {
		t.Fatalf("认领: claim=%v replay=%v err=%v", claim, replay, err)
	}
	c, err := e.svc.CreateNewOrder(idemsvc.WithClaim(uctx, claim), NewOrderInput{
		PlanID: planID, PriceID: priceID,
		Reply: func(c Created) (idemsvc.Response, error) {
			return idemsvc.JSON(201, map[string]string{"id": c.OrderID.String()})
		},
	})
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	var status, resType string
	var resID uuid.UUID
	var code int
	if err := e.row(ctx, `
SELECT status, resource_type, resource_id, response_code FROM idempotency_keys
WHERE scope = 'order_create' AND actor_id = ? AND idempotency_key = ?`, user, key).Scan(&status, &resType, &resID, &code); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || resType != ResourceOrder || resID != c.OrderID || code != 201 {
		t.Fatalf("幂等记录 status=%s resource=%s/%s code=%d", status, resType, resID, code)
	}

	// 同一个键再来：拿到可重放的响应，不再建单
	_, replay, err = e.idem.Claim(uctx, "order_create", key, fp)
	if err != nil || replay == nil {
		t.Fatalf("重放: replay=%v err=%v", replay, err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ?`, user); n != 1 {
		t.Fatalf("订单 %d 张，期望 1", n)
	}
}

// 报价：未登录 401；不支持的动作 422。
func TestQuoteRejects(t *testing.T) {
	e, ctx := newEnv(t)
	if _, err := e.svc.Quote(ctx, QuoteInput{Action: QuoteNew}); codeOf(err) != errs.Unauthorized {
		t.Fatalf("未登录应 401，得到 %v", err)
	}
	user := e.mkUser(t, ctx)
	for _, a := range []string{QuoteChange, "bogus"} {
		if _, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: a}); codeOf(err) != errs.ValidationFailed {
			t.Fatalf("动作 %s 应 422，得到 %v", a, err)
		}
	}
}

// 续费时订阅没有关联价格档、请求也没带 price_id：回 409。
func TestCreateRenewalWithoutPrice(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, _ := e.mkPlan(t, ctx, 1000, "month")
	sub, err := e.subs.Provision(ctx, subsvc.Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: subsvc.PriceSnapshot{Amount: dec("10"), Interval: "month", IntervalCount: 1},
		Actor: subsvc.Actor{Kind: subsvc.ActorSystem},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	ee := errs.As(err)
	if ee.Code != errs.Conflict || ee.Message != "这条订阅没有关联价格，无法续费" {
		t.Fatalf("期望 409「没有关联价格」，得到 %v", err)
	}
}
