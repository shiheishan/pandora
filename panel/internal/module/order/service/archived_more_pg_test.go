// 套餐归档后才付款（用户 2026-10-16）的补充用例：换套餐单、归档后不让再发起付款、
// 以及 CloseArchivedOrder 遇到已不是待付的订单只走 settle、不再动冻结。
package service

import (
	"context"
	"errors"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 换套餐（升级）单在目标套餐归档后付款：不换套餐，应付进余额。
func TestArchivedPlanUpgradePaidToBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 90)
	e.usePaidTime(t, ctx, sub.ID, 1, 2)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month")
	_, exp := e.changeQuote(t, ctx, user, sub.ID, planB)
	c, err := e.svc.CreatePlanChangeOrder(asUser(ctx, user), PlanChangeOrderInput{
		SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB, Checkout: Checkout{Expect: exp},
	})
	if err != nil {
		t.Fatalf("换套餐下单: %v", err)
	}
	if o := e.order(t, ctx, c.OrderID); o.Kind != KindUpgrade {
		t.Fatalf("订单 kind=%s，期望 upgrade", o.Kind)
	}
	mustEq(t, "Payable", c.Payable, dec("85"))
	planBefore := e.planOf(t, ctx, sub.ID)

	code := payCode()
	e.mkPayProvider(t, ctx, code)
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	e.archivePlan(t, ctx, planB)

	balBefore := e.balanceOf(t, ctx, user)
	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("up-"+randHex(8), intent.OutTradeNo, 8500))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}

	e.assertArchivedRefund(t, ctx, c.OrderID, dec("85"))
	mustEq(t, "余额增加", e.balanceOf(t, ctx, user).Sub(balBefore), dec("85"))
	if p := e.planOf(t, ctx, sub.ID); p != planBefore {
		t.Fatalf("订阅套餐变成 %s，应仍是 %s", p, planBefore)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 0 {
		t.Fatalf("不应记 plan_change_refund，得到 %d 笔", n)
	}
	e.assertNoDrift(t, ctx)
}

// 套餐归档后，归档前下的待付单不能再发起付款：409 order_lapsed、原样文案、带 reason，不建新意图。
func TestPayRefusedAfterPlanArchived(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	e.archivePlan(t, ctx, planID)

	e.mustExec(t, ctx, "SAVEPOINT pay_archived")
	_, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	e.mustExec(t, ctx, "ROLLBACK TO SAVEPOINT pay_archived")

	if !errors.Is(err, ErrPlanRetired) {
		t.Fatalf("应回 ErrPlanRetired，得到 %v", err)
	}
	pe := errs.As(err)
	if pe.Code != errs.OrderLapsed || pe.Status() != 409 {
		t.Fatalf("应 409 order_lapsed，得到 %s %d", pe.Code, pe.Status())
	}
	if pe.Message != "套餐已停售，不能再付款，可以取消这张订单" || pe.Fields["reason"] != "plan_archived" {
		t.Fatalf("文案 %q fields %v", pe.Message, pe.Fields)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ?`, c.OrderID); n != 0 {
		t.Fatalf("不应建意图，得到 %d 条", n)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单仍应待付（用户可以取消），得到 %s", st)
	}
	// 用户照样能取消
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID}); err != nil {
		t.Fatalf("取消: %v", err)
	}
	e.assertNoDrift(t, ctx)
}

// CloseArchivedOrder 遇到已取消的单（钱晚于取消到账）：只调一次 settle、带当前状态，
// 不改取消原因、不再释放冻结（取消时已释放过一次）。
func TestCloseArchivedOrderNotPendingOnlySettles(t *testing.T) {
	e, ctx := newEnv(t)
	user, _, orderID := e.pendingWithEverything(t, ctx)
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: orderID}); err != nil {
		t.Fatalf("取消: %v", err)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindBalanceRelease, orderID); n != 1 {
		t.Fatalf("取消后 balance_release %d 笔，期望 1", n)
	}
	balBefore := e.balanceOf(t, ctx, user)

	var calls int
	var gotStatus string
	err := e.svc.CloseArchivedOrder(ctx, orderID, func(_ context.Context, orderStatus string) error {
		calls++
		gotStatus = orderStatus
		return nil
	})
	if err != nil {
		t.Fatalf("CloseArchivedOrder: %v", err)
	}
	if calls != 1 {
		t.Fatalf("settle 调用 %d 次，期望 1", calls)
	}
	if gotStatus != StatusCancelled {
		t.Fatalf("settle 收到状态 %q，期望 %s", gotStatus, StatusCancelled)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindBalanceRelease, orderID); n != 1 {
		t.Fatalf("balance_release %d 笔，冻结只应释放一次", n)
	}
	mustEq(t, "余额不变", e.balanceOf(t, ctx, user), balBefore)
	o := e.order(t, ctx, orderID)
	if o.Status != StatusCancelled || o.CancelReason == nil || *o.CancelReason != ReasonUserCancelled {
		t.Fatalf("订单 %s / %v，应保持用户取消", o.Status, o.CancelReason)
	}
	e.assertNoDrift(t, ctx)
}
