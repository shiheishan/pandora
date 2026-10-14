package service

import (
	"testing"
	"time"
)

func TestOverlongPeriod(t *testing.T) {
	cases := []struct {
		name   string
		period string
		start  time.Time
		end    time.Time
		want   bool
	}{
		{
			name:   "day 末 +24h 不算过长",
			period: PeriodDay,
			start:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			want:   false,
		},
		{
			name:   "day 末 +26h 过长",
			period: PeriodDay,
			start:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 6, 2, 2, 0, 0, 0, time.UTC),
			want:   true,
		},
		{
			name:   "month 1/10 到 2/10 不算过长",
			period: PeriodMonth,
			start:  time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC),
			want:   false,
		},
		{
			name:   "month 1/10 到 3/10 过长",
			period: PeriodMonth,
			start:  time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
			want:   true,
		},
		{
			name:   "cycle 恒为 false",
			period: PeriodCycle,
			start:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			want:   false,
		},
	}
	for _, tc := range cases {
		if got := overlongPeriod(tc.period, tc.start, tc.end); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestNaturalRollStart(t *testing.T) {
	cases := []struct {
		name   string
		period string
		start  time.Time
		end    time.Time
		now    time.Time
		want   time.Time
		ok     bool
	}{
		{
			name:   "正常 day",
			period: PeriodDay,
			start:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC),
			want:   time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "落后多期 day",
			period: PeriodDay,
			start:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 6, 5, 8, 0, 0, 0, time.UTC),
			want:   time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "正常 month",
			period: PeriodMonth,
			start:  time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
			want:   time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "落后 month 从锚点加 k 不漂移",
			period: PeriodMonth,
			start:  time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			want:   time.Date(2026, 5, 3, 8, 0, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "过长 month",
			period: PeriodMonth,
			start:  time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			want:   time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "过长 day",
			period: PeriodDay,
			start:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 6, 3, 5, 0, 0, 0, time.UTC),
			want:   time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
			ok:     true,
		},
		{
			name:   "过长但还没满一期 month",
			period: PeriodMonth,
			start:  time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
			want:   time.Time{},
			ok:     false,
		},
		{
			name:   "锚点在 now 之后",
			period: PeriodDay,
			start:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			end:    time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
			want:   time.Time{},
			ok:     false,
		},
	}
	for _, tc := range cases {
		got, ok := naturalRollStart(tc.period, tc.start, tc.end, tc.now)
		if ok != tc.ok {
			t.Fatalf("%s: ok got %v want %v (start=%s)", tc.name, ok, tc.ok, got)
		}
		if tc.ok && !got.Equal(tc.want) {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestCycleRollWindow(t *testing.T) {
	cases := []struct {
		name      string
		oldEnd    time.Time
		subEnd    time.Time
		now       time.Time
		interval  string
		count     int
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "月付正常滚一期",
			oldEnd:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			subEnd:    time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC),
			interval:  "month",
			count:     1,
			wantStart: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "零头并进最后一期",
			oldEnd:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			subEnd:    time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			interval:  "month",
			count:     1,
			wantStart: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "任务停过追上",
			oldEnd:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			subEnd:    time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
			interval:  "month",
			count:     1,
			wantStart: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "不越过订阅到期",
			oldEnd:    time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			subEnd:    time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
			now:       time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			interval:  "month",
			count:     1,
			wantStart: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		start, end := cycleRollWindow(tc.oldEnd, tc.subEnd, tc.now, tc.interval, tc.count)
		if !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) {
			t.Fatalf("%s: got (%s, %s) want (%s, %s)", tc.name, start, end, tc.wantStart, tc.wantEnd)
		}
	}
}
