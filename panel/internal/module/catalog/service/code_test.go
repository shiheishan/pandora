package service

// code_test.go 覆盖套餐代码自动生成的词表映射、截断、查重与空位挑选规则。

import (
	"strings"
	"testing"
)

func TestAutoCodeBase(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"标准套餐", "标准套餐", "standard"},
		{"专业版 年付", "专业版 年付", "pro-yearly"},
		{"Pro Max", "Pro Max", "pro-max"},
		{"家庭 · 季", "家庭 · 季", "family-quarterly"},
		{"高级", "高级", "premium"},
		{"全是不在词表的汉字", "套餐畅享", "plan"},
		{"单个字母 A", "A", "plan-a"},
		{"单个数字 1", "1", "plan-1"},
		{"超长纯英文截到 32", strings.Repeat("a", 40), strings.Repeat("a", 32)},
		{
			"截断刚好落在连字符时去掉末尾连字符",
			strings.Repeat("a", 31) + " b c",
			strings.Repeat("a", 31),
		},
		{"首尾符号去掉", "!!hello world!!", "hello-world"},
		{"纯符号回退到 plan", "--- !!! ---", "plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoCodeBase(tc.in); got != tc.want {
				t.Fatalf("autoCodeBase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPickCode(t *testing.T) {
	t.Run("没被占用直接用 base", func(t *testing.T) {
		if got := pickCode("standard", []string{"other"}); got != "standard" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("base 与 base-2 被占用递增到 base-3", func(t *testing.T) {
		got := pickCode("standard", []string{"standard", "standard-2"})
		if got != "standard-3" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("只有 base-2 被占用仍可用 base", func(t *testing.T) {
		got := pickCode("standard", []string{"standard-2"})
		if got != "standard" {
			t.Fatalf("got %q", got)
		}
	})
}
