package service

import "testing"

func TestCsvSafe(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"=1", "'=1"},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@x", "'@x"},
		{"\tx", "'\tx"},
		{"\rx", "'\rx"},
		{"abc", "abc"},
	}
	for _, c := range cases {
		if got := csvSafe(c.in); got != c.want {
			t.Errorf("csvSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCsvHeader(t *testing.T) {
	want := []string{
		"occurred_at", "actor_kind", "actor_email", "action", "resource_type", "resource_id",
		"resource_label", "api_domain", "outcome", "reason", "source_ip", "auth_context",
	}
	if len(csvHeader) != 12 {
		t.Fatalf("csvHeader 列数 %d, want 12", len(csvHeader))
	}
	for i := range want {
		if csvHeader[i] != want[i] {
			t.Fatalf("csvHeader[%d] = %q, want %q", i, csvHeader[i], want[i])
		}
	}
}
