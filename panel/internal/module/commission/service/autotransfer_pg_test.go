package service

import (
	"strings"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
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
		Kind: ledgersvc.KindCommissionToBalance, Currency: testCurrency,
		SourceType: SourceCommissionTransfer, SourceID: dupID, Memo: "测试：重复自动转余额",
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionAvailable, referrer), Direction: ledgersvc.Debit, Amount: dec2("0.01")},
			{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, referrer), Direction: ledgersvc.Credit, Amount: dec2("0.01")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = e.exec(ctx, `INSERT INTO commission_transfers (id, user_id, currency, amount, ledger_txn_id, entry_id)
		VALUES (?, ?, 'CNY', 0.01, ?, ?)`, dupID, referrer, spareLedger, autoEntryID)
	if err == nil || !strings.Contains(err.Error(), "commission_transfers_entry_key") {
		t.Fatalf("同一 entry_id 应撞 commission_transfers_entry_key，得到 %v", err)
	}
}

func TestAutoTransferSkipsNonSiteCurrency(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrderIn(t, e, ctx, buyer, "200.00", money.USD)
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
	if bal := accountBalanceIn(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable, money.USD); !bal.Equal(dec2("20.00")) {
		t.Fatalf("USD 可提现 %s", bal)
	}
	if bal := accountBalanceIn(t, e, ctx, referrer, ledgersvc.UserBalance, money.USD); !bal.IsZero() {
		t.Fatalf("USD 余额 %s", bal)
	}
	var trN int
	if err := e.row(ctx, `SELECT count(*) FROM commission_transfers WHERE user_id = ?`, referrer).Scan(&trN); err != nil || trN != 0 {
		t.Fatalf("不应有 transfers %d err=%v", trN, err)
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
