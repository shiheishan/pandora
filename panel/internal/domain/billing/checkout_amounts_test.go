package billing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
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

// 后台待支付单「用户付不了」只有一个判定：建单 422 与 preview 的 below_minimum 共用。
// 应付为 0（赠送抵满、换套餐抵满）不算，最低额 ≤ 1 分等于不限。
func TestManualDueBelowMinimum(t *testing.T) {
	cases := []struct {
		due, min int64
		want     bool
	}{
		{2500, 3000, true},
		{3000, 3000, false},
		{0, 3000, false},
		{50, 1, false},
		{50, 0, false},
	}
	for _, tc := range cases {
		if got := manualDueBelowMinimum(tc.due, tc.min); got != tc.want {
			t.Errorf("manualDueBelowMinimum(%d,%d)=%v want %v", tc.due, tc.min, got, tc.want)
		}
	}
	// 建单的 422 带 fields.settlement：后台据它落到「结算方式」，不靠文案
	if errManualBelowMinimum.Code != httpx.CodeValidationFailed ||
		errManualBelowMinimum.Fields["settlement"] != errManualBelowMinimum.Message {
		t.Fatalf("manual below-minimum error=%+v", errManualBelowMinimum)
	}
	// 超过付款期限有自己的码（409 order_lapsed），门户不再匹配文案
	if ErrOrderPaymentExpired.Code != httpx.CodeOrderLapsed {
		t.Fatalf("lapsed order code=%s", ErrOrderPaymentExpired.Code)
	}
}

// preview 的选项是 purchase.Placement 原样展平再加 due / below_minimum 两项，外加顶层 min_payment；
// 后台 schema（admin/screens/billing/schemas.ts）按这个形状校验。
func TestManualOrderPreviewJSONShape(t *testing.T) {
	b, err := json.Marshal(ManualOrderPreview{Options: []ManualPlacement{{
		Placement: purchase.Placement{Option: purchase.Option{Key: "new", Kind: purchase.KindNew}},
		Due:       2500, BelowMinimum: true,
	}}, MinPayment: 3000})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"key":"new"`, `"kind":"new"`, `"due":2500`, `"below_minimum":true`,
		`"default_key":""`, `"min_payment":3000`} {
		if !strings.Contains(got, want) {
			t.Fatalf("preview JSON %s lacks %s", got, want)
		}
	}
}
