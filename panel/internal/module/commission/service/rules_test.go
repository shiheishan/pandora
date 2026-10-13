package service

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// 佣金公式向零截断，只在最后一步截断一次。
func TestAmountTruncatesTowardZero(t *testing.T) {
	cases := []struct {
		base string
		bp   int64
		want string
	}{
		{"100.00", 1000, "10.00"},
		{"0.99", 1000, "0.09"},      // 0.099 → 0.09
		{"33.33", 3333, "11.10"},    // 11.108889 → 11.10
		{"0.03", 3333, "0.00"},      // 0.009999 → 0
		{"19.99", 100, "0.19"},      // 0.1999 → 0.19
		{"1000.00", 5000, "500.00"}, // 上限 50%
		{"0.01", 5000, "0.00"},
	}
	for _, c := range cases {
		got := Amount(dec(c.base), c.bp)
		if !got.Equal(dec(c.want)) {
			t.Errorf("Amount(%s, %d) = %s，想要 %s", c.base, c.bp, got, c.want)
		}
	}
}

// rate_bp 由整数百分比换算，边界 1% 与 50% 落在库里 CHECK (0, 5000] 之内。
func TestRateBPBounds(t *testing.T) {
	if RateBP(1) != 100 || RateBP(50) != 5000 {
		t.Fatalf("RateBP(1)=%d RateBP(50)=%d", RateBP(1), RateBP(50))
	}
	if RateBP(0) != 0 {
		t.Fatalf("RateBP(0)=%d", RateBP(0))
	}
}

// 待复核来源按原因常量归类；解冻余额不足的原因带金额后缀。
func TestReviewSource(t *testing.T) {
	cases := map[string]string{
		reviewSameSource:         ReviewSourceSameSource,
		reviewSourceCheckFailed:  ReviewSourceSourceCheck,
		reviewSourceCheckMissing: ReviewSourceSourceCheck,
		reviewUnderfundedPrefix + "，无法解冻 10.00 USD": ReviewSourceUnderfunded,
		"手工标记": ReviewSourceOther,
		"":     ReviewSourceOther,
	}
	for reason, want := range cases {
		if got := reviewSource(reason); got != want {
			t.Errorf("reviewSource(%q) = %s，期望 %s", reason, got, want)
		}
	}
}

func TestTransitions(t *testing.T) {
	if !CanEntryTransition(EntryPending, EntryAvailable) || CanEntryTransition(EntryAvailable, EntryPending) {
		t.Fatal("条目转换表不对")
	}
	// 作废只从 pending 来，且是终态；已解冻的不能作废
	if !CanEntryTransition(EntryPending, EntryVoided) || CanEntryTransition(EntryAvailable, EntryVoided) ||
		CanEntryTransition(EntryVoided, EntryPending) || CanEntryTransition(EntryVoided, EntryAvailable) {
		t.Fatal("作废转换不对")
	}
	ok := [][2]string{
		{WithdrawalRequested, WithdrawalApproved},
		{WithdrawalRequested, WithdrawalRejected},
		{WithdrawalApproved, WithdrawalPaid},
	}
	for _, p := range ok {
		if !CanWithdrawalTransition(p[0], p[1]) {
			t.Errorf("%s → %s 应允许", p[0], p[1])
		}
	}
	bad := [][2]string{
		{WithdrawalRequested, WithdrawalPaid},
		{WithdrawalApproved, WithdrawalRejected},
		{WithdrawalRejected, WithdrawalApproved},
		{WithdrawalPaid, WithdrawalRejected},
	}
	for _, p := range bad {
		if CanWithdrawalTransition(p[0], p[1]) {
			t.Errorf("%s → %s 不应允许", p[0], p[1])
		}
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.test": "al***@example.test",
		"ab@example.test":    "***@example.test",
		"":                   "***",
		"noat":               "***",
	}
	for in, want := range cases {
		if got := MaskEmail(in); got != want {
			t.Errorf("MaskEmail(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestMonthStartUsesSiteTimezone(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC) // 上海已是 11 月 1 日 04:00
	got := monthStart(now, "Asia/Shanghai")
	want := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("monthStart = %s，想要 %s", got, want)
	}
	if got := monthStart(now, "bad/zone"); !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("无效时区按 UTC：%s", got)
	}
}

func TestValidateWithdrawal(t *testing.T) {
	if _, err := validateWithdrawal(dec("0"), "alipay"); err == nil {
		t.Fatal("金额 0 应报错")
	}
	if _, err := validateWithdrawal(dec("1.001"), "alipay"); err == nil {
		t.Fatal("三位小数应报错")
	}
	if _, err := validateWithdrawal(dec("1"), "  "); err == nil {
		t.Fatal("空收款方式应报错")
	}
	if _, err := validateWithdrawal(dec("1"), "a\x00b"); err == nil {
		t.Fatal("含 NUL 应报错")
	}
	got, err := validateWithdrawal(dec("1"), "  支付宝 a@b.test ")
	if err != nil || got != "支付宝 a@b.test" {
		t.Fatalf("got %q err %v", got, err)
	}
}
