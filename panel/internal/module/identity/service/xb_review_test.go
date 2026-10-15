package service

import (
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestReservedEmail(t *testing.T) {
	id := uuid.MustParse("01890000-0000-7000-8000-000000000001")
	for email, want := range map[string]bool{
		DeletedEmail(id):               true,
		"x@deleted.invalid":            true,
		"X@DELETED.INVALID":            true,
		"x@Deleted.Invalid ":           true,
		"x@example.test":               false,
		"deleted.invalid@example.test": false,
		"x@sub.deleted.invalid":        false,
		"no-at-sign":                   false,
	} {
		if got := reservedEmail(email); got != want {
			t.Errorf("reservedEmail(%q) = %v, want %v", email, got, want)
		}
	}
}

// 批量生成账号不能用占位邮箱的域名（任何大小写）。
func TestGenerateRejectsReservedDomain(t *testing.T) {
	for _, d := range []string{"deleted.invalid", "DELETED.invalid"} {
		_, err := normalizeGenerateInput(GenerateInput{Count: 1, EmailPrefix: "gen", EmailDomain: d, Reason: "批量生成测试账号"})
		if e := errs.As(err); e.Code != errs.ValidationFailed || e.Fields["email_domain"] == "" {
			t.Fatalf("%s: want 422 email_domain, got %v", d, err)
		}
	}
	if _, err := normalizeGenerateInput(GenerateInput{Count: 1, EmailPrefix: "gen", EmailDomain: "example.test", Reason: "批量生成测试账号"}); err != nil {
		t.Fatalf("普通域名应通过: %v", err)
	}
}
