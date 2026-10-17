package service

import (
	"context"
	"strings"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestAutoTransferWhenWithdrawClosed(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "200.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil {
		t.Fatalf("解冻: %v", err)
	}
	if st.Settled != 1 || st.AutoTransferred != 1 {
		t.Fatalf("统计 %+v", st)
	}

	var entryID uuid.UUID
	var entryStatus string
	if err := e.row(ctx, `SELECT id, status FROM commission_entries WHERE order_id = ?`, o.OrderID).Scan(&entryID, &entryStatus); err != nil {
		t.Fatal(err)
	}
	if entryStatus != EntryAvailable {
		t.Fatalf("条目状态 %s", entryStatus)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("余额 %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
		t.Fatalf("可提现佣金 %s", bal)
	}

	var trAmt decimal.Decimal
	var trN int
	if err := e.row(ctx, `SELECT amount FROM commission_transfers WHERE entry_id = ?`, entryID).Scan(&trAmt); err != nil {
		t.Fatal(err)
	}
	if !trAmt.Equal(dec2("20.00")) {
		t.Fatalf("transfer 金额 %s", trAmt)
	}
	if err := e.row(ctx, `SELECT count(*) FROM commission_transfers WHERE entry_id = ?`, entryID).Scan(&trN); err != nil || trN != 1 {
		t.Fatalf("transfers 行数 %d err=%v", trN, err)
	}

	var auditN int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionAutoTransferred, entryID).Scan(&auditN); err != nil || auditN != 1 {
		t.Fatalf("自动转余额审计 %d err=%v", auditN, err)
	}

	st2, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if st2.AutoTransferred != 0 {
		t.Fatalf("第二轮 AutoTransferred=%d", st2.AutoTransferred)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("余额应仍为 20.00: %s", bal)
	}
	if err := e.row(ctx, `SELECT count(*) FROM commission_transfers WHERE entry_id = ?`, entryID).Scan(&trN); err != nil || trN != 1 {
		t.Fatalf("transfers 仍应 1 行 %d err=%v", trN, err)
	}
}

func TestAutoTransferUniqueEntry(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer1, buyer2 := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer1, "200.00")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	refer(e, buyer2, referrer)
	o2 := newPaidOrder(t, e, ctx, buyer2, "200.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o2.OrderID); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil || st.AutoTransferred != 1 {
		t.Fatalf("自动转 %+v err=%v", st, err)
	}

	var autoEntryID uuid.UUID
	if err := e.row(ctx, `SELECT entry_id FROM commission_transfers WHERE user_id = ? AND entry_id IS NOT NULL`, referrer).Scan(&autoEntryID); err != nil {
		t.Fatal(err)
	}
	// 另记一笔不被任何转余额记录引用的账本交易，让重复插入只可能撞 entry_id 唯一（ledger_txn_id 也唯一）
	dupID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	spareLedger, err := e.ledger.Post(ctx, ledgersvc.Posting{
		Kind:       ledgersvc.KindCommissionToBalance,
		SourceType: SourceCommissionTransfer, SourceID: dupID, Memo: "测试：重复自动转余额",
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, referrer), Direction: ledgersvc.Debit, Amount: dec2("0.01")},
			{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, referrer), Direction: ledgersvc.Credit, Amount: dec2("0.01")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = e.exec(ctx, `INSERT INTO commission_transfers (id, user_id, amount, ledger_txn_id, entry_id)
		VALUES (?, ?, 0.01, ?, ?)`, dupID, referrer, spareLedger, autoEntryID)
	if err == nil || !strings.Contains(err.Error(), "commission_transfers_entry_key") {
		t.Fatalf("同一 entry_id 应撞 commission_transfers_entry_key，得到 %v", err)
	}
}

func TestNoAutoTransferWhenWithdrawOpen(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "200.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if st.AutoTransferred != 0 || st.Settled != 1 {
		t.Fatalf("统计 %+v", st)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("20.00")) {
		t.Fatalf("可提现 %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.IsZero() {
		t.Fatalf("余额 %s", bal)
	}
}

func TestManualTransferStillWorksWhenClosed(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	uctx := userCtx(ctx, referrer)

	txnID, err := e.svc.TransferToBalance(uctx, referrer, dec2("20.00"))
	if err != nil {
		t.Fatalf("关闭提现后手动转余额: %v", err)
	}
	if txnID == uuid.Nil {
		t.Fatal("txn id 为空")
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("余额 %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
		t.Fatalf("可提现 %s", bal)
	}
}

// autoTransferDueOf 读条目的「等自动转余额」标记。
func autoTransferDueOf(t *testing.T, e *env, ctx context.Context, entryID uuid.UUID) bool {
	t.Helper()
	var due bool
	if err := e.row(ctx, `SELECT auto_transfer_due FROM commission_entries WHERE id = ?`, entryID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	return due
}

// 解冻事务只挂标记、不碰余额科目；转余额在之后单独的事务里做（应改-2：解冻事务里不同时锁佣金与余额）。
func TestSettleOnlyFlagsThenTransfersSeparately(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "200.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	entryID := entryIDOf(t, e, ctx, o.OrderID, referrer)

	out, err := e.svc.settleOne(ctx, entryID, autoTransferPlan{on: true})
	if err != nil || out != settleDone {
		t.Fatalf("解冻 out=%v err=%v", out, err)
	}
	if !autoTransferDueOf(t, e, ctx, entryID) {
		t.Fatal("提现关闭时解冻应挂上 auto_transfer_due")
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.IsZero() {
		t.Fatalf("解冻事务里不应转余额: %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("20.00")) {
		t.Fatalf("可提现 %s", bal)
	}

	n, failed, err := e.svc.autoTransferDue(ctx, SettleBatchDefault, autoTransferPlan{on: true})
	if err != nil || n != 1 || failed != 0 {
		t.Fatalf("补转 n=%d failed=%d err=%v", n, failed, err)
	}
	if autoTransferDueOf(t, e, ctx, entryID) {
		t.Fatal("转完应清掉标记")
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("余额 %s", bal)
	}

	// 开着提现时解冻不挂标记
	o2 := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o2); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o2.OrderID); err != nil {
		t.Fatal(err)
	}
	id2 := entryIDOf(t, e, ctx, o2.OrderID, referrer)
	if out, err := e.svc.settleOne(ctx, id2, autoTransferPlan{on: false}); err != nil || out != settleDone {
		t.Fatalf("解冻 out=%v err=%v", out, err)
	}
	if autoTransferDueOf(t, e, ctx, id2) {
		t.Fatal("开着提现时不应挂标记")
	}
}

// 上一轮转失败、标记还挂着的条目，下一轮 SettleDue 补转；只转一次。
func TestAutoTransferRetriesFlaggedEntry(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	var entryID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE referrer_user_id = ?`, referrer).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	// 模拟「解冻已提交、转余额那个事务失败」：条目已 available，标记挂着，还没有转余额记录
	if err := e.exec(ctx, `UPDATE commission_entries SET auto_transfer_due = true WHERE id = ?`, entryID); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil || st.Settled != 0 || st.AutoTransferred != 1 {
		t.Fatalf("补转 %+v err=%v", st, err)
	}
	if autoTransferDueOf(t, e, ctx, entryID) {
		t.Fatal("补转后应清掉标记")
	}
	var trN int
	if err := e.row(ctx, `SELECT count(*) FROM commission_transfers WHERE entry_id = ?`, entryID).Scan(&trN); err != nil || trN != 1 {
		t.Fatalf("transfers %d err=%v", trN, err)
	}
	st2, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil || st2.AutoTransferred != 0 {
		t.Fatalf("第二轮 %+v err=%v", st2, err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("余额 %s", bal)
	}
}

// 标记挂着期间用户自己转走了一部分或全部：只转剩下的；一分不剩时只清标记、不记账。
func TestAutoTransferClampsToAvailable(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer1, buyer2 := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer1, "200.00") // 20.00
	uctx := userCtx(ctx, referrer)
	var first uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE referrer_user_id = ?`, referrer).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET auto_transfer_due = true WHERE id = ?`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("15.00")); err != nil {
		t.Fatal(err)
	}
	n, failed, err := e.svc.autoTransferDue(ctx, SettleBatchDefault, autoTransferPlan{on: true})
	if err != nil || n != 1 || failed != 0 {
		t.Fatalf("补转 n=%d failed=%d err=%v", n, failed, err)
	}
	var amt decimal.Decimal
	if err := e.row(ctx, `SELECT amount FROM commission_transfers WHERE entry_id = ?`, first).Scan(&amt); err != nil || !amt.Equal(dec2("5.00")) {
		t.Fatalf("只应转剩下的 5.00: %s err=%v", amt, err)
	}

	// 第二条：标记挂着时用户已经全部转走
	accrueAvailable(t, e, ctx, referrer, buyer2, "100.00") // 10.00
	second := uuid.Nil
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE referrer_user_id = ? AND id <> ?`, referrer, first).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET auto_transfer_due = true WHERE id = ?`, second); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("10.00")); err != nil {
		t.Fatal(err)
	}
	n, failed, err = e.svc.autoTransferDue(ctx, SettleBatchDefault, autoTransferPlan{on: true})
	if err != nil || n != 0 || failed != 0 {
		t.Fatalf("没钱可转 n=%d failed=%d err=%v", n, failed, err)
	}
	if autoTransferDueOf(t, e, ctx, second) {
		t.Fatal("没钱可转也应清掉标记，不每轮空转")
	}
	var trN int
	if err := e.row(ctx, `SELECT count(*) FROM commission_transfers WHERE entry_id = ?`, second).Scan(&trN); err != nil || trN != 0 {
		t.Fatalf("不应有自动转余额记录 %d err=%v", trN, err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("30.00")) {
		t.Fatalf("余额 %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
		t.Fatalf("可提现 %s", bal)
	}
}

// 关提现时解冻挂了标记，转之前提现又重新打开：下一轮只清标记、不转，钱留在可提现由用户自己处理。
func TestAutoTransferSkippedAfterWithdrawReopened(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "200.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	entryID := entryIDOf(t, e, ctx, o.OrderID, referrer)
	// 只做解冻这一步（模拟转余额那个事务还没来得及做或失败了）
	if out, err := e.svc.settleOne(ctx, entryID, autoTransferPlan{on: true}); err != nil || out != settleDone {
		t.Fatalf("解冻 out=%v err=%v", out, err)
	}
	if !autoTransferDueOf(t, e, ctx, entryID) {
		t.Fatal("应挂上标记")
	}

	setWithdraw(t, e, ctx, admin, true, "alipay")
	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil || st.AutoTransferred != 0 || st.Failed != 0 {
		t.Fatalf("重新打开后 %+v err=%v", st, err)
	}
	if autoTransferDueOf(t, e, ctx, entryID) {
		t.Fatal("重新打开后应清掉标记")
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.IsZero() {
		t.Fatalf("余额不应变: %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("20.00")) {
		t.Fatalf("可提现应仍为 20.00: %s", bal)
	}
	var trN int
	if err := e.row(ctx, `SELECT count(*) FROM commission_transfers WHERE entry_id = ?`, entryID).Scan(&trN); err != nil || trN != 0 {
		t.Fatalf("不应有自动转余额记录 %d err=%v", trN, err)
	}
}
