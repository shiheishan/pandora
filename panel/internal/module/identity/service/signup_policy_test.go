package service

import (
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestCheckEmailPolicy(t *testing.T) {
	base := settingssvc.Signup{
		EmailSuffixWhitelistEnabled: false,
		GmailAliasBlock:             false,
	}

	cases := []struct {
		name  string
		cfg   settingssvc.Signup
		email string
		want  string // 空表示应通过
	}{
		{"关着全过", base, "any@example.test", ""},
		{"白名单命中", settingssvc.Signup{
			EmailSuffixWhitelistEnabled: true,
			EmailSuffixes:               []string{"gmail.com", "qq.com"},
		}, "u@gmail.com", ""},
		{"白名单不命中", settingssvc.Signup{
			EmailSuffixWhitelistEnabled: true,
			EmailSuffixes:               []string{"gmail.com"},
		}, "u@outlook.com", "暂不支持这个邮箱后缀"},
		{"提示最多10个", settingssvc.Signup{
			EmailSuffixWhitelistEnabled: true,
			EmailSuffixes: []string{
				"a.com", "b.com", "c.com", "d.com", "e.com",
				"f.com", "g.com", "h.com", "i.com", "j.com", "k.com",
			},
		}, "u@z.com", "等"},
		{"gmail带点拒", settingssvc.Signup{GmailAliasBlock: true}, "a.b@gmail.com", "Gmail 别名"},
		{"gmail带加号拒", settingssvc.Signup{GmailAliasBlock: true}, "a+b@gmail.com", "Gmail 别名"},
		{"googlemail拒", settingssvc.Signup{GmailAliasBlock: true}, "a.b@googlemail.com", "Gmail 别名"},
		{"outlook带点放行", settingssvc.Signup{GmailAliasBlock: true}, "a.b@outlook.com", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkEmailPolicy(tc.cfg, tc.email)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			e, ok := err.(*errs.Error)
			if !ok {
				t.Fatalf("%T", err)
			}
			msg := e.Fields["email"]
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("msg %q want %q", msg, tc.want)
			}
		})
	}
}
