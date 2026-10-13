package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

func TestVerdictOf(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	graceFuture := now.Add(time.Hour)
	gracePast := now.Add(-time.Hour)

	cases := []struct {
		name       string
		credStatus string
		grace      *time.Time
		st         State
		want       pullVerdict
	}{
		{"active+live", "active", nil, StateLive, pullLive},
		{"active+revivable", "active", nil, StateRevivable, pullExpired},
		{"active+ended", "active", nil, StateEnded, pullDenied},
		{"revoked无宽限", "revoked", nil, StateLive, pullDenied},
		{"revoked宽限内+live", "revoked", &graceFuture, StateLive, pullLive},
		{"revoked宽限外", "revoked", &gracePast, StateLive, pullDenied},
		{"revoked宽限内+revivable", "revoked", &graceFuture, StateRevivable, pullExpired},
	}
	for _, tc := range cases {
		got := verdictOf(tc.credStatus, tc.grace, tc.st, now)
		if got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestFailureSampler(t *testing.T) {
	clock := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return clock }
	f := newFailureSampler(time.Minute, 2)
	f.now = nowFn

	srcA := "source-a"
	srcB := "source-b"
	srcC := "source-c"

	admit, dropped := f.admit(srcA)
	if !admit || dropped != 0 {
		t.Fatalf("第一次 srcA: admit=%v dropped=%d", admit, dropped)
	}
	admit, dropped = f.admit(srcA)
	if admit || dropped != 0 {
		t.Fatalf("同一来源第二次应不落库: admit=%v dropped=%d", admit, dropped)
	}
	admit, dropped = f.admit(srcB)
	if !admit || dropped != 0 {
		t.Fatalf("第二个来源: admit=%v dropped=%d", admit, dropped)
	}
	admit, dropped = f.admit(srcC)
	if admit || dropped != 0 {
		t.Fatalf("第三个不同来源超额应不落库: admit=%v dropped=%d", admit, dropped)
	}

	clock = clock.Add(time.Minute)
	srcD := "source-d"
	admit, dropped = f.admit(srcD)
	if dropped != 2 {
		t.Fatalf("窗口切换 dropped 应为 2，got %d", dropped)
	}
	if !admit {
		t.Fatalf("新窗口第一次应落库")
	}
}

func TestUserinfoOf(t *testing.T) {
	periodEnd := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	limit := int64(100)
	consumed := int64(30)

	row := repo.PullRow{
		PeriodEnd:       periodEnd,
		TrafficLimit:    &limit,
		TrafficConsumed: &consumed,
		PackRemaining:   50,
	}
	u := userinfoOf(row)
	if u.Total != 150 || u.Download != 30 || u.Upload != 0 || u.Expire != periodEnd.Unix() {
		t.Fatalf("套餐+包: %+v", u)
	}

	row2 := repo.PullRow{
		PeriodEnd:     periodEnd,
		PackRemaining: 7,
	}
	u2 := userinfoOf(row2)
	if u2.Total != 7 || u2.Download != 0 || u2.Expire != periodEnd.Unix() {
		t.Fatalf("无配额行仅包: %+v", u2)
	}
}
