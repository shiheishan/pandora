// [INPUT]: 依赖 platform/sourcetest 按名取佣金解冻、提现打款、提现申请、转余额与可用佣金口径各函数的源码，依赖 ErrWithdrawCurrencyAmbiguous
// [OUTPUT]: 对外提供 TestCommissionMaturityPerEntryTransactionContract、TestCommissionMaturityLockBalanceAndCASContract、TestCommissionPayoutLockBalanceAndMonotonicStateContract、TestRequestWithdrawalUsesLedgerAvailabilityUnderSharedLock、TestCommissionTransferUsesSameLedgerAvailability、TestCommissionAvailabilityDeductsOnlyUnpostedWithdrawals、TestWithdrawCurrencyAmbiguousErrorIsStableConflict
// [POS]: billing 分销佣金的并发源码契约：解冻逐笔一事务、先锁后校余额再 CAS 过账，打款与提现申请的锁序，可用佣金 = 账本余额 − 未过账在途提现（D-F-1）在提现、转余额与摘要三处同一口径
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// commissionDecl 按名取佣金声明的源码；函数挪到哪个文件都不影响。
func commissionDecl(t *testing.T, name string) string {
	t.Helper()
	return sourcetest.Load(t, ".").Decl(name)
}

func commissionConcurrencySection(t *testing.T, source, start, end string) string {
	t.Helper()
	begin := strings.Index(source, start)
	if begin < 0 {
		t.Fatalf("commission source boundary missing %q", start)
	}
	finish := strings.Index(source[begin+len(start):], end)
	if finish < 0 {
		t.Fatalf("commission source boundary missing %q after %q", end, start)
	}
	return source[begin : begin+len(start)+finish]
}

func assertCommissionSourceOrder(t *testing.T, source string, ordered ...string) {
	t.Helper()
	last := -1
	for _, needle := range ordered {
		at := strings.Index(source, needle)
		if at < 0 {
			t.Fatalf("commission concurrency contract missing %q", needle)
		}
		if at <= last {
			t.Fatalf("commission concurrency order is not monotonic at %q", needle)
		}
		last = at
	}
}

func TestCommissionMaturityPerEntryTransactionContract(t *testing.T) {
	worker := commissionDecl(t, "Service.SettleMatured")
	assertCommissionSourceOrder(t, worker,
		"discoverMaturedCommissionIDs(ctx, tenantID)",
		"for _, entryID := range ids",
		"settleMaturedCommission(ctx, tenantID, entryID)",
		"failures = append(failures",
		"errors.Join(failures...)",
	)
	if strings.Contains(worker, "prepareAndLockLedgerAccounts(") ||
		strings.Contains(worker, "Post(ctx, tx") {
		t.Fatal("SettleMatured must not hold one transaction across account locks and postings")
	}

	discovery := commissionDecl(t, "Service.discoverMaturedCommissionIDs")
	for _, needle := range []string{
		"s.pool.InTx(",
		"status='pending'",
		"review_required=false AND review_reason IS NULL",
		"settle_txn_id IS NULL",
		"LIMIT $2 FOR UPDATE SKIP LOCKED",
		"maxMaturedCommissionBatch",
	} {
		if !strings.Contains(discovery, needle) {
			t.Errorf("bounded commission discovery missing %q", needle)
		}
	}
	if strings.Contains(discovery, "Post(ctx, tx") ||
		strings.Contains(discovery, "prepareAndLockLedgerAccounts(") {
		t.Fatal("candidate discovery transaction must not lock accounts or post ledger entries")
	}
}

func TestCommissionMaturityLockBalanceAndCASContract(t *testing.T) {
	entry := commissionDecl(t, "Service.settleMaturedCommission")
	assertCommissionSourceOrder(t, entry,
		"s.pool.InTx(",
		"FROM commission_entries",
		"FROM orders",
		"FOR UPDATE",
		"var lockedOrderID",
		`status != "pending" || reviewRequired || reviewReason != nil || !due || settleTxnID != nil`,
		`if orderStatus != "paid" && orderStatus != "fulfilled"`,
		"ineligible commission review quarantine transition lost",
		"prepareAndLockLedgerAccounts(",
		`pendingBalance, err := Balance(`,
		"if pendingBalance < amount",
		"underfunded commission review quarantine transition lost",
		"Post(ctx, tx",
		"status='available',settle_txn_id=$3::uuid",
	)
	for _, needle := range []string{
		`AND status='pending' AND review_required=false`,
		`AND settle_txn_id IS NULL`,
		`AND frozen_until IS NOT NULL AND frozen_until<=now()`,
		`errCommissionSettlementUnderfunded`,
		`errCommissionOrderIneligible`,
		`warning = fmt.Errorf`,
		`committed = true`,
	} {
		if !strings.Contains(entry, needle) {
			t.Errorf("per-entry commission settlement missing %q", needle)
		}
	}
	if count := strings.Count(entry, "tag.RowsAffected() != 1"); count != 3 {
		t.Fatalf("ineligible, underfunded and settlement paths each need exact CAS, got %d", count)
	}
	if count := strings.Count(entry, "SET CONSTRAINTS ALL IMMEDIATE"); count != 3 {
		t.Fatalf("both review paths and successful settlement each need a constraint gate, got %d", count)
	}
}

func TestCommissionPayoutLockBalanceAndMonotonicStateContract(t *testing.T) {
	// 窗口截到函数里第一处 return txnID, nil，与原先按文件截取的范围相同
	payout := commissionConcurrencySection(t, commissionDecl(t, "Service.PostWithdrawalPayout"),
		"func (s *Service) PostWithdrawalPayout(",
		"return txnID, nil")
	assertCommissionSourceOrder(t, payout,
		"FROM withdrawals",
		"FOR UPDATE",
		`status != "approved" || existingTxn != nil`,
		"prepareAndLockLedgerAccounts(",
		`availableBalance, err := Balance(`,
		"if availableBalance < amount",
		"Post(ctx, tx",
		"status='processing',payout_txn_id=$3::uuid",
		"tag.RowsAffected() != 1",
		"SET CONSTRAINTS ALL IMMEDIATE",
	)
	for _, needle := range []string{
		`Key: "available", AccountType: AccountUserCommissionAvailable`,
		`Key: "channel", AccountType: AccountChannelCash`,
		`Currency: currency, OwnerRef: "payout"`,
		`errCommissionPayoutUnderfunded`,
		`AND status='approved' AND payout_txn_id IS NULL`,
		`AccountID: availableAcct, Direction: Debit`,
		`AccountID: channelAcct, Direction: Credit`,
	} {
		if !strings.Contains(payout, needle) {
			t.Errorf("commission payout contract missing %q", needle)
		}
	}
	if strings.Contains(payout, "EnsureAccount(") {
		t.Fatal("payout must not acquire account locks through unordered EnsureAccount calls")
	}
}

// D-F-1：提现申请、转余额与摘要只认一个「可用佣金」—— 账本余额 − 未过账的在途提现，
// 两条动账路径在同一把科目锁下按这个口径校验。
func TestRequestWithdrawalUsesLedgerAvailabilityUnderSharedLock(t *testing.T) {
	request := commissionDecl(t, "Service.RequestWithdrawal")
	assertCommissionSourceOrder(t, request,
		"s.pool.InTxSerializableRetry(",
		"if amount < cfg.MinWithdraw",
		"lockUserCommissionAccounts(ctx, tx, tenantID, userID)",
		"if len(currencies) == 0",
		"FROM withdrawals",
		"if inFlight > 0",
		"withdrawableCommission(ctx, tx, tenantID, userID,",
		"if positive > 1",
		"return ErrWithdrawCurrencyAmbiguous",
		"if positive == 0",
		"if amount > available",
		"INSERT INTO withdrawals",
	)
	for _, forbidden := range []string{
		"FROM commission_entries",
		"status = 'paid'",
		"max(currency::text)",
		"min(currency::text)",
	} {
		if strings.Contains(request, forbidden) {
			t.Errorf("withdrawal request must not derive availability from %q", forbidden)
		}
	}
}

func TestCommissionTransferUsesSameLedgerAvailability(t *testing.T) {
	transfer := commissionConcurrencySection(t, commissionDecl(t, "Service.TransferCommissionToBalance"),
		"func (s *Service) TransferCommissionToBalance(",
		"return txnID, nil")
	assertCommissionSourceOrder(t, transfer,
		"s.pool.InTxSerializableRetry(",
		"prepareAndLockLedgerAccounts(",
		"withdrawableCommission(ctx, tx, tenantID, userID,",
		"if avail < amount",
		"return ErrCommissionTransferInsufficient",
		"Post(ctx, tx",
	)
	if strings.Contains(transfer, "Balance(ctx, tx, accounts") {
		t.Fatal("transfer must not bypass the shared availability rule with a raw ledger balance")
	}
}

func TestCommissionAvailabilityDeductsOnlyUnpostedWithdrawals(t *testing.T) {
	query := commissionDecl(t, "unpostedWithdrawalsSQL")
	if !strings.Contains(query, "status IN ('requested','reviewing','approved')") {
		t.Fatalf("unposted withdrawals must be exactly the pre-payout states: %s", query)
	}
	for _, posted := range []string{"'processing'", "'paid'"} {
		if strings.Contains(query, posted) {
			t.Fatalf("%s withdrawals already debited the ledger and must not be deducted twice", posted)
		}
	}
	rule := commissionDecl(t, "withdrawableCommission")
	assertCommissionSourceOrder(t, rule,
		"Balance(ctx, tx, accountID)",
		"unpostedWithdrawalsSQL",
		"return ledger - reserved, nil",
	)

	summary := commissionDecl(t, "Service.CommissionSummary")
	if !strings.Contains(summary, "commissionAvailableSnapshot(ctx, tx,") {
		t.Fatal("commission summary must display the shared availability rule")
	}
	if strings.Contains(summary, "FILTER (WHERE status = 'available')") {
		t.Fatal("commission summary must not derive availability from commission entries")
	}
}

func TestWithdrawCurrencyAmbiguousErrorIsStableConflict(t *testing.T) {
	if ErrWithdrawCurrencyAmbiguous.Code != "conflict" {
		t.Fatalf("ambiguous withdrawal currency code=%q want=conflict",
			ErrWithdrawCurrencyAmbiguous.Code)
	}
	if !strings.Contains(ErrWithdrawCurrencyAmbiguous.Message, "多个币种") {
		t.Fatalf("ambiguous withdrawal currency message is not actionable: %q",
			ErrWithdrawCurrencyAmbiguous.Message)
	}
}
