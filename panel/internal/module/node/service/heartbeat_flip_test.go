package service

import (
	"testing"
	"time"
)

func TestBeatFlipsOnline(t *testing.T) {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	prev30 := base.Add(-30 * time.Second)
	prev89 := base.Add(-89 * time.Second)
	prev90 := base.Add(-NodeStaleAfter)

	cases := []struct {
		name string
		prev *time.Time
		at   time.Time
		want bool
	}{
		{"从没心跳", nil, base, true},
		{"差 30 秒", &prev30, base, false},
		{"差 89 秒", &prev89, base, false},
		{"差正好 NodeStaleAfter", &prev90, base, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := beatFlipsOnline(c.prev, c.at); got != c.want {
				t.Fatalf("beatFlipsOnline(%v, %v) = %v，期望 %v", c.prev, c.at, got, c.want)
			}
		})
	}
}
