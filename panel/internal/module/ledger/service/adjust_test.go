package service

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestNormalizeAdjustment(t *testing.T) {
	uid := uuid.New()
	validReason := "补偿线路故障五个字"

	cases := []struct {
		name       string
		adj        Adjustment
		wantReason string
		wantCode   errs.Code
		wantField  string // 非空时要求 Fields 含该键
		wantEmpty  bool   // 为 true 时要求 Fields 为空或 nil
	}{
		{
			name:      "金额为零",
			adj:       Adjustment{UserID: uid, Amount: decimal.Zero, Reason: validReason},
			wantCode:  errs.ValidationFailed,
			wantEmpty: true,
		},
		{
			name:      "金额三位小数",
			adj:       Adjustment{UserID: uid, Amount: dec("1.001"), Reason: validReason},
			wantCode:  errs.ValidationFailed,
			wantField: "amount",
		},
		{
			name:      "原因不足五个字",
			adj:       Adjustment{UserID: uid, Amount: dec("1.00"), Reason: "四个字的"},
			wantCode:  errs.ValidationFailed,
			wantField: "reason",
		},
		{
			name:      "原因超过五百字",
			adj:       Adjustment{UserID: uid, Amount: dec("1.00"), Reason: strings.Repeat("字", 501)},
			wantCode:  errs.ValidationFailed,
			wantField: "reason",
		},
		{
			name:      "原因含 NUL",
			adj:       Adjustment{UserID: uid, Amount: dec("1.00"), Reason: "原因五个字\x00坏"},
			wantCode:  errs.ValidationFailed,
			wantField: "reason",
		},
		{
			name:      "原因非法 UTF-8",
			adj:       Adjustment{UserID: uid, Amount: dec("1.00"), Reason: "原因五个字" + string([]byte{0xff, 0xfe})},
			wantCode:  errs.ValidationFailed,
			wantField: "reason",
		},
		{
			name:       "原因首尾空白通过",
			adj:        Adjustment{UserID: uid, Amount: dec("1.00"), Reason: "  补偿五个字  "},
			wantReason: "补偿五个字",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := normalizeAdjustment(tc.adj)
			if tc.wantCode != "" {
				if err == nil {
					t.Fatal("期望错误，得 nil")
				}
				pe := errs.As(err)
				if pe.Code != tc.wantCode {
					t.Fatalf("code: got %s want %s", pe.Code, tc.wantCode)
				}
				if tc.wantEmpty {
					if len(pe.Fields) != 0 {
						t.Fatalf("Fields 应为空，得 %v", pe.Fields)
					}
				}
				if tc.wantField != "" {
					if pe.Fields == nil || pe.Fields[tc.wantField] == "" {
						t.Fatalf("Fields 应含 %q，得 %v", tc.wantField, pe.Fields)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason: got %q want %q", reason, tc.wantReason)
			}
		})
	}
}
