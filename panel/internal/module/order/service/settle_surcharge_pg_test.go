package service

import (
	"context"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
)

// setPayFee 给渠道设用户手续费（元、百分比）。
func (e *env) setPayFee(t *testing.T, ctx context.Context, code, fixed, percent string) {
	e.mustExec(t, ctx, `UPDATE payment_providers SET fee_fixed = ?, fee_percent = ? WHERE code = ?`, fixed, percent, code)
}

// capturePaid 记下 PaidHook 收到的订单摘要。
type capturePaid struct{ got []PaidOrder }

func (c *capturePaid) OnOrderPaid(_ context.Context, o PaidOrder) error {
	c.got = append(c.got, o)
	return nil
}

// TestTopupSettlesWithSurcharge 充值单：用户余额只加应付，渠道现金与手续费收入各记 surcharge。
func TestTopupSettlesWithSurcharge(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	e.setPayFee(t, ctx, code, "0.30", "2")

	topup, err := e.svc.CreateTopup(asUser(ctx, user), TopupInput{Amount: dec("10.00")})
	if err != nil {
		t.Fatal(err)
	}

	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: topup.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	mustEq(t, "意图金额", intent.Amount, dec("10.50"))
	mustEq(t, "意图 surcharge", intent.Surcharge, dec("0.50"))

	ref := "pay-topup-sc-" + randHex(8)
	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound(ref, intent.OutTradeNo, 1050))
	if err != nil {
		t.Fatal(err)
	}
	if !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}

	mustEq(t, "用户余额", e.balanceOf(t, ctx, user), dec("10.00"))
	mustEq(t, "手续费收入", e.platformBalance(t, ctx, ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge), dec("0.50"))
	mustEq(t, "渠道现金", e.platformBalance(t, ctx, ledgersvc.ChannelCash, code), dec("10.50"))

	var payAmt, paySur string
	if err := e.row(ctx, `SELECT amount::text, surcharge_amount::text FROM payments WHERE order_id = ?`, topup.OrderID).
		Scan(&payAmt, &paySur); err != nil {
		t.Fatal(err)
	}
	mustEq(t, "payments.amount", dec(payAmt), dec("10.50"))
	mustEq(t, "payments.surcharge_amount", dec(paySur), dec("0.50"))

	e.assertNoDrift(t, ctx)
}

// TestNewOrderSettlesWithSurchargeHookExcludesFee 新购：结算成功且佣金钩子拿到的金额不含手续费。
func TestNewOrderSettlesWithSurchargeHookExcludesFee(t *testing.T) {
	e, ctx := newEnv(t)
	hook := &capturePaid{}
	e.svc.UsePaidHook(hook)

	code := payCode()
	e.mkPayProvider(t, ctx, code)
	e.setPayFee(t, ctx, code, "0.30", "2")

	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 990, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	mustEq(t, "意图金额", intent.Amount, dec("10.40"))
	mustEq(t, "意图 surcharge", intent.Surcharge, dec("0.50"))

	ref := "pay-new-sc-" + randHex(8)
	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound(ref, intent.OutTradeNo, 1040))
	if err != nil {
		t.Fatal(err)
	}
	if !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}

	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("订单 %s", st)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 1 {
		t.Fatalf("order_paid %d 笔", n)
	}
	if len(hook.got) != 1 {
		t.Fatalf("PaidHook 调用 %d 次", len(hook.got))
	}
	mustEq(t, "hook.Paid", hook.got[0].Paid, dec("9.90"))
	mustEq(t, "hook.Total", hook.got[0].Total, dec("9.90"))
	mustEq(t, "手续费收入", e.platformBalance(t, ctx, ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge), dec("0.50"))
	e.assertNoDrift(t, ctx)
}

// TestSurchargeUnderpayRejectedOrder 只付应付、不含手续费时拒收，订单仍待付且无 order_paid。
func TestSurchargeUnderpayRejectedOrder(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	e.setPayFee(t, ctx, code, "0.30", "2")

	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 990, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}

	ref := "pay-under-" + randHex(8)
	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound(ref, intent.OutTradeNo, 990))
	if err != nil {
		t.Fatal(err)
	}
	if ackOK(ack) {
		t.Fatalf("少付手续费不应 success: %s", ack.Body)
	}

	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单 %s", st)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, c.OrderID); n != 0 {
		t.Fatalf("order_paid %d 笔", n)
	}
}
