package service

import (
	"testing"
	"time"
)

func TestAddInterval(t *testing.T) {
	jan31 := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	leap := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		from     time.Time
		interval string
		count    int
		want     time.Time
	}{
		{"月末 + 1 月按 AddDate 溢出到 3 月", jan31, "month", 1, time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC)},
		{"闰日 + 1 年", leap, "year", 1, time.Date(2029, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"天", jan31, "day", 3, time.Date(2026, 2, 3, 8, 0, 0, 0, time.UTC)},
		{"周", jan31, "week", 2, time.Date(2026, 2, 14, 8, 0, 0, 0, time.UTC)},
		{"季", jan31, "quarter", 1, time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)},
		{"一次性给远期哨兵", jan31, "one_time", 1, time.Date(2126, 1, 31, 8, 0, 0, 0, time.UTC)},
		{"count 非正按 1", jan31, "day", 0, time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)},
		{"未知周期按月", jan31, "fortnight", 2, time.Date(2026, 3, 31, 8, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := AddInterval(tc.from, tc.interval, tc.count); !got.Equal(tc.want) {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
	if !ValidInterval("quarter") || ValidInterval("fortnight") {
		t.Fatal("ValidInterval 口径不对")
	}
	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if got := CycleLength(from, "month", 1); got != 28*24*time.Hour {
		t.Fatalf("CycleLength: got %s", got)
	}
}

func TestQuotaPeriodEnd(t *testing.T) {
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	cycleEnd := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	if got := QuotaPeriodEnd(PeriodTotal, start, cycleEnd); got != nil {
		t.Fatalf("total: got %v", got)
	}
	cases := []struct {
		period string
		want   time.Time
	}{
		{PeriodDay, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		{PeriodMonth, time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
		{PeriodCycle, cycleEnd},
	}
	for _, tc := range cases {
		got := QuotaPeriodEnd(tc.period, start, cycleEnd)
		if got == nil || !got.Equal(tc.want) {
			t.Fatalf("%s: got %v want %s", tc.period, got, tc.want)
		}
	}
}

func TestRenewalBase(t *testing.T) {
	oldEnd := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	before := oldEnd.Add(-time.Hour)
	after := oldEnd.Add(time.Hour)
	cases := []struct {
		name        string
		paidAt      time.Time
		created     *time.Time
		wantBase    time.Time
		wantRestart bool
	}{
		{"提前续费", before, &before, oldEnd, false},
		{"到期前下单、回调晚到", after, &before, oldEnd, false},
		{"过期后下单、过期恢复", after, &after, after, true},
		{"无下单时间且已过期", after, nil, after, true},
	}
	for _, tc := range cases {
		base, restart := RenewalBase(oldEnd, tc.paidAt, tc.created)
		if !base.Equal(tc.wantBase) || restart != tc.wantRestart {
			t.Fatalf("%s: got %s %v want %s %v", tc.name, base, restart, tc.wantBase, tc.wantRestart)
		}
	}
}

func TestExtendBase(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 0, 3)
	if base, lapsed := ExtendBase(future, now); !base.Equal(future) || lapsed {
		t.Fatalf("未过期: got %s %v", base, lapsed)
	}
	if base, lapsed := ExtendBase(now, now); !base.Equal(now) || !lapsed {
		t.Fatalf("已过期: got %s %v", base, lapsed)
	}
}

func TestProratedQuota(t *testing.T) {
	const gib = int64(1) << 30
	month := 30 * 24 * time.Hour
	// 100 GiB × 7 天 ÷ 30 天 = 751619276800 ÷ 30 = 25053975893.33…，向下取整
	if got := ProratedQuota(100*gib, 7, month); got != 25053975893 {
		t.Fatalf("100GiB×7/30: got %d", got)
	}
	for _, tc := range []struct {
		limit int64
		days  int
		cycle time.Duration
	}{{0, 7, month}, {100 * gib, 0, month}, {100 * gib, 7, 0}, {-1, 7, month}} {
		if got := ProratedQuota(tc.limit, tc.days, tc.cycle); got != 0 {
			t.Fatalf("非正参数 %+v: got %d", tc, got)
		}
	}
	// 结果超出 int64 返回 0
	if got := ProratedQuota(1<<62, 3650, time.Nanosecond); got != 0 {
		t.Fatalf("溢出: got %d", got)
	}
}

func TestNextCycleEnd(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 剩余够两期：下一期正常结束
	subEnd := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := NextCycleEnd(start, subEnd, "month", 1); !got.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("够两期: got %s", got)
	}
	// 不够：零头并进这一期
	subEnd = time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	if got := NextCycleEnd(start, subEnd, "month", 1); !got.Equal(subEnd) {
		t.Fatalf("不够两期: got %s", got)
	}
}

func TestRenewUntil(t *testing.T) {
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if got := RenewUntil(end, now, "month", 1); !got.Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("未到期接到期: got %s", got)
	}
	if got := RenewUntil(now.AddDate(0, 0, -5), now, "month", 1); !got.Equal(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("已到期从 now 起: got %s", got)
	}
}
