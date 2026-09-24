package adminops

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestParseClusterKey(t *testing.T) {
	if b, err := ParseClusterKey(" 0aff "); err != nil || len(b) != 2 || b[1] != 0xff {
		t.Fatalf("valid key: %v %v", b, err)
	}
	for _, key := range []string{"", "zz", "abc"} {
		var httpErr *httpx.Error
		if _, err := ParseClusterKey(key); !errors.As(err, &httpErr) || httpErr.Code != httpx.CodeNotFound {
			t.Errorf("key %q: %v, want 404", key, err)
		}
	}
}

// 去重并排序：固定的加锁顺序让两个并发的批量停用不会互相死锁。
func TestNormalizeDisableInput(t *testing.T) {
	a, b := "0190a000-0000-7000-8000-00000000000b", "0190a000-0000-7000-8000-00000000000a"
	ids, reason, err := normalizeDisableInput([]string{a, " " + b, a}, "  同一机房批量注册  ")
	if err != nil || len(ids) != 2 || ids[0] != b || ids[1] != a || reason != "同一机房批量注册" {
		t.Fatalf("got ids=%v reason=%q err=%v", ids, reason, err)
	}
	many := make([]string, 201)
	for i := range many {
		many[i] = "0190a000-0000-7000-8000-" + strings.Repeat("0", 9) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
	}
	for _, tc := range []struct {
		ids    []string
		reason string
		field  string
	}{
		{[]string{a}, "短", "reason"},
		{[]string{a}, strings.Repeat("长", 501), "reason"},
		{nil, "同一机房批量注册", "user_ids"},
		{[]string{"nope"}, "同一机房批量注册", "user_ids"},
		{many, "同一机房批量注册", "user_ids"},
	} {
		_, _, err := normalizeDisableInput(tc.ids, tc.reason)
		var httpErr *httpx.Error
		if !errors.As(err, &httpErr) || httpErr.Fields[tc.field] == "" {
			t.Errorf("ids=%d reason=%q: %v, want 422 fields.%s", len(tc.ids), tc.reason, err, tc.field)
		}
	}
}
