package service

import (
	"testing"

	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/shopspring/decimal"
)

func TestMaskEmail(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ab@x.example", "***@x.example"},
		{"alice@example.com", "al***@example.com"},
		{"bad", "***"},
		{"a@example.com", "***@example.com"},
		{"abc@example.com", "ab***@example.com"},
	}
	for _, tc := range tests {
		got := maskEmail(tc.input)
		if got != tc.want {
			t.Errorf("maskEmail(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPaidAmount(t *testing.T) {
	got := paidAmount(ordersvc.PaidNotice{})
	if got != "—" {
		t.Fatalf("paidAmount = %q, want %q", got, "—")
	}
	if got := paidAmount(ordersvc.PaidNotice{Total: decimal.RequireFromString("29.9"), Currency: "CNY"}); got != "29.90 CNY" {
		t.Fatalf("paidAmount = %q, want 29.90 CNY", got)
	}
}
