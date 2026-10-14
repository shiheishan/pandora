package service

import (
	"testing"
	"time"
)

func TestDayOf(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 12, 17, 30, 0, 0, time.UTC)
	wantSH := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	wantUTC := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if got := dayOf(t0, loc); !got.Equal(wantSH) {
		t.Fatalf("Asia/Shanghai: got %v want %v", got, wantSH)
	}
	if got := dayOf(t0, time.UTC); !got.Equal(wantUTC) {
		t.Fatalf("UTC: got %v want %v", got, wantUTC)
	}
}

func TestUsageWindow(t *testing.T) {
	today := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		start    time.Time
		days     int
		wantFrom time.Time
	}{
		{
			name:     "period start 10 days ago days=0 → 11 days",
			start:    today.AddDate(0, 0, -10),
			days:     0,
			wantFrom: today.AddDate(0, 0, -10),
		},
		{
			name:     "period start 200 days ago days=0 → cap 93",
			start:    today.AddDate(0, 0, -200),
			days:     0,
			wantFrom: today.AddDate(0, 0, -(MaxUsageDays - 1)),
		},
		{
			name:     "days=7",
			start:    today.AddDate(0, 0, -30),
			days:     7,
			wantFrom: today.AddDate(0, 0, -6),
		},
		{
			name:     "start after today → only today",
			start:    today.AddDate(0, 0, 1),
			days:     0,
			wantFrom: today,
		},
	}
	for _, tc := range cases {
		got := usageWindow(today, tc.start, tc.days)
		if !got.Equal(tc.wantFrom) {
			t.Fatalf("%s: from %v want %v", tc.name, got, tc.wantFrom)
		}
	}
}

func TestFillUsageDays(t *testing.T) {
	from := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	today := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	byDay := map[string]int64{
		"2026-10-11": 100,
		"2026-10-12": 50,
	}
	points, todayBytes, avg := fillUsageDays(from, today, byDay)
	if len(points) != 3 {
		t.Fatalf("len(points)=%d want 3", len(points))
	}
	want := []UsageDay{
		{Date: "2026-10-10", Bytes: 0},
		{Date: "2026-10-11", Bytes: 100},
		{Date: "2026-10-12", Bytes: 50},
	}
	for i := range want {
		if points[i].Date != want[i].Date || points[i].Bytes != want[i].Bytes {
			t.Fatalf("points[%d]=%+v want %+v", i, points[i], want[i])
		}
	}
	if todayBytes != 50 {
		t.Fatalf("todayBytes=%d want 50", todayBytes)
	}
	if avg != 50 {
		t.Fatalf("avg=%d want 50", avg)
	}
}
