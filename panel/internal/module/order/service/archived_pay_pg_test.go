// 套餐归档后才付款（用户 2026-10-16）：不开通，实付经挂账自动转入用户余额。
// 覆盖续费、叠加、新购（券+余额冻结释放）的线上回调，以及后台线下标记已支付的拒绝路径。
package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// archivePlan 把套餐标为已归档（与线上运营操作一致，测试直接写库）。
func (e *env) archivePlan(t *testing.T, ctx context.Context, planID uuid.UUID) {
	t.Helper()
	e.mustExec(t, ctx,
		`UPDATE plans SET status = 'archived', allow_new_purchase = false, row_version = row_version + 1 WHERE id = ?`,
		planID)
}

// paymentLedgerTxnCount 数某订单关联收款（payments）上、指定 kind 的账务交易笔数。
func (e *env) paymentLedgerTxnCount(t *testing.T, ctx context.Context, kind ledgersvc.Kind, orderID uuid.UUID) int64 {
	t.Helper()
	return e.count(t, ctx, `
		SELECT count(*) FROM ledger_transactions
		WHERE kind = ? AND source_type = 'payment'
		  AND source_id IN (SELECT id FROM payments WHERE order_id = ?)`,
		string(kind), orderID)
}

// assertArchivedRefund 核对归档后付款关单：取消原因、挂账已转余额、账本、预留、不开订阅、意图成功、不计佣。
func (e *env) assertArchivedRefund(t *testing.T, ctx context.Context, orderID uuid.UUID, paid decimal.Decimal) {
	t.Helper()
	o := e.order(t, ctx, orderID)
	if o.Status != StatusCancelled {
		t.Fatalf("订单状态 %s，期望 cancelled", o.Status)
	}
	if reason := e.strCol(t, ctx, `SELECT cancel_reason FROM orders WHERE id = ?`, orderID); reason != ReasonPlanArchived {
		t.Fatalf("cancel_reason=%q，期望 %s", reason, ReasonPlanArchived)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, orderID); n != 1 {
		t.Fatalf("挂账案 %d 条，期望 1", n)
	}
	var caseKind, caseStatus, caseAmt string
	var appliedTxn uuid.UUID
	if err := e.row(ctx, `
		SELECT case_kind, status, amount::text, applied_txn_id
		FROM late_payment_cases WHERE order_id = ?`, orderID).
		Scan(&caseKind, &caseStatus, &caseAmt, &appliedTxn); err != nil {
		t.Fatal(err)
	}
	if caseKind != "archived_plan" {
		t.Fatalf("case_kind=%q，期望 archived_plan", caseKind)
	}
	if caseStatus != "applied" {
		t.Fatalf("挂账案 status=%q，期望 applied", caseStatus)
	}
	mustEq(t, "挂账金额", dec(caseAmt), paid)
	if appliedTxn == uuid.Nil {
		t.Fatal("applied_txn_id 不应为空")
	}
	if n := e.paymentLedgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense, orderID); n != 1 {
		t.Fatalf("late_payment_suspense %d 笔，期望 1", n)
	}
	if n := e.paymentLedgerTxnCount(t, ctx, ledgersvc.KindLatePaymentApplied, orderID); n != 1 {
		t.Fatalf("late_payment_applied %d 笔，期望 1", n)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindOrderPaid, orderID); n != 0 {
		t.Fatalf("不应记 order_paid，得到 %d 笔", n)
	}
	if st := e.reservationState(t, ctx, orderID); st != ReservationReleased {
		t.Fatalf("预留 %s，期望 released", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM subscriptions WHERE source_order_id = ?`, orderID); n != 0 {
		t.Fatalf("不应按本单开通订阅，得到 %d 份", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payment_intents WHERE order_id = ? AND status = 'succeeded'`, orderID); n != 1 {
		t.Fatalf("成功意图 %d 条，期望 1", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, orderID); n != 0 {
		t.Fatalf("不应计佣，得到 %d 条", n)
	}
}

// 续费单在套餐归档后付款：钱进余额，订阅到期不变，PaidHook 不调；同一回调重放幂等。
func TestArchivedPlanRenewalPaidToBalance(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month")
	sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)
	periodEndBefore := sub.PeriodEnd

	c, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: c.OrderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	e.archivePlan(t, ctx, planID)

	hook := &spyHook{}
	e.svc.UsePaidHook(hook)
	balBefore := e.balanceOf(t, ctx, user)

	ref := "pay-" + randHex(8)
	in := epayInbound(ref, intent.OutTradeNo, 1000)
	ack, err := e.payments.HandleWebhook(ctx, code, in)
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}

	e.assertArchivedRefund(t, ctx, c.OrderID, dec("10"))
	mustEq(t, "余额增加", e.balanceOf(t, ctx, user).Sub(balBefore), dec("10"))
	after, err := e.subs.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !after.PeriodEnd.Equal(periodEndBefore) {
		t.Fatalf("续费后 PeriodEnd=%s，应与付款前 %s 相同", after.PeriodEnd, periodEndBefore)
	}
	if len(hook.snapshot()) != 0 {
		t.Fatal("归档关单不应调 PaidHook")
	}

	for i := 0; i < 3; i++ {
		ack, err := e.payments.HandleWebhook(ctx, code, in)
		if err != nil || !ackOK(ack) {
			t.Fatalf("第 %d 次重放回调 %s %v", i+1, ack.Body, err)
		}
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, c.OrderID); n != 1 {
		t.Fatalf("payments %d 行，重放后仍应 1", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, c.OrderID); n != 1 {
		t.Fatalf("挂账案 %d 条，重放后仍应 1", n)
	}
	mustEq(t, "重放后余额不再增加", e.balanceOf(t, ctx, user).Sub(balBefore), dec("10"))
	if n := e.paymentLedgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense, c.OrderID); n != 1 {
		t.Fatalf("late_payment_suspense %d 笔，重放后仍应 1", n)
	}
	if n := e.paymentLedgerTxnCount(t, ctx, ledgersvc.KindLatePaymentApplied, c.OrderID); n != 1 {
		t.Fatalf("late_payment_applied %d 笔，重放后仍应 1", n)
	}
	e.assertNoDrift(t, ctx)
}

// 叠加单在套餐归档后付款：钱进余额，订阅期末与 total 流量上限不变。
func TestArchivedPlanStackPaidToBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
	sub := provisionOneTime(t, ctx, e, user, planID, versionID, priceID, 1000, intp(30))
	periodEndBefore := sub.PeriodEnd

	var limitBefore int64
	if err := e.row(ctx, `
		SELECT limit_value FROM quota_balances
		WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`, sub.ID).
		Scan(&limitBefore); err != nil {
		t.Fatal(err)
	}

	created, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	e.archivePlan(t, ctx, planID)

	balBefore := e.balanceOf(t, ctx, user)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	ack := e.payAndNotify(t, ctx, user, created.OrderID, code, "stk-"+randHex(8))
	if !ackOK(ack) {
		t.Fatalf("回调: %s", ack.Body)
	}

	e.assertArchivedRefund(t, ctx, created.OrderID, dec("10"))
	mustEq(t, "余额增加", e.balanceOf(t, ctx, user).Sub(balBefore), dec("10"))

	subAfter, err := e.subs.Get(ctx, sub.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := subAfter.PeriodEnd.Sub(periodEndBefore); d < -time.Second || d > time.Second {
		t.Fatalf("叠加归档付款后 PeriodEnd=%s，应与 %s 相同", subAfter.PeriodEnd, periodEndBefore)
	}
	var limitAfter int64
	if err := e.row(ctx, `
		SELECT limit_value FROM quota_balances
		WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`, sub.ID).
		Scan(&limitAfter); err != nil {
		t.Fatal(err)
	}
	if limitAfter != limitBefore {
		t.Fatalf("limit_value %d，期望不变 %d", limitAfter, limitBefore)
	}
	e.assertNoDrift(t, ctx)
}

// 新购单占券与余额冻结：归档后付款释放券与冻结，实付与退回冻结一并进余额。
func TestArchivedPlanNewOrderReleasesCouponAndHold(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user, planID, orderID := e.pendingWithEverything(t, ctx)

	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: orderID, Provider: code})
	if err != nil {
		t.Fatal(err)
	}
	e.archivePlan(t, ctx, planID)

	balBefore := e.balanceOf(t, ctx, user)
	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound("pay-"+randHex(8), intent.OutTradeNo, 1500))
	if err != nil || !ackOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}

	e.assertArchivedRefund(t, ctx, orderID, dec("15"))
	if st := e.strCol(t, ctx, `SELECT status FROM coupon_redemptions WHERE order_id = ?`, orderID); st != "released" {
		t.Fatalf("券 status=%s，期望 released", st)
	}
	if st := e.strCol(t, ctx, `SELECT status FROM balance_holds WHERE order_id = ?`, orderID); st != "released" {
		t.Fatalf("冻结 status=%s，期望 released", st)
	}
	mustEq(t, "余额增加（含冻结退回与实付）", e.balanceOf(t, ctx, user).Sub(balBefore), dec("25"))
	mustEq(t, "余额应为 25", e.balanceOf(t, ctx, user), dec("25"))
	e.assertNoDrift(t, ctx)
}

// 套餐归档后后台标记已支付：409 且文案含「套餐已归档」，订单仍待付，无收款与挂账。
func TestArchivedPlanMarkPaidRefused(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 1000, "month")
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	e.archivePlan(t, ctx, planID)

	e.mustExec(t, ctx, "SAVEPOINT mark_paid_archived")
	_, err := e.svc.MarkPaid(actx, MarkPaidInput{OrderID: c.OrderID, Reason: "用户线下转账已核对", Reference: "BANK-ARC-1"})
	e.mustExec(t, ctx, "ROLLBACK TO SAVEPOINT mark_paid_archived")

	if codeOf(err) != errs.Conflict {
		t.Fatalf("应 409 conflict，得到 %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "套餐已归档") {
		t.Fatalf("错误应含「套餐已归档」，得到 %v", err)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusPending {
		t.Fatalf("订单仍应待付，得到 %s", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, c.OrderID); n != 0 {
		t.Fatalf("late_payment_cases %d 条，期望 0", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM payments WHERE order_id = ?`, c.OrderID); n != 0 {
		t.Fatalf("payments %d 行，期望 0", n)
	}
	e.assertNoDrift(t, ctx)
}
