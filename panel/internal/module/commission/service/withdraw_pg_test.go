package service

import (
	"bytes"
	"errors"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 这些用例都在测试事务里跑：只断言 Go 层先挡下的错误，不触发会让事务中止的 SQL 错误。

func TestRequestWithdrawalRules(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	if amt := accrueAvailable(t, e, ctx, referrer, buyer, "200.00"); !amt.Equal(dec2("20.00")) {
		t.Fatalf("佣金 %s", amt)
	}
	uctx := userCtx(ctx, referrer)

	// 没有任何佣金
	poor := newUser(t, e, ctx)
	if _, err := e.svc.RequestWithdrawal(userCtx(ctx, poor), poor, dec2("10.00"), "alipay p@example.test"); !errors.Is(err, ErrNoCommission) {
		t.Fatalf("没有佣金应拒绝: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("5.00"), "alipay a@example.test"); !errors.Is(err, ErrWithdrawTooSmall) {
		t.Fatalf("低于最低额应拒绝: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("20.01"), "alipay a@example.test"); !errors.Is(err, ErrWithdrawTooMuch) {
		t.Fatalf("超过可提现应拒绝: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "   "); errCode(err) != errs.ValidationFailed {
		t.Fatalf("空收款方式应 422: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("10.001"), "alipay a@example.test"); errCode(err) != errs.ValidationFailed {
		t.Fatalf("三位小数应 422: %v", err)
	}

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), " alipay a@example.test ")
	if err != nil {
		t.Fatalf("提现申请: %v", err)
	}
	var status string
	var sealed []byte
	if err := e.row(ctx, `SELECT status, payout_sealed FROM withdrawals WHERE id = ? AND user_id = ?`, id, referrer).Scan(&status, &sealed); err != nil {
		t.Fatal(err)
	}
	if status != WithdrawalRequested || len(sealed) == 0 || bytes.Contains(sealed, []byte("alipay")) {
		t.Fatalf("申请行不对: status=%s sealed=%q", status, sealed)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionWithdrawalRequested, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("申请审计 %d 条 err=%v", n, err)
	}
	// 提现申请不动账
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("20.00")) {
		t.Fatalf("申请后可提现佣金科目 %s", bal)
	}

	// 已有在途申请
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("10.00"), "alipay a@example.test"); !errors.Is(err, ErrWithdrawPending) {
		t.Fatalf("在途时再申请应拒绝: %v", err)
	}
}

func TestReviewAndPay(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	id, err := e.svc.RequestWithdrawal(userCtx(ctx, referrer), referrer, dec2("15.00"), "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	actx := adminCtx(ctx, admin)

	if _, err := e.svc.ReviewWithdrawal(ctx, id, true, ""); errCode(err) != errs.Unauthorized {
		t.Fatalf("没有登录主体应 401: %v", err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, uuid.New(), true, ""); !errors.Is(err, ErrWithdrawalNotFound) {
		t.Fatalf("不存在的申请应 404: %v", err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, true, "顺便说一句"); errCode(err) != errs.ValidationFailed {
		t.Fatalf("批准带理由应 422: %v", err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-EARLY"); !errors.Is(err, ErrWithdrawalNotPayable) {
		t.Fatalf("未批准就打款应拒绝: %v", err)
	}
	st, err := e.svc.ReviewWithdrawal(actx, id, true, "")
	if err != nil || st != WithdrawalApproved {
		t.Fatalf("批准: %s %v", st, err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, false, "改主意"); !errors.Is(err, ErrWithdrawalHandled) {
		t.Fatalf("已批准的再审应拒绝: %v", err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "  "); errCode(err) != errs.ValidationFailed {
		t.Fatalf("空流水号应 422: %v", err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-0001"); err != nil {
		t.Fatalf("打款: %v", err)
	}

	var (
		status, ref     string
		payoutTxn       *uuid.UUID
		reviewer, payer *uuid.UUID
	)
	if err := e.row(ctx, `SELECT status, payout_reference, payout_txn_id, reviewed_by, paid_by FROM withdrawals WHERE id = ?`, id).
		Scan(&status, &ref, &payoutTxn, &reviewer, &payer); err != nil {
		t.Fatal(err)
	}
	if status != WithdrawalPaid || ref != "TX-0001" || payoutTxn == nil || reviewer == nil || *reviewer != admin || payer == nil || *payer != admin {
		t.Fatalf("打款后行不对: %s %s %v %v %v", status, ref, payoutTxn, reviewer, payer)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("5.00")) {
		t.Fatalf("打款后可提现佣金科目 %s", bal)
	}
	var kind string
	if err := e.row(ctx, `SELECT kind FROM ledger_transactions WHERE id = ?`, *payoutTxn).Scan(&kind); err != nil || kind != "commission_paid" {
		t.Fatalf("打款交易 kind=%s err=%v", kind, err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-0002"); !errors.Is(err, ErrWithdrawalNotPayable) {
		t.Fatalf("重复打款应拒绝: %v", err)
	}

	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	s := mc.Summary
	if !s.Available.Equal(dec2("5.00")) || !s.Settled.Equal(dec2("15.00")) || !s.Withdrawing.IsZero() {
		t.Fatalf("汇总 available=%s settled=%s withdrawing=%s", s.Available, s.Settled, s.Withdrawing)
	}
	if len(mc.Withdrawals) != 1 || mc.Withdrawals[0].Status != WithdrawalPaid || mc.Withdrawals[0].CompletedAt == nil {
		t.Fatalf("提现记录 %+v", mc.Withdrawals)
	}
}

func TestRejectWithdrawal(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx, actx := userCtx(ctx, referrer), adminCtx(ctx, admin)
	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, false, "  "); errCode(err) != errs.ValidationFailed {
		t.Fatalf("拒绝不给理由应 422: %v", err)
	}
	st, err := e.svc.ReviewWithdrawal(actx, id, false, "收款信息不全")
	if err != nil || st != WithdrawalRejected {
		t.Fatalf("拒绝: %s %v", st, err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-0001"); !errors.Is(err, ErrWithdrawalNotPayable) {
		t.Fatalf("已拒绝的打款应拒绝: %v", err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, true, ""); !errors.Is(err, ErrWithdrawalHandled) {
		t.Fatalf("已拒绝的再批准应拒绝: %v", err)
	}

	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if !mc.Summary.Available.Equal(dec2("20.00")) || !mc.Summary.Withdrawing.IsZero() {
		t.Fatalf("拒绝后应全额退回可提现: %s / %s", mc.Summary.Available, mc.Summary.Withdrawing)
	}
	if len(mc.Withdrawals) != 1 || mc.Withdrawals[0].RejectReason != "收款信息不全" {
		t.Fatalf("提现记录 %+v", mc.Withdrawals)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("20.00")) {
		t.Fatalf("拒绝不动账，可提现佣金科目 %s", bal)
	}
	// 拒绝后可以重新申请全额
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("20.00"), "alipay a@example.test"); err != nil {
		t.Fatalf("拒绝后重新申请: %v", err)
	}
}

func TestAdminWithdrawalsAndOverview(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	before, err := e.svc.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	id, err := e.svc.RequestWithdrawal(userCtx(ctx, referrer), referrer, dec2("15.00"), "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.AdminWithdrawals(ctx, "bogus"); errCode(err) != errs.ValidationFailed {
		t.Fatalf("非法状态应 422: %v", err)
	}
	rows, err := e.svc.AdminWithdrawals(ctx, WithdrawalRequested)
	if err != nil {
		t.Fatal(err)
	}
	var got *AdminWithdrawal
	for i := range rows {
		if rows[i].Status != WithdrawalRequested {
			t.Fatalf("按状态筛出了 %s", rows[i].Status)
		}
		if rows[i].ID == id {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatal("列表里没有刚申请的提现")
	}
	if got.PayoutDetail != "alipay a@example.test" || got.UserID != referrer || got.Email == "" ||
		!got.Amount.Equal(dec2("15.00")) || !got.EarnedTotal.Equal(dec2("20.00")) || got.CompletedAt != nil {
		t.Fatalf("列表行不对: %+v", *got)
	}

	after, err := e.svc.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.TotalEarned.Sub(before.TotalEarned).Equal(dec2("20.00")) ||
		!after.Available.Sub(before.Available).Equal(dec2("20.00")) ||
		!after.ThisMonth.Sub(before.ThisMonth).Equal(dec2("20.00")) ||
		after.Entries-before.Entries != 1 ||
		after.WaitingWithdrawals-before.WaitingWithdrawals != 1 ||
		after.InvitedUsers-before.InvitedUsers != 1 {
		t.Fatalf("概览增量不对: before=%+v after=%+v", before, after)
	}
	if after.RatePercent != 10 || after.FreezeDays != 0 || !after.MinWithdraw.Equal(dec2("10.00")) || after.Scope != "every_order" {
		t.Fatalf("概览参数 %+v", after)
	}
}

func TestMyCommissionSummary(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, b1, b2 := newUser(t, e, ctx), newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, b1, "200.00") // 20.00 已解冻

	setConfig(t, e, ctx, admin, 10, 3, "10.00", "every_order")
	refer(e, b2, referrer)
	if err := e.svc.OnOrderPaid(ctx, newPaidOrder(t, e, ctx, b2, "50.00")); err != nil { // 5.00 冻结中
		t.Fatal(err)
	}
	uctx := userCtx(ctx, referrer)
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("3.00")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("10.00"), "alipay a@example.test"); err != nil {
		t.Fatal(err)
	}

	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	s := mc.Summary
	if s.Currency != testCurrency || !s.Pending.Equal(dec2("5.00")) || !s.Available.Equal(dec2("7.00")) ||
		!s.Withdrawing.Equal(dec2("10.00")) || !s.Settled.IsZero() || !s.TotalEarned.Equal(dec2("25.00")) {
		t.Fatalf("金额汇总 %+v", s)
	}
	if s.Invitees != 2 || s.Orders != 2 || s.PaidInvitees != 2 || s.RatePercent != 10 ||
		!s.MinWithdraw.Equal(dec2("10.00")) || s.Scope != "every_order" {
		t.Fatalf("计数与参数 %+v", s)
	}
	if len(mc.Entries) != 2 || len(mc.Withdrawals) != 1 || len(mc.Transfers) != 1 {
		t.Fatalf("明细条数 entries=%d withdrawals=%d transfers=%d", len(mc.Entries), len(mc.Withdrawals), len(mc.Transfers))
	}
	for _, en := range mc.Entries {
		if len(en.From) < 5 || en.From[:5] != "cm***" || en.OrderNo == "" || en.RateBP != 1000 {
			t.Fatalf("佣金明细 %+v", en)
		}
	}

	// 没有任何记录的用户：列表是空切片不是 nil
	lone := newUser(t, e, ctx)
	mc, err = e.svc.MyCommission(ctx, lone)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Entries == nil || mc.Withdrawals == nil || mc.Transfers == nil || !mc.Summary.Available.IsZero() {
		t.Fatalf("空用户 %+v", mc)
	}
}
