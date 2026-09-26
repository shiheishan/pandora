package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// orderReleasePG18CommissionLedgerCases 是 D-F-1 的 PG18 证明，挂在
// TestOrderReleasePG18 末尾复用它的一次性租户（佣金 10%、冻结 0 天、最低提现 100）。
//
// 口径：可用佣金 = 账本科目余额 − 未过账的在途提现；转余额与提现申请在同一把
// 科目锁下按这个口径校验。旧实现里两条路径各看各的，同一笔佣金可以先转余额、
// 再申请提现，要到打款时才被账本拦下。
func orderReleasePG18CommissionLedgerCases(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, service *Service, fx orderReleasePG18Fixture,
	newOrder func(*testing.T, string, string) string,
	webhook func(orderID, eventID, paymentID string, amount int64) PaymentWebhookInput) {

	t.Helper()
	seq := 0
	// fund 让 commissionBuyer 付 n 单，每单给 referrer 结出 100 可用佣金。
	fund := func(t *testing.T, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			seq++
			label := fmt.Sprintf("ledger-%d", seq)
			orderID := newOrder(t, fx.commissionBuyer, "commission-"+label)
			capture := webhook(orderID, label+"-event-"+fx.suffix, label+"-payment-"+fx.suffix, 1000)
			if out, err := service.HandlePaymentWebhook(ctx, fx.tenant, capture); err != nil ||
				out == nil || !out.Processed {
				t.Fatalf("fund commission %s output=%#v err=%v", label, out, err)
			}
		}
		if settled, err := service.SettleMatured(ctx, fx.tenant); err != nil || settled != n {
			t.Fatalf("settle funded commissions count=%d want=%d err=%v", settled, n, err)
		}
	}
	summary := func(t *testing.T) *CommissionSummary {
		t.Helper()
		sum, err := service.CommissionSummary(ctx, fx.tenant, fx.referrer)
		if err != nil {
			t.Fatalf("commission summary: %v", err)
		}
		return sum
	}
	wantAvailable := func(t *testing.T, want int64) {
		t.Helper()
		if got := summary(t).Available; got != want {
			t.Fatalf("summary.available=%d want=%d", got, want)
		}
	}
	wantCode := func(t *testing.T, err error, codes ...httpx.Code) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) {
			t.Fatalf("error=%v, want one of %v", err, codes)
		}
		for _, code := range codes {
			if he.Code == code {
				return
			}
		}
		t.Fatalf("error code=%s message=%q, want one of %v", he.Code, he.Message, codes)
	}
	payOut := func(t *testing.T, withdrawalID string, amount int64) {
		t.Helper()
		err := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer},
			func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `UPDATE withdrawals SET status='approved',updated_at=now()
					WHERE tenant_id=$1 AND id=$2::uuid AND status='requested'`, fx.tenant, withdrawalID)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("approve withdrawal rows=%d", tag.RowsAffected())
				}
				payoutTxn, err := service.PostWithdrawalPayout(ctx, tx, fx.tenant,
					fx.referrer, "CNY", amount, withdrawalID)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE withdrawals
					SET status='paid',payout_reference='pg18-ledger',completed_at=now(),updated_at=now()
					WHERE tenant_id=$1 AND id=$2::uuid AND payout_txn_id=$3::uuid`,
					fx.tenant, withdrawalID, payoutTxn); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
				return err
			})
		if err != nil {
			t.Fatalf("pay out withdrawal %s: %v", withdrawalID, err)
		}
	}

	if base := summary(t).Available; base != 0 {
		t.Fatalf("referrer must start with no available commission, got %d", base)
	}

	// 1) 转余额之后，同一笔钱不能再申请提现。
	fund(t, 2)
	wantAvailable(t, 200)
	if _, err := service.TransferCommissionToBalance(ctx, fx.tenant, fx.referrer, 150); err != nil {
		t.Fatalf("transfer 150: %v", err)
	}
	wantAvailable(t, 50)
	_, err := service.RequestWithdrawal(ctx, fx.tenant, fx.referrer, 100, "")
	if !errors.Is(err, ErrWithdrawTooMuch) {
		t.Fatalf("withdraw after transfer err=%v, want ErrWithdrawTooMuch", err)
	}
	t.Log("marker=commission_ledger_withdraw_after_transfer_rejected_ok")

	// 2) 提现在途时，被占用的部分不能再转余额。
	fund(t, 1)
	wantAvailable(t, 150)
	withdrawalID, err := service.RequestWithdrawal(ctx, fx.tenant, fx.referrer, 100, "")
	if err != nil {
		t.Fatalf("withdraw 100: %v", err)
	}
	if sum := summary(t); sum.Available != 50 || sum.Withdrawing != 100 {
		t.Fatalf("summary after withdrawal available=%d withdrawing=%d, want 50/100",
			sum.Available, sum.Withdrawing)
	}
	_, err = service.TransferCommissionToBalance(ctx, fx.tenant, fx.referrer, 51)
	if !errors.Is(err, ErrCommissionTransferInsufficient) {
		t.Fatalf("transfer over reserved commission err=%v, want ErrCommissionTransferInsufficient", err)
	}
	if _, err := service.TransferCommissionToBalance(ctx, fx.tenant, fx.referrer, 50); err != nil {
		t.Fatalf("transfer the unreserved 50: %v", err)
	}
	wantAvailable(t, 0)
	t.Log("marker=commission_ledger_transfer_during_withdrawal_rejected_ok")

	// 3) 打款时账本恰好够：被占用的 100 一直没有被别的路径动过。
	payOut(t, withdrawalID, 100)
	if sum := summary(t); sum.Available != 0 || sum.Withdrawing != 0 || sum.Settled < 100 {
		t.Fatalf("summary after payout available=%d withdrawing=%d settled=%d",
			sum.Available, sum.Withdrawing, sum.Settled)
	}
	t.Log("marker=commission_ledger_payout_funded_ok")

	// 4) 同一时刻转余额与申请提现争同一笔 100：只能有一个成功，
	// 输的一方拿到业务错误而不是序列化失败或 500。
	fund(t, 1)
	wantAvailable(t, 100)
	var wg sync.WaitGroup
	var transferErr, withdrawErr error
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, transferErr = service.TransferCommissionToBalance(ctx, fx.tenant, fx.referrer, 100)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, withdrawErr = service.RequestWithdrawal(ctx, fx.tenant, fx.referrer, 100, "")
	}()
	close(start)
	wg.Wait()
	switch {
	case transferErr == nil && withdrawErr != nil:
		wantCode(t, withdrawErr, httpx.CodeValidationFailed, httpx.CodeConflict)
	case withdrawErr == nil && transferErr != nil:
		wantCode(t, transferErr, httpx.CodeConflict)
	default:
		t.Fatalf("race must have exactly one winner transfer=%v withdraw=%v", transferErr, withdrawErr)
	}
	wantAvailable(t, 0)
	t.Log("marker=commission_ledger_race_single_winner_ok")
}

// orderReleasePG18LatePaymentCurrencyCases 证明挂账待处理合计按币种分开（缺陷 13）。
// 它会用超级用户绕过触发器塞一条美元挂账，所以必须是 TestOrderReleasePG18 的最后一步。
func orderReleasePG18LatePaymentCurrencyCases(t *testing.T, ctx context.Context,
	admin *pgx.Conn, service *Service, fx orderReleasePG18Fixture) {

	t.Helper()
	pendingBefore := func() map[string]int64 {
		t.Helper()
		_, _, pending, err := service.ListLatePayments(ctx, fx.tenant, ListLatePaymentsInput{})
		if err != nil {
			t.Fatalf("list late payments: %v", err)
		}
		return pending
	}
	before := pendingBefore()
	if before["CNY"] <= 0 {
		t.Fatalf("fixture must already hold CNY suspense cases, pending=%v", before)
	}
	if _, ok := before["USD"]; ok {
		t.Fatalf("fixture unexpectedly holds USD suspense, pending=%v", before)
	}

	// 这张美元挂账在正常路径上造不出来（夹具只有人民币价格）；它只是一个
	// 让「跨币种相加」现形的探针，不需要账务证据链成立。
	if _, err := admin.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatalf("disable triggers for USD probe: %v", err)
	}
	_, seedErr := admin.Exec(ctx, `
		INSERT INTO late_payment_cases
			(tenant_id, order_id, payment_id, amount, currency, status, suspense_txn_id)
		SELECT tenant_id, order_id, gen_random_uuid(), 777, 'USD', 'suspense', gen_random_uuid()
		  FROM late_payment_cases WHERE tenant_id = $1 LIMIT 1`, fx.tenant)
	if _, err := admin.Exec(ctx, `SET session_replication_role = origin`); err != nil {
		t.Fatalf("restore trigger role: %v", err)
	}
	if seedErr != nil {
		t.Fatalf("seed USD suspense probe: %v", seedErr)
	}

	after := pendingBefore()
	if after["USD"] != 777 || after["CNY"] != before["CNY"] || len(after) != 2 {
		t.Fatalf("pending amounts before=%v after=%v, want CNY unchanged and USD=777 kept apart",
			before, after)
	}
	t.Log("marker=late_payment_pending_amounts_by_currency_ok")
}
