package service

import (
	"testing"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

func ovIntPtr(n int) *int { return &n }

func ovStrPtr(s string) *string { return &s }

func TestValidateOverrideInput(t *testing.T) {
	cases := []struct {
		name      string
		in        OverrideInput
		wantEmail string
		wantField string // 期望出现的字段键；空表示无错
	}{
		{
			name:      "合法_去空白与比例",
			in:        OverrideInput{Email: " a@example.test ", RatePercent: ovIntPtr(10)},
			wantEmail: "a@example.test",
		},
		{
			name:      "合法_仅 scope",
			in:        OverrideInput{Email: "a@example.test", Scope: ovStrPtr("first_order")},
			wantEmail: "a@example.test",
		},
		{
			name:      "合法_比例为 0",
			in:        OverrideInput{Email: "a@example.test", RatePercent: ovIntPtr(0)},
			wantEmail: "a@example.test",
		},
		{
			name:      "空邮箱",
			in:        OverrideInput{RatePercent: ovIntPtr(1)},
			wantEmail: "",
			wantField: "email",
		},
		{
			name:      "比例 51",
			in:        OverrideInput{Email: "a@example.test", RatePercent: ovIntPtr(51)},
			wantEmail: "a@example.test",
			wantField: "rate_percent",
		},
		{
			name:      "比例 -1",
			in:        OverrideInput{Email: "a@example.test", RatePercent: ovIntPtr(-1)},
			wantEmail: "a@example.test",
			wantField: "rate_percent",
		},
		{
			name:      "非法 scope",
			in:        OverrideInput{Email: "a@example.test", Scope: ovStrPtr("weekly")},
			wantEmail: "a@example.test",
			wantField: "scope",
		},
		{
			name:      "两项都 nil",
			in:        OverrideInput{Email: "a@example.test"},
			wantEmail: "a@example.test",
			wantField: "rate_percent",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			email, fields := validateOverrideInput(c.in)
			if email != c.wantEmail {
				t.Errorf("email = %q，想要 %q", email, c.wantEmail)
			}
			if c.wantField == "" {
				if fields != nil {
					t.Errorf("期望无字段错误，得到 %v", fields)
				}
				return
			}
			if fields == nil {
				t.Fatalf("期望字段 %q 有错，fields 为 nil", c.wantField)
			}
			if _, ok := fields[c.wantField]; !ok {
				t.Errorf("期望 fields[%q] 存在，得到 %v", c.wantField, fields)
			}
		})
	}
}

func TestOverrideData(t *testing.T) {
	if got := overrideData(model.Override{}, false); got != nil {
		t.Fatalf("ok=false 应返回 nil，得到 %v", got)
	}
	got := overrideData(model.Override{RatePercent: nil, Scope: ovStrPtr("every_order")}, true)
	if got["rate_percent"] != nil {
		t.Fatalf("rate_percent 应为 nil，得到 %v", got["rate_percent"])
	}
	if got["scope"] != "every_order" {
		t.Fatalf("scope = %v", got["scope"])
	}
	got2 := overrideData(model.Override{RatePercent: ovIntPtr(5), Scope: nil}, true)
	if got2["rate_percent"] != 5 {
		t.Fatalf("rate_percent = %v", got2["rate_percent"])
	}
	if got2["scope"] != nil {
		t.Fatalf("scope 应为 nil，得到 %v", got2["scope"])
	}
}
