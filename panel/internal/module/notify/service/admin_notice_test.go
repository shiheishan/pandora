package service

import (
	"context"
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
	if got := paidAmount(ordersvc.PaidNotice{}); got != "0.00 元" {
		t.Fatalf("paidAmount = %q, want 0.00 元", got)
	}
	if got := paidAmount(ordersvc.PaidNotice{Total: decimal.RequireFromString("29.9")}); got != "29.90 元" {
		t.Fatalf("paidAmount = %q, want 29.90 元", got)
	}
}

// 只有巡检告警可以不关某个用户；别的管理员提醒缺用户 id 直接报错，不写库（store 为 nil，碰到就会 panic）。
func TestWriteAdminNoticeRejectsMissingUser(t *testing.T) {
	s := &Service{}
	_, err := s.writeAdminNotice(context.Background(), 1, adminNotice{Key: "k", Code: TemplateAdminTicketOpened})
	if err == nil {
		t.Fatal("缺用户 id 的管理员提醒应报错")
	}
}
