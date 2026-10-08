package billing

import (
	"errors"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/domain/purchase"
)

func TestProviderMinAmount(t *testing.T) {
	cases := []struct {
		adapter string
		cfg     map[string]any
		want    int64
	}{
		{"epay", map[string]any{}, 100},                           // 易支付没配按 ¥1.00
		{"epay", map[string]any{"min_amount": float64(200)}, 200}, // JSON 解出来是 float64
		{"epay", map[string]any{"min_amount": "50"}, 50},
		{"epay", map[string]any{"min_amount": float64(0)}, 100},      // 越界按默认
		{"epay", map[string]any{"min_amount": float64(200000)}, 100}, // 越界按默认
		{"demo_hmac", map[string]any{}, 0},
		{"demo_hmac", map[string]any{"min_amount": int64(300)}, 300},
	}
	for _, tc := range cases {
		if got := providerMinAmount(tc.adapter, tc.cfg); got != tc.want {
			t.Errorf("providerMinAmount(%s,%v)=%d want %d", tc.adapter, tc.cfg, got, tc.want)
		}
	}
}

func TestQuoteTimeWindowAndExpectation(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if at, err := quoteTime(nil, now); err != nil || !at.Equal(now) {
		t.Fatalf("no expectation: %s %v", at, err)
	}
	for name, asOf := range map[string]time.Time{
		"11 minutes ago": now.Add(-11 * time.Minute),
		"in the future":  now.Add(time.Second),
	} {
		if _, err := quoteTime(&Expectation{AsOf: asOf}, now); !errors.Is(err, ErrQuoteChanged) {
			t.Errorf("%s: err=%v want quote_changed", name, err)
		}
	}
	if at, err := quoteTime(&Expectation{AsOf: now.Add(-9 * time.Minute)}, now); err != nil || !at.Equal(now.Add(-9*time.Minute)) {
		t.Fatalf("inside the window: %s %v", at, err)
	}

	b := purchase.Balance{Applied: 2900, Payable: 100}
	ok := &Expectation{AsOf: time.Now(), Total: 3000, BalanceApplied: 2900, Payable: 100}
	if err := checkExpectation(ok, 3000, b); err != nil {
		t.Fatalf("matching expectation: %v", err)
	}
	if err := checkExpectation(nil, 1, b); err != nil {
		t.Fatalf("no expectation must pass: %v", err)
	}
	for name, e := range map[string]Expectation{
		"total":   {AsOf: ok.AsOf, Total: 2999, BalanceApplied: 2900, Payable: 100},
		"applied": {AsOf: ok.AsOf, Total: 3000, BalanceApplied: 2950, Payable: 100},
		"payable": {AsOf: ok.AsOf, Total: 3000, BalanceApplied: 2900, Payable: 50},
		"stale":   {AsOf: time.Now().Add(-time.Hour), Total: 3000, BalanceApplied: 2900, Payable: 100},
	} {
		if err := checkExpectation(&e, 3000, b); !errors.Is(err, ErrQuoteChanged) {
			t.Errorf("%s mismatch err=%v", name, err)
		}
	}
}

func TestLabelWithSuffix(t *testing.T) {
	if got := labelWithSuffix("妈妈的 iPad", 1); got != "妈妈的 iPad" {
		t.Fatal(got)
	}
	if got := labelWithSuffix("妈妈的 iPad", 2); got != "妈妈的 iPad 2" {
		t.Fatal(got)
	}
	long := "一二三四五六七八九十一二三四五六"
	got := labelWithSuffix(long, 12)
	if utf8.RuneCountInString(got) > purchase.MaxLabelRunes || got != "一二三四五六七八九十一二三 12" {
		t.Fatalf("long label suffix=%q", got)
	}
	if n, err := purchase.NormalizeLabel(got); err != nil || n != got {
		t.Fatalf("suffixed label must stay valid: %q %v", n, err)
	}
}
