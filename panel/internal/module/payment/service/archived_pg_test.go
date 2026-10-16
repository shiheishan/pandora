package service

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 与 order.StatusPending 一致，挂账案 order_status 须匹配 ^[a-z_]{1,32}$
const archivedOrderStatus = "pending_payment"

// TestWebhookArchivedPlanAppliesToBalance：套餐归档后才付款的线上回调自动转余额。
func TestWebhookArchivedPlanAppliesToBalance(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-arc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseArchivedPlan, archivedOrderStatus)
	e.settler.mu.Unlock()
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")

	eventID := "evt-arc-1"
	payRef := "pay-arc-1"
	in := fakeInbound(eventID, "payment.succeeded", payRef, intent.OutTradeNo, 990)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}
	if e.settler.closeCalls.Load() != 1 {
		t.Fatalf("CloseArchivedOrder 应 1 次，实际 %d", e.settler.closeCalls.Load())
	}

	var caseID, appliedTxnID uuid.UUID
	var kind, lstatus, reason, amt string
	var resolvedAt bool
	if err := e.row(ctx, `
	SELECT id, case_kind, status, amount::text, applied_txn_id, resolution_reason, resolved_at IS NOT NULL
	FROM late_payment_cases WHERE order_id = ?`, orderID).
		Scan(&caseID, &kind, &lstatus, &amt, &appliedTxnID, &reason, &resolvedAt); err != nil {
		t.Fatal(err)
	}
	if kind != string(CaseArchivedPlan) || lstatus != string(LateApplied) {
		t.Fatalf("挂账案 kind=%s status=%s", kind, lstatus)
	}
	if !decimal.RequireFromString(amt).Equal(dec("9.90")) {
		t.Fatalf("amount=%s", amt)
	}
	if appliedTxnID == uuid.Nil {
		t.Fatal("applied_txn_id 应有值")
	}
	if reason != archivedApplyReason {
		t.Fatalf("resolution_reason=%q", reason)
	}
	if !resolvedAt {
		t.Fatal("resolved_at 应有值")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense) != 1 {
		t.Fatal("应有 late_payment_suspense 一笔")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentApplied) != 1 {
		t.Fatal("应有 late_payment_applied 一笔")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
		t.Fatal("不应 order_paid：保存点应撤回桩 Settler 的写入")
	}
	susp := ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain)
	if !e.accountBalance(t, ctx, susp).Equal(decimal.Zero) {
		t.Fatalf("挂账科目余额 %s", e.accountBalance(t, ctx, susp))
	}
	ub := ledgersvc.UserAccount(ledgersvc.UserBalance, user)
	if !e.accountBalance(t, ctx, ub).Equal(dec("9.90")) {
		t.Fatalf("用户余额 %s", e.accountBalance(t, ctx, ub))
	}
	var ist string
	if err := e.row(ctx, `SELECT status FROM payment_intents WHERE id = ?`, intent.IntentID).Scan(&ist); err != nil {
		t.Fatal(err)
	}
	if ist != string(IntentSucceeded) {
		t.Fatalf("意图 %s", ist)
	}

	var paymentID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM payments WHERE order_id = ?`, orderID).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if e.count(t, ctx, `SELECT count(*) FROM audit_events WHERE action = 'payment.quarantined' AND resource_id = ?`, paymentID) != 1 {
		t.Fatal("应有 payment.quarantined 审计一条")
	}
	if e.count(t, ctx, `SELECT count(*) FROM audit_events WHERE action = 'late_payment.applied' AND resource_id = ?`, caseID) != 1 {
		t.Fatal("应有 late_payment.applied 审计一条")
	}

	for i := 0; i < 2; i++ {
		ackR, err := e.svc.HandleWebhook(ctx, code, in)
		if err != nil {
			t.Fatalf("重放 %d: %v", i+1, err)
		}
		if ackR.HTTPStatus != http.StatusOK {
			t.Fatalf("重放 %d HTTP %d", i+1, ackR.HTTPStatus)
		}
	}
	if e.settler.closeCalls.Load() != 1 {
		t.Fatalf("重放后 closeCalls 仍应为 1，实际 %d", e.settler.closeCalls.Load())
	}
	if e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, orderID) != 1 {
		t.Fatal("重放不应新开挂账案")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense) != 1 || e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentApplied) != 1 {
		t.Fatal("重放不应多记分录")
	}
	if !e.accountBalance(t, ctx, ub).Equal(dec("9.90")) {
		t.Fatal("重放后用户余额不应变")
	}
	e.assertNoDrift(t, ctx)
}

// TestRecordOfflineArchivedPlanRefused：线下收款遇归档套餐整笔拒绝、不开挂账。
func TestRecordOfflineArchivedPlanRefused(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseArchivedPlan, archivedOrderStatus)
	e.settler.mu.Unlock()

	_, err := e.svc.RecordOffline(ctx, OfflineInput{
		OrderID: orderID, OrderNo: orderNo, UserID: user,
		Amount: dec("9.90"), Reference: "BANK-ARC-1", Reason: "线下转账",
	})
	if err == nil {
		t.Fatal("应失败")
	}
	if !errors.Is(err, ErrArchivedPlanOffline) {
		if ae := errs.As(err); ae.Code != errs.Conflict || !strings.Contains(ae.Message, "套餐已归档") {
			t.Fatalf("期望 ErrArchivedPlanOffline 或 Conflict+文案，得 %+v", err)
		}
	}
	if e.settler.closeCalls.Load() != 0 {
		t.Fatalf("线下不应调 CloseArchivedOrder，实际 %d", e.settler.closeCalls.Load())
	}
	if e.count(t, ctx, `SELECT count(*) FROM late_payment_cases WHERE order_id = ?`, orderID) != 0 {
		t.Fatal("不应有挂账案")
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindLatePaymentSuspense) != 0 {
		t.Fatal("不应有 late_payment_suspense")
	}
}

func TestCaseKindValid(t *testing.T) {
	if !CaseArchivedPlan.Valid() {
		t.Fatal("archived_plan 应合法")
	}
	if CaseKind("nope").Valid() {
		t.Fatal("未知种类应不合法")
	}
}

// TestWebhookArchivedPlanFeeAndSurcharge 钉住当前口径（用户 2026-10-16「手续费不退」）：
// 加收的支付手续费 surcharge 照记手续费收入、不退；渠道手续费照记费用；挂账与转入余额都只有应付 = 实收 − surcharge。
func TestWebhookArchivedPlanFeeAndSurcharge(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-arc-sc-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)
	e.setFee(t, ctx, code, "0.30", "2")
	orderID, orderNo := e.mkOrder(t, ctx, user)
	e.settler.mu.Lock()
	e.settler.notPayable[orderID] = NotPayable(CaseArchivedPlan, archivedOrderStatus)
	e.settler.mu.Unlock()
	intent := e.createIntent(t, ctx, code, orderID, orderNo, user, "9.90")
	if !intent.Amount.Equal(dec("10.40")) || !intent.Surcharge.Equal(dec("0.50")) {
		t.Fatalf("意图金额 %s surcharge %s", intent.Amount, intent.Surcharge)
	}

	// 实收 10.40，其中加收手续费 0.50；渠道另扣 0.20 渠道手续费
	in := fakeInboundFee("evt-arc-sc-1", "payment.succeeded", "pay-arc-sc-1", intent.OutTradeNo, 1040, 20)
	ack, err := e.svc.HandleWebhook(ctx, code, in)
	if err != nil {
		t.Fatal(err)
	}
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("应 200: %s", string(ack.Body))
	}

	// legsOf 把某类交易的全部分录拼成「科目/owner_ref/方向/金额」按字典序排好的一串（numeric(20,2) 转文本带两位小数）
	legsOf := func(kind ledgersvc.Kind) string {
		t.Helper()
		var agg sql.NullString
		if err := e.row(ctx, `
			SELECT string_agg(a.account_type || '/' || a.owner_ref || '/' || le.direction || '/' || le.amount::text, ','
			                  ORDER BY a.account_type, a.owner_ref, le.direction, le.amount)
			FROM ledger_entries le
			JOIN ledger_transactions lt ON lt.id = le.transaction_id
			JOIN ledger_accounts a ON a.id = le.account_id
			WHERE lt.kind = ?`, string(kind)).Scan(&agg); err != nil {
			t.Fatal(err)
		}
		return agg.String
	}

	// 挂账分录：渠道现金 10.20 + 渠道手续费 0.20 = 挂账 9.90 + 加收手续费收入 0.50
	wantSuspense := strings.Join([]string{
		"channel_cash/" + code + "/debit/10.20",
		"late_payment_suspense/" + ledgersvc.RefMain + "/credit/9.90",
		"platform_fee_expense/" + code + "/debit/0.20",
		"platform_revenue/" + ledgersvc.RefSurcharge + "/credit/0.50",
	}, ",")
	if got := legsOf(ledgersvc.KindLatePaymentSuspense); got != wantSuspense {
		t.Fatalf("挂账分录\n得到 %s\n期望 %s", got, wantSuspense)
	}
	// 转入分录：挂账借 9.90、用户余额贷 9.90（不含 surcharge；用户科目 owner_ref 为空）
	wantApplied := "late_payment_suspense/" + ledgersvc.RefMain + "/debit/9.90,user_balance//credit/9.90"
	if got := legsOf(ledgersvc.KindLatePaymentApplied); got != wantApplied {
		t.Fatalf("转入分录\n得到 %s\n期望 %s", got, wantApplied)
	}

	var caseAmt string
	if err := e.row(ctx, `SELECT amount::text FROM late_payment_cases WHERE order_id = ?`, orderID).Scan(&caseAmt); err != nil {
		t.Fatal(err)
	}
	if !dec(caseAmt).Equal(dec("9.90")) {
		t.Fatalf("挂账案金额 %s，期望 9.90（实收 − surcharge）", caseAmt)
	}
	if b := e.accountBalance(t, ctx, ledgersvc.UserAccount(ledgersvc.UserBalance, user)); !b.Equal(dec("9.90")) {
		t.Fatalf("用户余额 %s，期望 9.90", b)
	}
	if b := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge)); !b.Equal(dec("0.50")) {
		t.Fatalf("手续费收入 %s，期望 0.50（不退）", b)
	}
	if b := e.accountBalance(t, ctx, ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain)); !b.IsZero() {
		t.Fatalf("挂账科目余额 %s，期望 0", b)
	}
	if e.ledgerTxnCount(t, ctx, ledgersvc.KindOrderPaid) != 0 {
		t.Fatal("不应记 order_paid（不记订单收入）")
	}
	e.assertNoDrift(t, ctx)
}
