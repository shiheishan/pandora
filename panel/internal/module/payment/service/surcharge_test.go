package service

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestSurcharge(t *testing.T) {
	cases := []struct {
		amount  string
		fixed   string
		percent string
		want    string
	}{
		{"10.00", "0", "0", "0"},
		{"10.00", "0", "2.5", "0.25"},
		{"9.99", "0", "1", "0.10"},
		{"10.00", "0.30", "0", "0.30"},
		{"33.33", "0.10", "3", "1.10"},
		{"0.01", "0", "0.01", "0.01"},
		{"0", "1", "1", "0"},
		{"100.00", "1.00", "100", "101.00"},
	}
	for _, tc := range cases {
		got := Surcharge(
			decimal.RequireFromString(tc.amount),
			decimal.RequireFromString(tc.fixed),
			decimal.RequireFromString(tc.percent),
		)
		want := decimal.RequireFromString(tc.want)
		if !got.Equal(want) {
			t.Errorf("Surcharge(%s, %s, %s) = %s, want %s", tc.amount, tc.fixed, tc.percent, got, want)
		}
	}
}

func TestValidFee(t *testing.T) {
	if ValidFeeFixed(decimal.RequireFromString("-0.01")) {
		t.Error("-0.01 fixed should be invalid")
	}
	if ValidFeeFixed(decimal.RequireFromString("0.001")) {
		t.Error("0.001 fixed should be invalid")
	}
	if ValidFeeFixed(decimal.RequireFromString("1000.01")) {
		t.Error("1000.01 fixed should be invalid")
	}
	if !ValidFeeFixed(decimal.RequireFromString("0")) {
		t.Error("0 fixed should be valid")
	}
	if !ValidFeeFixed(decimal.RequireFromString("1000.00")) {
		t.Error("1000.00 fixed should be valid")
	}

	if ValidFeePercent(decimal.RequireFromString("-1")) {
		t.Error("-1 percent should be invalid")
	}
	if ValidFeePercent(decimal.RequireFromString("0.001")) {
		t.Error("0.001 percent should be invalid")
	}
	if ValidFeePercent(decimal.RequireFromString("100.01")) {
		t.Error("100.01 percent should be invalid")
	}
	if !ValidFeePercent(decimal.RequireFromString("0")) {
		t.Error("0 percent should be valid")
	}
	if !ValidFeePercent(decimal.RequireFromString("2.5")) {
		t.Error("2.5 percent should be valid")
	}
	if !ValidFeePercent(decimal.RequireFromString("100")) {
		t.Error("100 percent should be valid")
	}
}
