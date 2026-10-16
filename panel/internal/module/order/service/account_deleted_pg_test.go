package service

import (
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 注销账号时取消该用户全部待付订单：资源全部退回、在途支付意图作废、原因 account_deleted；
// 别人的单不动；之后到的付款回调走迟到付款（挂账 released_order），不开订阅。门户主体调用 403。
func TestCancelUserPendingOnAccountDeletion(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user, planID, orderID := e.pendingWithEverything(t, ctx)
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: orderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	other := e.mkUser(t, ctx)
	otherPlan, _, otherPrice := e.mkPlan(t, ctx, 1000, "month")
	otherOrder := e.buyNew(t, ctx, other, otherPlan, otherPrice, Checkout{})

	if _, err := e.svc.CancelUserPending(asUser(ctx, user), user); codeOf(err) != errs.Forbidden {
		t.Fatalf("门户主体应 403，得到 %v", err)
	}

	actx := asAdmin(ctx, e.mkUser(t, ctx))
	n, err := e.svc.CancelUserPending(actx, user)
	if err != nil || n != 1 {
		t.Fatalf("取消 n=%d err=%v", n, err)
	}
	o := e.order(t, ctx, orderID)
	if o.Status != StatusCancelled || o.CancelReason == nil || *o.CancelReason != ReasonAccountDeleted {
		t.Fatalf("订单 status=%s reason=%v", o.Status, o.CancelReason)
	}
	e.assertReleased(t, ctx, user, planID, orderID, EventCancel)
	if st := e.strCol(t, ctx, `SELECT status FROM payment_intents WHERE order_id = ?`, orderID); st != "cancelled" {
		t.Fatalf("支付意图 %s，期望 cancelled", st)
	}
	if st := e.order(t, ctx, otherOrder.OrderID).Status; st != StatusPending {
		t.Fatalf("别人的单被动了：%s", st)
	}

	if n, err := e.svc.CancelUserPending(actx, user); err != nil || n != 0 {
		t.Fatalf("重复调用 n=%d err=%v，期望 0", n, err)
	}

	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 1500))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	kind, ostatus, _ := e.lateCase(t, ctx, orderID)
	if kind != "released_order" || ostatus != StatusCancelled {
		t.Fatalf("挂账 kind=%q order_status=%q", kind, ostatus)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM subscriptions WHERE source_order_id = ?`, orderID); n != 0 {
		t.Fatalf("不该开订阅，得到 %d 份", n)
	}
	e.assertNoDrift(t, ctx)
}

// 已注销的账号：门户新购、充值与后台开单、开单预览一律 409（账号已注销），不建单。
func TestDeletedUserCannotOrder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	e.users.markDeleted(user)

	if _, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID}); codeOf(err) != errs.Conflict {
		t.Fatalf("门户新购应 409，得到 %v", err)
	}
	if _, err := e.svc.CreateTopup(asUser(ctx, user), TopupInput{Amount: dec("10")}); codeOf(err) != errs.Conflict {
		t.Fatalf("充值应 409，得到 %v", err)
	}
	actx := asAdmin(ctx, e.mkUser(t, ctx))
	if _, err := e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: planID, PriceID: priceID, Reason: manualReason}); codeOf(err) != errs.Conflict {
		t.Fatalf("后台开单应 409，得到 %v", err)
	}
	if _, err := e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: planID, PriceID: &priceID}); codeOf(err) != errs.Conflict {
		t.Fatalf("开单预览应 409，得到 %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ?`, user); n != 0 {
		t.Fatalf("不该建单，得到 %d 张", n)
	}
}
