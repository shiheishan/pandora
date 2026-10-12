package service

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNormalizeEmail(t *testing.T) {
	got := normalizeEmail("  Foo@Example.COM ")
	want := "foo@example.com"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLooksLikeEmail(t *testing.T) {
	if !looksLikeEmail("a@b.co") {
		t.Fatal("a@b.co 应为真")
	}
	bad := []string{"", "a", "a@", "@b.co", "a b@c.d"}
	for _, s := range bad {
		if looksLikeEmail(s) {
			t.Fatalf("%q 应为假", s)
		}
	}
	long := strings.Repeat("a", 250) + "@b.co"
	if len(long) <= 254 || looksLikeEmail(long) {
		t.Fatalf("超长邮箱应为假 len=%d", len(long))
	}
}

func TestNumericCode(t *testing.T) {
	code, err := newNumericCode(6)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("长度 %d", len(code))
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			t.Fatalf("非数字: %q", code)
		}
	}
	if !validNumericCode(code, 6) {
		t.Fatal("validNumericCode 应对生成的码为真")
	}
	for _, s := range []string{"12345", "1234567", "12a456", "１２３４５６"} {
		if validNumericCode(s, 6) {
			t.Fatalf("%q 应为假", s)
		}
	}
}

func TestTokenAndHash(t *testing.T) {
	a, err := newToken(32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || a == b {
		t.Fatal("令牌应非空且随机")
	}
	h1 := hashToken(a)
	h2 := hashToken(a)
	if len(h1) != 32 || len(h2) != 32 {
		t.Fatalf("哈希长度 %d %d", len(h1), len(h2))
	}
	for i := range h1 {
		if h1[i] != h2[i] {
			t.Fatal("hashToken 应确定性")
		}
	}
}

func TestCleanText(t *testing.T) {
	s, ok := cleanText("  hi  ")
	if !ok || s != "hi" {
		t.Fatalf("trim: ok=%v s=%q", ok, s)
	}
	_, ok = cleanText("a\x00b")
	if ok {
		t.Fatal("NUL 应失败")
	}
	_, ok = cleanText("\xff")
	if ok {
		t.Fatal("非法 UTF-8 应失败")
	}
}

func TestNewInviteCode(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := newInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 8 {
			t.Fatalf("长度 %d", len(code))
		}
		for _, c := range code {
			if !strings.ContainsRune(inviteCodeAlphabet, c) {
				t.Fatalf("非法字符 %q in %q", c, code)
			}
			for _, bad := range "0O1IL" {
				if c == bad {
					t.Fatalf("含禁用字符 %c in %q", bad, code)
				}
			}
		}
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "al***@example.com",
		"ab@x.io":           "a***@x.io",
		"@x.io":             "***",
		"a@x.io":            "***",
		"noat":              "***",
	}
	for in, want := range cases {
		got := maskEmail(in)
		if got != want {
			t.Fatalf("maskEmail(%q)=%q want %q", in, got, want)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("乱码: %q", got)
		}
	}
	cn := maskEmail("中文名@example.com")
	if !utf8.ValidString(cn) {
		t.Fatalf("中文本地部分: %q", cn)
	}
}

func TestGroupSlug(t *testing.T) {
	if got := groupSlug("VIP Users"); got != "vip-users" {
		t.Fatalf("VIP: %q", got)
	}
	if got := groupSlug("--a__b--"); got != "a--b" {
		t.Fatalf("--a__b--: %q", got)
	}
	cn := groupSlug("用户组")
	if !strings.HasPrefix(cn, "group-") || !groupCodeRe.MatchString(cn) {
		t.Fatalf("中文名 slug: %q", cn)
	}
	long := groupSlug(strings.Repeat("a", 80))
	if len(long) > 64 || strings.HasSuffix(long, "-") {
		t.Fatalf("超长: %q len=%d", long, len(long))
	}
}

func TestValidateGroup(t *testing.T) {
	code, name, _, err := validateGroup("", "My Group", "")
	if err != nil || code == "" || name != "My Group" {
		t.Fatalf("空 code 由 name 生成: code=%q name=%q err=%v", code, name, err)
	}

	_, _, _, err = validateGroup("Bad Code", "ok", "")
	e := errs.As(err)
	if e.Code != errs.ValidationFailed || e.Fields["code"] == "" {
		t.Fatalf("Bad Code: %v fields=%v", e.Code, e.Fields)
	}

	_, _, _, err = validateGroup("ok", "", "")
	e = errs.As(err)
	if e.Fields["name"] == "" {
		t.Fatal("空 name")
	}

	desc := strings.Repeat("x", 501)
	_, _, _, err = validateGroup("ok", "name", desc)
	e = errs.As(err)
	if e.Fields["description"] == "" {
		t.Fatal("描述过长")
	}
}

func TestTruncateToPtr(t *testing.T) {
	if truncateToPtr("", 512) != nil {
		t.Fatal("空串应 nil")
	}
	p := truncateToPtr("a\x00b", 512)
	if p == nil || strings.Contains(*p, "\x00") {
		t.Fatalf("NUL 应去掉: %v", p)
	}
	big := strings.Repeat("中", 200)
	p = truncateToPtr(big, 512)
	if p == nil {
		t.Fatal("应非 nil")
	}
	if len(*p) > 512 || !utf8.ValidString(*p) {
		t.Fatalf("截断: len=%d valid=%v", len(*p), utf8.ValidString(*p))
	}
	p = truncateToPtr("\xffabc", 512)
	if p == nil || strings.Contains(*p, "\xff") {
		t.Fatalf("非法 UTF-8: %q", p)
	}
}

func TestValidEmail(t *testing.T) {
	good := []string{"a@b.co", "first.last+tag@example.test"}
	for _, s := range good {
		if !ValidEmail(s) {
			t.Fatalf("%q 应合格", s)
		}
	}
	bad := []string{"", "a..b@x.com", "a,b@x.com", "a@b@x.com", "Name <a@b.co>", "a@b.co\r\n", strings.Repeat("a", 250) + "@b.co"}
	for _, s := range bad {
		if ValidEmail(s) {
			t.Fatalf("%q 应不合格", s)
		}
	}
}
