package money

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCentsRoundTrip(t *testing.T) {
	if got := FromCents(990); !got.Equal(dec("9.90")) {
		t.Fatalf("990 分 = %s", got)
	}
	if n, err := ToCents(dec("9.90")); err != nil || n != 990 {
		t.Fatalf("9.90 → %d, %v", n, err)
	}
	if _, err := ToCents(dec("9.901")); !errors.Is(err, ErrPrecision) {
		t.Fatalf("3 位小数应拒绝: %v", err)
	}
	if n, err := ToCents(dec("-0.01")); err != nil || n != -1 {
		t.Fatalf("-0.01 → %d, %v", n, err)
	}
}

func TestParseProvider(t *testing.T) {
	for in, want := range map[string]string{"9.90": "9.9", "9.9": "9.9", "10": "10", " 0.01 ": "0.01", "9.900": "9.9"} {
		got, err := ParseProvider(in)
		if err != nil || !got.Equal(dec(want)) {
			t.Fatalf("%q → %s, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "9.901", "1e2", "-1", "abc", "9,90", "0x10"} {
		if _, err := ParseProvider(bad); err == nil {
			t.Fatalf("%q 应拒绝", bad)
		}
	}
	if got := FormatProvider(dec("9.9")); got != "9.90" {
		t.Fatalf("FormatProvider=%s", got)
	}
}

func TestMulRatioTruncatesTowardZero(t *testing.T) {
	cases := []struct {
		d        string
		num, den int64
		want     string
	}{
		{"9.99", 1, 3, "3.33"},
		{"10.00", 2, 3, "6.66"},
		{"-1.00", 1, 3, "-0.33"},
		{"100.00", 1500, BasisPoints, "15"},
		{"0.01", 1, 2, "0"},
	}
	for _, c := range cases {
		if got := MulRatio(dec(c.d), c.num, c.den); !got.Equal(dec(c.want)) {
			t.Fatalf("%s×%d/%d=%s want %s", c.d, c.num, c.den, got, c.want)
		}
	}
	if got := ApplyBP(dec("99.99"), 1234); !got.Equal(dec("12.33")) {
		t.Fatalf("ApplyBP=%s", got)
	}
}

func TestCheckAndCurrency(t *testing.T) {
	if err := CheckNonNegative(dec("-0.01")); !errors.Is(err, ErrNegative) {
		t.Fatalf("负数: %v", err)
	}
	if err := Check(dec("1.234")); !errors.Is(err, ErrPrecision) {
		t.Fatalf("精度: %v", err)
	}
	if c, err := ParseCurrency("cny"); err != nil || c != CNY {
		t.Fatalf("cny → %q, %v", c, err)
	}
	if _, err := ParseCurrency("JPY"); !errors.Is(err, ErrCurrency) {
		t.Fatalf("JPY: %v", err)
	}
}
