package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func ensurePlatformAccount(t *testing.T, e *env, ctx context.Context, accountType, ownerRef string) uuid.UUID {
	t.Helper()
	if err := e.exec(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, owner_ref, balance)
		VALUES (?, 'CNY', ?, 0) ON CONFLICT DO NOTHING`,
		accountType, ownerRef); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := e.row(ctx, `
		SELECT id FROM ledger_accounts
		 WHERE account_type = ? AND currency = 'CNY' AND owner_ref = ?`,
		accountType, ownerRef).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertBalancedTxn(t *testing.T, e *env, ctx context.Context, kind string, occurred time.Time,
	debitAcct, creditAcct uuid.UUID, amount string) {
	t.Helper()
	var txnID uuid.UUID
	if err := e.row(ctx, `
		INSERT INTO ledger_transactions (kind, currency, actor_kind, occurred_at)
		VALUES (?, 'CNY', 'system', ?) RETURNING id`, kind, occurred).Scan(&txnID); err != nil {
		t.Fatal(err)
	}
	for _, leg := range []struct {
		acct uuid.UUID
		dir  string
	}{
		{debitAcct, "debit"},
		{creditAcct, "credit"},
	} {
		if err := e.exec(ctx, `
			INSERT INTO ledger_entries (transaction_id, account_id, currency, direction, amount)
			VALUES (?, ?, 'CNY', ?, ?)`, txnID, leg.acct, leg.dir, amount); err != nil {
			t.Fatal(err)
		}
	}
}

func insertRevenueFixture(t *testing.T, e *env, ctx context.Context) (uuid.UUID, uuid.UUID) {
	t.Helper()
	rev := ensurePlatformAccount(t, e, ctx, "platform_revenue", "main")
	cash := ensurePlatformAccount(t, e, ctx, "channel_cash", "test")
	// 上海 3 月 2 日 00:30
	insertBalancedTxn(t, e, ctx, "order_paid",
		time.Date(2026, 3, 1, 16, 30, 0, 0, time.UTC), cash, rev, "100.00")
	// 上海 3 月 1 日 23:30
	insertBalancedTxn(t, e, ctx, "commission_accrued",
		time.Date(2026, 3, 1, 15, 30, 0, 0, time.UTC), rev, cash, "10.00")
	return rev, cash
}

func findRevenueDay(days []RevenueDay, day string) (RevenueDay, bool) {
	for _, d := range days {
		if d.Day == day && d.Currency == "CNY" {
			return d, true
		}
	}
	return RevenueDay{}, false
}

func TestRevenueByDayTimezonePG(t *testing.T) {
	e, ctx := newEnv(t)
	insertRevenueFixture(t, e, ctx)

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 3, 3, 0, 0, 0, 0, loc)
	days, err := e.svc.RevenueByDay(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	d1, ok := findRevenueDay(days, "2026-03-01")
	if !ok {
		t.Fatalf("缺少 2026-03-01: %v", days)
	}
	if !d1.Credit.Equal(decimal.Zero) || !d1.Debit.Equal(dec("10.00")) {
		t.Fatalf("2026-03-01 口径不对: credit=%s debit=%s", d1.Credit, d1.Debit)
	}
	d2, ok := findRevenueDay(days, "2026-03-02")
	if !ok {
		t.Fatalf("缺少 2026-03-02: %v", days)
	}
	if !d2.Credit.Equal(dec("100.00")) || !d2.Debit.Equal(decimal.Zero) {
		t.Fatalf("2026-03-02 口径不对: credit=%s debit=%s", d2.Credit, d2.Debit)
	}

	fromUTC := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	toUTC := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	daysUTC, err := e.svc.RevenueByDay(ctx, fromUTC, toUTC)
	if err != nil {
		t.Fatal(err)
	}
	dU, ok := findRevenueDay(daysUTC, "2026-03-01")
	if !ok {
		t.Fatalf("UTC 窗口缺少 2026-03-01: %v", daysUTC)
	}
	if !dU.Credit.Equal(dec("100.00")) || !dU.Debit.Equal(dec("10.00")) {
		t.Fatalf("UTC 同日汇总不对: credit=%s debit=%s", dU.Credit, dU.Debit)
	}
}

func TestRevenueTotalsPG(t *testing.T) {
	e, ctx := newEnv(t)
	insertRevenueFixture(t, e, ctx)

	totals, err := e.svc.RevenueTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cny *RevenueTotal
	for i := range totals {
		if totals[i].Currency == "CNY" {
			cny = &totals[i]
			break
		}
	}
	if cny == nil {
		t.Fatal("缺少 CNY 汇总")
	}
	if cny.Credit.LessThan(dec("100.00")) {
		t.Fatalf("CNY credit 下界: %s", cny.Credit)
	}
	if cny.Debit.LessThan(dec("10.00")) {
		t.Fatalf("CNY debit 下界: %s", cny.Debit)
	}
}

func TestDriftAccountsPG(t *testing.T) {
	e, ctx := newEnv(t)

	n0, err := e.svc.DriftAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := "drift-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	if err := e.exec(ctx, `
		INSERT INTO ledger_accounts (account_type, currency, owner_ref, balance)
		VALUES ('suspense', 'CNY', ?, 5.00)`, ref); err != nil {
		t.Fatal(err)
	}
	n1, err := e.svc.DriftAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != n0+1 {
		t.Fatalf("漂移计数: n0=%d n1=%d", n0, n1)
	}
}
