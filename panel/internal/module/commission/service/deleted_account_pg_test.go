package service

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/module/commission/model"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 账号注销时作废佣金（用户 10-14 定）：冻结、待复核的条目作废，在途提现驳回，剩下的可提现佣金作废；已打款、已转余额不动；
// 两个佣金科目归零；再调一次全 0；之后转余额、提现、自动转余额、后台批准与打款都回 409「账号已注销」。
func TestVoidForDeletedUser(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	a := newUser(t, e, ctx)
	b1, b2, b3, b4, b5 := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "1.00", "every_order")

	// 已打款：10.00 解冻后整笔提现、批准、打款
	if amt := accrueAvailable(t, e, ctx, a, b1, "100.00"); !amt.Equal(dec2("10.00")) {
		t.Fatalf("b1 佣金 %s", amt)
	}
	paidID, err := e.svc.RequestWithdrawal(userCtx(ctx, a), a, dec2("10.00"), "alipay", "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, paidID, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, paidID, "PAY-DEL-0001", nil); err != nil {
		t.Fatal(err)
	}
	// 已转余额：20.00 解冻后全部转入余额
	accrueAvailable(t, e, ctx, a, b2, "200.00")
	if _, err := e.svc.TransferToBalance(userCtx(ctx, a), a, dec2("20.00")); err != nil {
		t.Fatal(err)
	}
	// 可提现：30.00 解冻，其中 12.00 在提现申请中
	o3 := accrueAvailable(t, e, ctx, a, b3, "300.00")
	if !o3.Equal(dec2("30.00")) {
		t.Fatalf("b3 佣金 %s", o3)
	}
	inflightID, err := e.svc.RequestWithdrawal(userCtx(ctx, a), a, dec2("12.00"), "alipay", "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	// 冻结中：40.00；待复核：50.00（同源转人工复核）
	setConfig(t, e, ctx, admin, 10, 3, "1.00", "every_order")
	refer(e, b4, a)
	o4 := newPaidOrder(t, e, ctx, b4, "400.00")
	if err := e.svc.OnOrderPaid(ctx, o4); err != nil {
		t.Fatal(err)
	}
	e.svc.UseSourceMatcher(&fakeSources{same: map[[2]uuid.UUID]bool{{a, b5}: true}})
	refer(e, b5, a)
	o5 := newPaidOrder(t, e, ctx, b5, "500.00")
	if err := e.svc.OnOrderPaid(ctx, o5); err != nil {
		t.Fatal(err)
	}
	e.svc.UseSourceMatcher(&fakeSources{})
	var review bool
	if err := e.row(ctx, `SELECT review_required FROM commission_entries WHERE order_id = ?`, o5.OrderID).Scan(&review); err != nil || !review {
		t.Fatalf("b5 的条目应待复核：%v %v", review, err)
	}

	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.Equal(dec2("90.00")) {
		t.Fatalf("注销前待结 %s", bal)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("30.00")) {
		t.Fatalf("注销前可提现 %s", bal)
	}

	// 注销（identity 在同一事务里先把账号标成已注销，这里用假实现标记）
	e.refs.markDeleted(a)
	res, err := e.svc.VoidForDeletedUser(actx, a)
	if err != nil {
		t.Fatal(err)
	}
	cny := testCurrency
	if res.VoidedEntries != 2 || !res.VoidedPending[cny].Equal(dec2("90.00")) ||
		res.RejectedWithdrawals != 1 || !res.RejectedAmount[cny].Equal(dec2("12.00")) || !res.Forfeited[cny].Equal(dec2("30.00")) {
		t.Fatalf("作废结果 %+v", res)
	}

	// 冻结、待复核的条目作废；已解冻的三条（含已打款、已转余额的）状态不变
	for _, o := range []uuid.UUID{o4.OrderID, o5.OrderID} {
		var status, reason string
		var voidTxn *uuid.UUID
		var reviewRequired bool
		var reviewedBy *uuid.UUID
		if err := e.row(ctx, `SELECT status, COALESCE(void_reason, ''), void_txn_id, review_required, reviewed_by FROM commission_entries WHERE order_id = ?`, o).
			Scan(&status, &reason, &voidTxn, &reviewRequired, &reviewedBy); err != nil {
			t.Fatal(err)
		}
		if status != EntryVoided || reason != ReasonAccountDeleted || voidTxn == nil || reviewRequired || reviewedBy == nil || *reviewedBy != admin {
			t.Fatalf("条目 %s：status=%s reason=%s txn=%v review=%v by=%v", o, status, reason, voidTxn, reviewRequired, reviewedBy)
		}
	}
	var availableEntries int64
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE referrer_user_id = ? AND status = 'available'`, a).Scan(&availableEntries); err != nil || availableEntries != 3 {
		t.Fatalf("已解冻条目应仍是 3 条：%d %v", availableEntries, err)
	}

	// 在途提现驳回；已打款的不动
	assertWithdrawal := func(id uuid.UUID, wantStatus, wantReason string) {
		t.Helper()
		var status, reason string
		if err := e.row(ctx, `SELECT status, COALESCE(reject_reason, '') FROM withdrawals WHERE id = ?`, id).Scan(&status, &reason); err != nil {
			t.Fatal(err)
		}
		if status != wantStatus || reason != wantReason {
			t.Fatalf("提现 %s：status=%s reason=%s", id, status, reason)
		}
	}
	assertWithdrawal(inflightID, WithdrawalRejected, ReasonAccountDeleted)
	assertWithdrawal(paidID, WithdrawalPaid, "")

	// 已转余额不动：转余额记录与余额都还是 20.00
	if tr, err := e.svc.repo.TransferTotalOf(ctx, a, string(testCurrency)); err != nil || !tr.Equal(dec2("20.00")) {
		t.Fatalf("转余额合计 %s %v", tr, err)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("余额 %s", bal)
	}

	// 佣金科目配平：待结、可提现都归零，按唯一口径算的可用佣金也是 0，作废记录一行 30.00
	assertCleared := func() {
		t.Helper()
		if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.IsZero() {
			t.Fatalf("注销后待结 %s", bal)
		}
		if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
			t.Fatalf("注销后可提现 %s", bal)
		}
		if avail, err := e.svc.available(ctx, a, testCurrency); err != nil || !avail.IsZero() {
			t.Fatalf("注销后可用佣金口径 %s %v", avail, err)
		}
		var n int64
		var sum decimal.Decimal
		if err := e.row(ctx, `SELECT count(*), COALESCE(sum(amount), 0) FROM commission_forfeitures WHERE user_id = ? AND reason = ?`, a, ReasonAccountDeleted).
			Scan(&n, &sum); err != nil || n != 1 || !sum.Equal(dec2("30.00")) {
			t.Fatalf("作废记录 %d 条 %s %v", n, sum, err)
		}
		if n, err := e.ledger.DriftAccounts(ctx); err != nil || n != 0 {
			t.Fatalf("科目缓存与分录漂移 %d %v", n, err)
		}
	}
	assertCleared()

	// 再调一次：什么都不做
	again, err := e.svc.VoidForDeletedUser(actx, a)
	if err != nil {
		t.Fatal(err)
	}
	if again.VoidedEntries != 0 || again.RejectedWithdrawals != 0 || len(again.VoidedPending) != 0 || len(again.Forfeited) != 0 || len(again.RejectedAmount) != 0 {
		t.Fatalf("重调应全 0：%+v", again)
	}
	assertCleared()

	// 注销后不再出钱：门户转余额、提现
	if _, err := e.svc.TransferToBalance(userCtx(ctx, a), a, dec2("1.00")); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("注销后转余额应 409：%v", err)
	}
	if _, err := e.svc.RequestWithdrawal(userCtx(ctx, a), a, dec2("1.00"), "alipay", "alipay a@example.test"); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("注销后提现应 409：%v", err)
	}
	// 自动转余额：标记被人为挂回去也不转
	var e3 uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE referrer_user_id = ? AND order_id IN (SELECT id FROM orders WHERE user_id = ?)`, a, b3).Scan(&e3); err != nil {
		t.Fatal(err)
	}
	var flagged int64
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE referrer_user_id = ? AND auto_transfer_due`, a).Scan(&flagged); err != nil || flagged != 0 {
		t.Fatalf("注销后不应留自动转余额标记：%d %v", flagged, err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET auto_transfer_due = true WHERE id = ?`, e3); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.autoTransferOne(ctx, model.Entry{ID: e3, ReferrerUserID: a}, autoTransferPlan{on: true, site: testCurrency}); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("注销后自动转余额应 409：%v", err)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserBalance); !bal.Equal(dec2("20.00")) {
		t.Fatalf("自动转余额不应动余额：%s", bal)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET auto_transfer_due = false WHERE id = ?`, e3); err != nil {
		t.Fatal(err)
	}
	// 后台批准与标记已打款：直接插一笔申请中的提现（门户已进不来）
	var wid uuid.UUID
	if err := e.row(ctx, `INSERT INTO withdrawals (user_id, currency, amount, payout_sealed, method) VALUES (?, ?, 5.00, ?, 'alipay') RETURNING id`,
		a, string(testCurrency), []byte{1}).Scan(&wid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, wid, true, ""); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("注销后批准提现应 409：%v", err)
	}
	if err := e.exec(ctx, `UPDATE withdrawals SET status = 'approved', reviewed_at = now(), reviewed_by = ? WHERE id = ?`, admin, wid); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, wid, "PAY-DEL-0002", nil); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("注销后标记已打款应 409：%v", err)
	}
	// 后台驳回不出钱，照常可以（清掉这笔，免得影响后面）
	if err := e.exec(ctx, `UPDATE withdrawals SET status = 'requested', reviewed_at = NULL, reviewed_by = NULL WHERE id = ?`, wid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, wid, false, "账号已注销"); err != nil {
		t.Fatalf("注销后驳回提现应照常：%v", err)
	}
}

// 注销提交那一刻才计提、没赶上注销作废的条目：解冻时不解冻、转人工复核；后台只能作废，不能通过。
func TestSettleDeletedBeneficiaryGoesToReview(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	a, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 3, "1.00", "every_order")
	refer(e, buyer, a)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	// 计提时还没注销，之后注销（作废没赶上这一条）
	e.refs.markDeleted(a)
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if st.Quarantined < 1 {
		t.Fatalf("应转人工复核：%+v", st)
	}
	var id uuid.UUID
	var status, reason string
	var review bool
	if err := e.row(ctx, `SELECT id, status, review_required, COALESCE(review_reason, '') FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&id, &status, &review, &reason); err != nil {
		t.Fatal(err)
	}
	if status != EntryPending || !review || reason != reviewDeletedReason {
		t.Fatalf("条目 status=%s review=%v reason=%s", status, review, reason)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
		t.Fatalf("不应解冻：可提现 %s", bal)
	}
	if _, err := e.svc.ReviewEntry(actx, id, true, ""); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("已注销受益人的条目不能通过：%v", err)
	}
	if got, err := e.svc.ReviewEntry(actx, id, false, "受益人账号已注销"); err != nil || got != EntryVoided {
		t.Fatalf("作废应照常：%s %v", got, err)
	}
	if bal := accountBalance(t, e, ctx, a, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("作废后待结 %s", bal)
	}
}
