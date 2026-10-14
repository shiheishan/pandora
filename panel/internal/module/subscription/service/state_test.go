package service

import (
	"testing"
	"time"
)

func TestStateAt(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	closed := now.Add(-time.Hour)
	cases := []struct {
		name   string
		status string
		end    time.Time
		closed *time.Time
		want   State
	}{
		{"active 未到期", StatusActive, now.Add(time.Hour), nil, StateLive},
		{"active 已过到期", StatusActive, now.Add(-time.Hour), nil, StateRevivable},
		{"expired 未关窗", StatusExpired, now.Add(-time.Hour), nil, StateRevivable},
		{"expired 关窗", StatusExpired, now.Add(-time.Hour), &closed, StateEnded},
		{"到期恰等于 now", StatusActive, now, nil, StateRevivable},
	}
	for _, tc := range cases {
		if got := StateAt(tc.status, tc.end, tc.closed, now); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
		s := Subscription{Status: tc.status, PeriodEnd: tc.end, RenewalClosedAt: tc.closed}
		if got := s.StateAt(now); got != tc.want {
			t.Fatalf("%s（方法）: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{StatusActive, StatusActive, true},
		{StatusActive, StatusExpired, true},
		{StatusExpired, StatusActive, true},
		{StatusExpired, StatusExpired, false},
		{"cancelled", StatusActive, false},
		{StatusActive, "cancelled", false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Fatalf("%s→%s: got %v want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestAcceptsChange(t *testing.T) {
	if !StateLive.AcceptsChange() || !StateRevivable.AcceptsChange() || StateEnded.AcceptsChange() {
		t.Fatal("AcceptsChange 口径不对")
	}
}

func TestCurrentIndex(t *testing.T) {
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	mk := func(st State, endDays, createdDays int) Summary {
		return Summary{
			Subscription: Subscription{PeriodEnd: base.AddDate(0, 0, endDays), CreatedAt: base.AddDate(0, 0, createdDays)},
			State:        st,
		}
	}
	if got := currentIndex(nil); got != -1 {
		t.Fatalf("空: got %d", got)
	}
	cases := []struct {
		name string
		subs []Summary
		want int
	}{
		{"生效优先", []Summary{mk(StateRevivable, 90, 5), mk(StateLive, 10, 1)}, 1},
		{"都不生效时到期晚的优先", []Summary{mk(StateRevivable, -10, 5), mk(StateEnded, -2, 1)}, 1},
		{"到期相同时创建晚的优先", []Summary{mk(StateLive, 30, 1), mk(StateLive, 30, 3), mk(StateLive, 30, 2)}, 1},
	}
	for _, tc := range cases {
		if got := currentIndex(tc.subs); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}
