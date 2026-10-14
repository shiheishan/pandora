package service

import (
	"errors"
	"sync"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestTransferToBalance(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx := userCtx(ctx, referrer)

	for _, bad := range []string{"0", "-1.00", "0.001"} {
		if _, err := e.svc.TransferToBalance(uctx, referrer, dec2(bad)); errCode(err) != errs.ValidationFailed {
			t.Fatalf("金额 %s 应 422: %v", bad, err)
		}
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("20.01")); !errors.Is(err, ErrTransferInsufficient) {
		t.Fatalf("超额应拒绝: %v", err)
	}

	txnID, err := e.svc.TransferToBalance(uctx, referrer, dec2("12.50"))
	if err != nil {
		t.Fatalf("转余额: %v", err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(dec2("7.50")) {
		t.Fatalf("可提现佣金科目 %s", bal)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserBalance); !bal.Equal(dec2("12.50")) {
		t.Fatalf("余额科目 %s", bal)
	}
	var (
		kind, sourceType string
		sourceID         uuid.UUID
		trAmount         decimal.Decimal
	)
	if err := e.row(ctx, `SELECT kind, source_type, source_id FROM ledger_transactions WHERE id = ?`, txnID).Scan(&kind, &sourceType, &sourceID); err != nil {
		t.Fatal(err)
	}
	if kind != "commission_to_balance" || sourceType != SourceCommissionTransfer {
		t.Fatalf("账本交易 kind=%s source=%s", kind, sourceType)
	}
	if err := e.row(ctx, `SELECT amount FROM commission_transfers WHERE id = ? AND user_id = ? AND ledger_txn_id = ?`, sourceID, referrer, txnID).Scan(&trAmount); err != nil || !trAmount.Equal(dec2("12.50")) {
		t.Fatalf("转余额记录 %s err=%v", trAmount, err)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionTransferredToBal, txnID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("转余额审计 %d 条 err=%v", n, err)
	}

	// 转光剩下的，再转一分也不行
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("7.50")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("0.01")); !errors.Is(err, ErrTransferInsufficient) {
		t.Fatalf("转光后再转应拒绝: %v", err)
	}
	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if !mc.Summary.Available.IsZero() || len(mc.Transfers) != 2 {
		t.Fatalf("转光后 available=%s transfers=%d", mc.Summary.Available, len(mc.Transfers))
	}
}

func TestTransferRespectsInFlightWithdrawal(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx := userCtx(ctx, referrer)

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "alipay", "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	// 在途提现占着 15.00，科目余额虽是 20.00，也只能转 5.00
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("5.01")); !errors.Is(err, ErrTransferInsufficient) {
		t.Fatalf("动到在途提现的部分应拒绝: %v", err)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("5.00")); err != nil {
		t.Fatalf("转在途之外的部分: %v", err)
	}
	// 拒绝后占用释放
	if _, err := e.svc.ReviewWithdrawal(adminCtx(ctx, admin), id, false, "收款信息不全"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.TransferToBalance(uctx, referrer, dec2("15.00")); err != nil {
		t.Fatalf("拒绝后转回: %v", err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.IsZero() {
		t.Fatalf("可提现佣金科目 %s", bal)
	}
}

// TestConcurrentWithdrawAndTransfer 让同一用户的提现与转余额真并发（各自提交），全额只能被拿走一次。
func TestConcurrentWithdrawAndTransfer(t *testing.T) {
	e, _ := newEnv(t)
	e.cleanCommitted(t)
	ctx := commitCtx()
	admin := newUser(t, e, ctx)

	// 这里的分销参数会真提交；用例结束改回原值，不影响后面的用例
	prev, err := e.settings.Commission(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.settings.SetCommission(adminCtx(commitCtx(), admin), settingssvc.CommissionUpdate{
			RatePercent: &prev.RatePercent, FreezeDays: &prev.FreezeDays, MinWithdraw: &prev.MinWithdraw, Scope: &prev.Scope,
		}); err != nil {
			t.Errorf("还原分销参数: %v", err)
		}
	})
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	if amt := accrueAvailable(t, e, ctx, referrer, buyer, "500.00"); !amt.Equal(dec2("50.00")) {
		t.Fatalf("佣金 %s", amt)
	}

	const workers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		others  []error
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uctx := userCtx(commitCtx(), referrer)
			<-start
			var err error
			if i%2 == 0 {
				_, err = e.svc.RequestWithdrawal(uctx, referrer, dec2("50.00"), "alipay", "alipay a@example.test")
			} else {
				_, err = e.svc.TransferToBalance(uctx, referrer, dec2("50.00"))
			}
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
			} else {
				others = append(others, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if success != 1 {
		t.Fatalf("应恰好成功 1 次，实际 %d；失败: %v", success, others)
	}
	for _, err := range others {
		if c := errCode(err); c != errs.Conflict && c != errs.ValidationFailed {
			t.Fatalf("失败只应是冲突或无可提现佣金: %v", err)
		}
	}
	var wsum, tsum decimal.Decimal
	if err := e.row(ctx, `SELECT COALESCE(sum(amount), 0) FROM withdrawals WHERE user_id = ? AND status <> 'rejected'`, referrer).Scan(&wsum); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT COALESCE(sum(amount), 0) FROM commission_transfers WHERE user_id = ?`, referrer).Scan(&tsum); err != nil {
		t.Fatal(err)
	}
	if !wsum.Add(tsum).Equal(dec2("50.00")) {
		t.Fatalf("提现 %s + 转余额 %s 应等于 50.00", wsum, tsum)
	}
	// 账本：可提现佣金科目只在转余额时减少
	want := dec2("50.00").Sub(tsum)
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(want) {
		t.Fatalf("可提现佣金科目 %s，应为 %s", bal, want)
	}
}
