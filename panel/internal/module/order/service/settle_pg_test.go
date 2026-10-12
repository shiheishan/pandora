package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/provider/demo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// spyHook 记下 PaidHook 的调用；fail 非空时返回它（模拟佣金写入失败）。
type spyHook struct {
	mu    sync.Mutex
	calls []PaidOrder
	fail  error
}

func (h *spyHook) OnOrderPaid(_ context.Context, o PaidOrder) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, o)
	return h.fail
}

func (h *spyHook) snapshot() []PaidOrder {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]PaidOrder(nil), h.calls...)
}

// demoInboundFee 同 demoInbound，带渠道手续费（分）。
func demoInboundFee(eventID, paymentRef, orderNo string, cents, feeCents int64) provider.Inbound {
	body, _ := json.Marshal(map[string]any{
		"event_id":   eventID,
		"event_type": "payment.succeeded",
		"payment_id": paymentRef,
		"order_no":   orderNo,
		"amount":     cents,
		"currency":   "CNY",
		"fee":        feeCents,
		"method":     "alipay",
	})
	h := http.Header{}
	h.Set("X-Aegis-Signature", demo.Sign([]byte(demoSecret), eventID, "payment.succeeded", paymentRef, orderNo, cents, feeCents, "CNY"))
	return provider.Inbound{Method: http.MethodPost, Header: h, Body: body}
}

// lateCase 读订单的挂账案（没有时 kind 为空）。
func (e *env) lateCase(t *testing.T, ctx context.Context, orderID uuid.UUID) (kind, orderStatus string, amount decimal.Decimal) {
	t.Helper()
	var amt string
	err := e.row(ctx, `SELECT case_kind, order_status, amount::text FROM late_payment_cases WHERE order_id = ?`, orderID).
		Scan(&kind, &orderStatus, &amt)
	if err != nil {
		return "", "", decimal.Zero
	}
	return kind, orderStatus, dec(amt)
}

// platformBalance 读平台科目（按 owner_ref）的余额。
func (e *env) platformBalance(t *testing.T, ctx context.Context, typ ledgersvc.AccountType, ref string) decimal.Decimal {
	t.Helper()
	s := e.strCol(t, ctx, `SELECT balance::text FROM ledger_accounts WHERE account_type = ? AND owner_ref = ? AND currency = 'CNY' AND owner_user_id IS NULL`,
		string(typ), ref)
	if s == "" {
		return decimal.Zero
	}
	return dec(s)
}

// 新购待付单（券 + 部分余额）→ 发起支付 → 回调：履约、order_paid 1 笔、库存转已售、冻结与券 captured。
func TestWebhookSettlesNewOrder(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user, planID, orderID := e.pendingWithEverything(t, ctx)

	ack := e.payAndNotify(t, ctx, user, orderID, code, "pay-"+randHex(8))
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回执 %d: %s", ack.HTTPStatus, ack.Body)
	}
	o := e.order(t, ctx, orderID)
	if o.Status != StatusFulfilled || o.SubscriptionID == nil {
		t.Fatalf("订单 status=%s sub=%v", o.Status, o.SubscriptionID)
	}
	mustEq(t, "paid_amount = total", o.PaidAmount, o.TotalAmount)
	if n := e.count(t, ctx, `SELECT count(*) FROM subscriptions WHERE source_order_id = ? AND user_id = ?`, orderID, user); n != 1 {
		t.Fatalf("开出订阅 %d 份", n)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, orderID); n != 1 {
		t.Fatalf("order_paid %d 笔", n)
	}
	if r, s := e.stock(t, ctx, planID); r != 0 || s != 1 {
		t.Fatalf("库存 reserved=%d sold=%d", r, s)
	}
	if p, r := e.counter(t, ctx, planID, user); p != 1 || r != 0 {
		t.Fatalf("限购 purchased=%d reserved=%d", p, r)
	}
	if st := e.strCol(t, ctx, `SELECT status FROM balance_holds WHERE order_id = ?`, orderID); st != "captured" {
		t.Fatalf("冻结 %s", st)
	}
	if st := e.strCol(t, ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, orderID); st != "captured" {
		t.Fatalf("券 %s", st)
	}
	if st := e.reservationState(t, ctx, orderID); st != ReservationCaptured {
		t.Fatalf("预留 %s", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM order_reservation_events WHERE order_id = ? AND event_kind = 'capture' AND actor_kind = 'payment'`, orderID); n != 1 {
		t.Fatalf("capture 流水应由 payment 写")
	}
	mustEq(t, "余额已扣", e.balanceOf(t, ctx, user), dec("0"))
	e.assertNoDrift(t, ctx)
}

// 同一回调重放 10 次只记一笔账。
func TestWebhookReplaySettlesOnce(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 990, "month", nil, nil)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	in := demoInbound("evt-replay", "payment.succeeded", "pay-replay", intent.OutTradeNo, 990)
	for i := 0; i < 10; i++ {
		ack, err := e.payments.HandleWebhook(ctx, code, in)
		if err != nil || ack.HTTPStatus != http.StatusOK {
			t.Fatalf("第 %d 次回调 %d %v", i+1, ack.HTTPStatus, err)
		}
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 1 {
		t.Fatalf("order_paid %d 笔", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, c.OrderID); n != 1 {
		t.Fatalf("payments %d 行", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM subscriptions WHERE source_order_id = ?`, c.OrderID); n != 1 {
		t.Fatalf("订阅 %d 份", n)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("订单 %s", st)
	}
}

// 续费单结算：订阅到期时间后移一期；订阅已停用时转挂账 ineligible_subscription，订单仍待付。
func TestWebhookSettlesRenewal(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)

	c, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ack := e.payAndNotify(t, ctx, user, c.OrderID, code, "pay-"+randHex(8)); ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回执 %d", ack.HTTPStatus)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("续费单 %s", st)
	}
	after, err := e.subs.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !after.PeriodEnd.After(sub.PeriodEnd.Add(27 * 24 * time.Hour)) {
		t.Fatalf("到期 %s 应比原来的 %s 后移约一个月", after.PeriodEnd, sub.PeriodEnd)
	}

	// 第二张续费单：付款前订阅被停用
	c2, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c2.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `UPDATE subscriptions SET status = 'expired', renewal_closed_at = now() WHERE id = ?`, sub.ID)
	ack, err := e.payments.HandleWebhook(ctx, code, demoInbound("evt-"+randHex(8), "payment.succeeded", "pay-"+randHex(8), intent.OutTradeNo, 1000))
	if err != nil || ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回调 %d %v", ack.HTTPStatus, err)
	}
	kind, ostatus, amt := e.lateCase(t, ctx, c2.OrderID)
	if kind != "ineligible_subscription" || ostatus != StatusPending {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	mustEq(t, "挂账金额", amt, dec("10"))
	if st := e.order(t, ctx, c2.OrderID).Status; st != StatusPending {
		t.Fatalf("订单应仍待付，得到 %s", st)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c2.OrderID); n != 0 {
		t.Fatalf("不应记 order_paid，得到 %d 笔", n)
	}
	e.assertNoDrift(t, ctx)
}

// 订单取消后回调到账：挂账 released_order，订单仍 cancelled，没有开订阅。
func TestWebhookAfterCancelGoesToSuspense(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user, _, orderID := e.pendingWithEverything(t, ctx)
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: orderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: orderID}); err != nil {
		t.Fatal(err)
	}
	ack, err := e.payments.HandleWebhook(ctx, code, demoInbound("evt-"+randHex(8), "payment.succeeded", "pay-"+randHex(8), intent.OutTradeNo, 1500))
	if err != nil || ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回调 %d %v", ack.HTTPStatus, err)
	}
	kind, ostatus, amt := e.lateCase(t, ctx, orderID)
	if kind != "released_order" || ostatus != StatusCancelled {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	mustEq(t, "挂账金额", amt, dec("15"))
	if st := e.order(t, ctx, orderID).Status; st != StatusCancelled {
		t.Fatalf("订单 %s", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM subscriptions WHERE source_order_id = ?`, orderID); n != 0 {
		t.Fatalf("不该开订阅，得到 %d 份", n)
	}
	mustEq(t, "余额已在取消时退回", e.balanceOf(t, ctx, user), dec("10"))
	e.assertNoDrift(t, ctx)
}

// 已履约的单再来一笔不同的渠道收款：挂账 excess_capture。
func TestWebhookExcessCapture(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1200, "month", nil, nil)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range []string{"pay-a-" + randHex(6), "pay-b-" + randHex(6)} {
		ack, err := e.payments.HandleWebhook(ctx, code, demoInbound("evt-"+ref, "payment.succeeded", ref, intent.OutTradeNo, 1200))
		if err != nil || ack.HTTPStatus != http.StatusOK {
			t.Fatalf("第 %d 笔回调 %d %v", i+1, ack.HTTPStatus, err)
		}
	}
	kind, ostatus, amt := e.lateCase(t, ctx, c.OrderID)
	if kind != "excess_capture" || ostatus != StatusFulfilled {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	mustEq(t, "挂账金额", amt, dec("12"))
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 1 {
		t.Fatalf("order_paid %d 笔", n)
	}
	e.assertNoDrift(t, ctx)
}

// 回调金额与应付不符：payment 回 409，订单仍待付、没有收款行。
func TestWebhookAmountMismatchRejected(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := e.payments.HandleWebhook(ctx, code, demoInbound("evt-"+randHex(8), "payment.succeeded", "pay-"+randHex(8), intent.OutTradeNo, 999))
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusConflict {
		t.Fatalf("金额不符应 409，得到 %d: %s", ack.HTTPStatus, ack.Body)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单 %s", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, c.OrderID); n != 0 {
		t.Fatalf("不应有收款行，得到 %d", n)
	}
}

// 充值单结算：用户余额 + 金额，渠道净收款与手续费分别入科目，没有 order_paid。
func TestWebhookSettlesTopup(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	hook := &spyHook{}
	e.svc.UsePaidHook(hook)

	c, err := e.svc.CreateTopup(asUser(ctx, user), TopupInput{Amount: dec("100.00")})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := e.payments.HandleWebhook(ctx, code, demoInboundFee("evt-"+randHex(8), "pay-"+randHex(8), intent.OutTradeNo, 10000, 60))
	if err != nil || ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回调 %d %v", ack.HTTPStatus, err)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("充值单 %s", st)
	}
	mustEq(t, "用户余额", e.balanceOf(t, ctx, user), dec("100"))
	mustEq(t, "渠道净收款", e.platformBalance(t, ctx, ledgersvc.ChannelCash, code), dec("99.40"))
	mustEq(t, "手续费", e.platformBalance(t, ctx, ledgersvc.PlatformFeeExpense, code), dec("0.60"))
	if n := e.txnCount(t, ctx, ledgersvc.KindBalanceTopup, c.OrderID); n != 1 {
		t.Fatalf("balance_topup %d 笔", n)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 0 {
		t.Fatalf("充值不该记 order_paid，得到 %d", n)
	}
	if len(hook.snapshot()) != 0 {
		t.Fatal("充值单不该调 PaidHook")
	}
	e.assertNoDrift(t, ctx)
}

// 流量包单结算：traffic_pack_grants 挂在目标订阅上。
func TestWebhookSettlesPack(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)
	packID := e.mkPack(t, ctx, 600, 20*gib)
	c, err := e.svc.CreatePackOrder(asUser(ctx, user), PackOrderInput{PackID: packID, SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ack := e.payAndNotify(t, ctx, user, c.OrderID, code, "pay-"+randHex(8)); ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回执 %d", ack.HTTPStatus)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM traffic_pack_grants WHERE source = 'order' AND source_id = ? AND subscription_id = ? AND granted_bytes = ?`,
		c.OrderID, sub.ID, 20*gib); n != 1 {
		t.Fatalf("流量包发放 %d 行", n)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("订单 %s", st)
	}
}

// 后台标记已支付：待付单履约、offline 渠道一行收款；已取消的单 409 且不开挂账；门户主体 403。
func TestMarkPaid(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	reason := "用户线下转账已核对"

	if _, err := e.svc.MarkPaid(asUser(ctx, admin), MarkPaidInput{OrderID: c.OrderID, Reason: reason}); codeOf(err) != errs.Forbidden {
		t.Fatalf("门户主体应 403，得到 %v", err)
	}
	if _, err := e.svc.MarkPaid(actx, MarkPaidInput{OrderID: c.OrderID, Reason: "短"}); codeOf(err) != errs.ValidationFailed {
		t.Fatalf("原因太短应 422，得到 %v", err)
	}
	res, err := e.svc.MarkPaid(actx, MarkPaidInput{OrderID: c.OrderID, Reason: reason, Reference: "  BANK-REF-1 "})
	if err != nil {
		t.Fatalf("标记已支付: %v", err)
	}
	if !res.Processed || res.PaymentID == nil || res.SubscriptionID == nil {
		t.Fatalf("结果 %+v", res)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.Status != StatusFulfilled || o.SubscriptionID == nil || *o.SubscriptionID != *res.SubscriptionID {
		t.Fatalf("订单 status=%s sub=%v", o.Status, o.SubscriptionID)
	}
	if n := e.count(t, ctx, `
SELECT count(*) FROM payments p JOIN payment_providers pp ON pp.id = p.provider_id
WHERE p.order_id = ? AND pp.code = 'offline'`, c.OrderID); n != 1 {
		t.Fatalf("offline 收款 %d 行", n)
	}
	if _, err := e.svc.MarkPaid(actx, MarkPaidInput{OrderID: c.OrderID, Reason: reason}); codeOf(err) != errs.Conflict {
		t.Fatalf("已履约的单再标记应 409，得到 %v", err)
	}

	c2 := e.buyNew(t, ctx, e.mkUser(t, ctx), planID, priceID, Checkout{})
	v := int64(1)
	if _, err := e.svc.Cancel(actx, CancelInput{OrderID: c2.OrderID, Admin: true, ExpectedStateVersion: &v, Reason: reason}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MarkPaid(actx, MarkPaidInput{OrderID: c2.OrderID, Reason: reason}); codeOf(err) != errs.Conflict {
		t.Fatalf("已取消的单应 409，得到 %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, c2.OrderID); n != 0 {
		t.Fatalf("不该开挂账，得到 %d", n)
	}
	e.assertNoDrift(t, ctx)
}

// 查单：查别人的订单 404；门户主体调后台查单 403。
// demo 渠道不支持主动查单，「查到已付即补记」只能靠 payment 自己的查单测试与上线前用真实渠道核对。
func TestQueryOwnership(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	if _, err := e.svc.Query(asUser(ctx, e.mkUser(t, ctx)), c.OrderID); codeOf(err) != errs.NotFound {
		t.Fatalf("查别人的订单应 404，得到 %v", err)
	}
	if _, err := e.svc.AdminQuery(asUser(ctx, user), c.OrderID); codeOf(err) != errs.Forbidden {
		t.Fatalf("门户主体调后台查单应 403，得到 %v", err)
	}
	if _, err := e.svc.AdminQuery(asAdmin(ctx, e.mkUser(t, ctx)), uuid.Must(uuid.NewV7())); codeOf(err) != errs.NotFound {
		t.Fatalf("后台查不存在的订单应 404，得到 %v", err)
	}
}

// PaidHook：渠道结算调用 1 次（Total 含余额、Paid 是渠道实收）；零应付单不调用。
func TestPaidHookCalls(t *testing.T) {
	e, ctx := newEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	hook := &spyHook{}
	e.svc.UsePaidHook(hook)
	user, _, orderID := e.pendingWithEverything(t, ctx)

	if ack := e.payAndNotify(t, ctx, user, orderID, code, "pay-"+randHex(8)); ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回执 %d", ack.HTTPStatus)
	}
	calls := hook.snapshot()
	if len(calls) != 1 {
		t.Fatalf("PaidHook 调用 %d 次，期望 1", len(calls))
	}
	got := calls[0]
	if got.OrderID != orderID || got.UserID != user || got.Kind != KindNew || got.PaymentID == uuid.Nil {
		t.Fatalf("PaidHook 入参 %+v", got)
	}
	mustEq(t, "Total（含余额）", got.Total, dec("25"))
	mustEq(t, "Paid（渠道实收）", got.Paid, dec("15"))

	zeroUser := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	e.giveBalance(t, ctx, zeroUser, 1000)
	if c := e.buyNew(t, ctx, zeroUser, planID, priceID, Checkout{UseBalance: dec("10")}); c.Status != StatusFulfilled {
		t.Fatalf("零应付单 %s", c.Status)
	}
	if n := len(hook.snapshot()); n != 1 {
		t.Fatalf("零应付单不该调 PaidHook，累计 %d 次", n)
	}
}

// PaidHook 返回错误时整笔结算回滚（真提交，看 payment 的事务真的没落库），之后重发回调能正常结算。
func TestPaidHookErrorRollsBack(t *testing.T) {
	e, ctx := newCommitEnv(t)
	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	hook := &spyHook{fail: errors.New("佣金写入失败")}
	e.svc.UsePaidHook(hook)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	in := demoInbound("evt-hook-"+randHex(6), "payment.succeeded", "pay-hook-"+randHex(6), intent.OutTradeNo, 1000)
	ack, err := e.payments.HandleWebhook(ctx, code, in)
	if err == nil && ack.HTTPStatus == http.StatusOK {
		t.Fatal("hook 失败时回执不应是 200")
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单应仍待付，得到 %s", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, c.OrderID); n != 0 {
		t.Fatalf("收款行应随事务回滚，得到 %d", n)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 0 {
		t.Fatalf("order_paid 应随事务回滚，得到 %d", n)
	}

	hook.mu.Lock()
	hook.fail = nil
	hook.mu.Unlock()
	ack, err = e.payments.HandleWebhook(ctx, code, in)
	if err != nil || ack.HTTPStatus != http.StatusOK {
		t.Fatalf("重发回调 %d %v", ack.HTTPStatus, err)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("重发后订单 %s", st)
	}
	e.assertNoDrift(t, ctx)
}
