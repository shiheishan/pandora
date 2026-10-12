package service

import (
	"testing"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNormalizeLabel(t *testing.T) {
	ok := map[string]string{
		"  妈妈的 iPad ": "妈妈的 iPad",
		"":            "",
		"   ":         "",
		"一二三四五六七八九十一二三四五六": "一二三四五六七八九十一二三四五六",
		"👨‍👩‍👧":  "👨‍👩‍👧",
		"Work 2": "Work 2",
	}
	for in, want := range ok {
		got, err := NormalizeLabel(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeLabel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"一二三四五六七八九十一二三四五六七",
		"a\nb",
		"a\tb",
		"a\x00b",
		"a‮b", // 从右到左覆盖：能伪造文件名
		"a b",
		"a\xffb",
	}
	for _, in := range bad {
		_, err := NormalizeLabel(in)
		if err == nil {
			t.Fatalf("NormalizeLabel(%q) 应失败", in)
		}
		e := errs.As(err)
		if e.Code != errs.ValidationFailed || e.Fields["label"] == "" {
			t.Fatalf("NormalizeLabel(%q) err=%v", in, err)
		}
	}
}

func TestLabelWithSuffix(t *testing.T) {
	if got := LabelWithSuffix("Work", 1); got != "Work" {
		t.Fatalf("n=1: got %q", got)
	}
	if got := LabelWithSuffix("Work", 2); got != "Work 2" {
		t.Fatalf("短名: got %q", got)
	}
	long := "一二三四五六七八九十一二三四五六"
	got := LabelWithSuffix(long, 12)
	if got != "一二三四五六七八九十一二三 12" || utf8.RuneCountInString(got) != MaxLabelRunes {
		t.Fatalf("截断: got %q", got)
	}
}

func TestProfileName(t *testing.T) {
	cases := []struct {
		site, label, plan, want string
	}{
		{"喵站", "妈妈的 iPad", "高级版", "喵站 · 妈妈的 iPad"},
		{"喵站", "", "高级版", "喵站 · 高级版"},
		{"喵站", " ", "", "喵站"},
		{" ", "", "高级版", "Pandora · 高级版"},
	}
	for _, tc := range cases {
		if got := ProfileName(tc.site, tc.label, tc.plan); got != tc.want {
			t.Fatalf("ProfileName(%q,%q,%q) = %q want %q", tc.site, tc.label, tc.plan, got, tc.want)
		}
	}
}
