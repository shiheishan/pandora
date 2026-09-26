// [INPUT]: 依赖 unexpected_payment.go 的源码文本
// [OUTPUT]: 对外提供异常收款隔离的源码契约测试（重放分支、挂账证据形状、case_kind 白名单）
// [POS]: billing 的源码契约门禁，与 00040 / 00095 的挂账守卫对照；真实 SQL 下的效果由 order_release 与 plan_change 两个 PG18 域证明
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"os"
	"strings"
	"testing"
)

func unexpectedPaymentSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("unexpected_payment.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestUnexpectedPaymentQuarantineOrderContract(t *testing.T) {
	s := unexpectedPaymentSource(t)
	ordered := []string{
		`pg_advisory_xact_lock`,
		`SELECT id::text,order_id::text,currency::text,amount,fee_amount,status`,
		`accountSpecs := []ledgerAccountSpec`,
		`prepareAndLockLedgerAccounts(`,
		`INSERT INTO payments`,
		`Post(ctx, tx`,
		`INSERT INTO late_payment_cases`,
		`processing_status='processed'`,
		`audit.Write(`,
		`SET CONSTRAINTS ALL IMMEDIATE`,
		`Processed: true`,
	}
	last := -1
	for _, needle := range ordered {
		at := strings.LastIndex(s, needle)
		if at < 0 {
			t.Fatalf("unexpected-payment quarantine missing %q", needle)
		}
		if at <= last {
			t.Fatalf("unexpected-payment quarantine order is not monotonic at %q", needle)
		}
		last = at
	}
}

func TestUnexpectedPaymentQuarantineIdempotencyContract(t *testing.T) {
	s := unexpectedPaymentSource(t)
	start := strings.Index(s, "if err == nil {")
	if start < 0 {
		t.Fatal("existing provider payment branch is missing")
	}
	end := strings.Index(s[start:], "if err != nil && !errors.Is(err, pgx.ErrNoRows)")
	if end < 0 {
		t.Fatal("existing provider payment branch boundary is missing")
	}
	branch := s[start : start+end]
	for _, needle := range []string{
		`existingOrderID != orderID`,
		`existingCurrency != in.Currency`,
		`existingAmount != in.Amount`,
		`existingFee != in.FeeAmount`,
		`processing_status='ignored'`,
		`processing_status='pending'`,
		`tag.RowsAffected() != 1`,
		`SET CONSTRAINTS ALL IMMEDIATE`,
		`AlreadyHandled: true`,
	} {
		if !strings.Contains(branch, needle) {
			t.Errorf("unexpected-payment idempotency branch missing %q", needle)
		}
	}
}

func TestUnexpectedPaymentQuarantineEvidenceContract(t *testing.T) {
	s := unexpectedPaymentSource(t)
	for _, needle := range []string{
		`case "released_order", "excess_capture", "ineligible_subscription":`,
		`provider_id=$2::uuid AND provider_payment_id=$3`,
		`strings.TrimSpace(in.Currency) == ""`,
		`in.Amount <= 0`,
		`in.FeeAmount < 0 || in.FeeAmount > in.Amount`,
		`AccountType("late_payment_suspense")`,
		`AccountType: AccountChannelCash`,
		`AccountType: AccountPlatformFeeExpense`,
		`payment_intent_id`,
		`$4,NULL,$5`,
		`Direction: Debit, Amount: net`,
		`Direction: Debit, Amount: in.FeeAmount`,
		`Direction: Credit, Amount: in.Amount`,
		`Kind: "late_payment_suspense"`,
		`SourceType: "payment", SourceID: &paymentID`,
		`payment_event_id,case_kind`,
		`provider_payment_id=$4 AND processing_status='pending'`,
		`Action: "payment.quarantined"`,
		`tag.RowsAffected() != 1`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("unexpected-payment evidence contract missing %q", needle)
		}
	}
	if count := strings.Count(s, `SET CONSTRAINTS ALL IMMEDIATE`); count != 2 {
		t.Fatalf("expected duplicate and processed constraint gates, got %d", count)
	}
	if count := strings.Count(s, `tag.RowsAffected() != 1`); count != 3 {
		t.Fatalf("expected three exact mutation checks, got %d", count)
	}
}
