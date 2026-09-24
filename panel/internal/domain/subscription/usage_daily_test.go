// [INPUT]: 依赖 usage_daily.go 的 usageWindow / fillUsageDays
// [OUTPUT]: 对外提供 TestUsageWindow、TestFillUsageDays
// [POS]: domain/subscription 按日用量读模型的单元测试：窗口边界与补零、今天、日均；真实 SQL 见 usage_daily_pg18_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestUsageWindow(t *testing.T) {
	today := day("2026-09-24")
	for _, tc := range []struct {
		name     string
		start    string
		days     int
		wantFrom string
	}{
		{"default covers the current period", "2026-09-20", 0, "2026-09-20"},
		{"period starting today is one day", "2026-09-24", 0, "2026-09-24"},
		{"a never-resetting period is capped", "2024-01-01", 0, "2026-06-24"},
		{"a start after today still yields today", "2026-09-30", 0, "2026-09-24"},
		{"explicit days ignore the period", "2026-09-20", 7, "2026-09-18"},
		{"explicit days are capped too", "2026-09-20", 500, "2026-06-24"},
	} {
		from := usageWindow(today, day(tc.start), tc.days)
		if got := from.Format(time.DateOnly); got != tc.wantFrom {
			t.Errorf("%s: from = %s, want %s", tc.name, got, tc.wantFrom)
		}
	}
	// 截断后恰好 MaxUsageDays 天（含今天）。
	from := usageWindow(today, day("2024-01-01"), 0)
	if n := int(today.Sub(from).Hours()/24) + 1; n != MaxUsageDays {
		t.Fatalf("capped window has %d days, want %d", n, MaxUsageDays)
	}
}

func TestFillUsageDays(t *testing.T) {
	// 跨月补零：没有行的日子是 0，窗口外的行不计入。
	points, today, avg := fillUsageDays(day("2026-09-29"), day("2026-10-02"), map[string]int64{
		"2026-09-28": 1 << 40, // 窗口外
		"2026-09-30": 300,
		"2026-10-02": 101,
	})
	want := []UsageDayPoint{
		{"2026-09-29", 0}, {"2026-09-30", 300}, {"2026-10-01", 0}, {"2026-10-02", 101},
	}
	if len(points) != len(want) {
		t.Fatalf("points = %v, want %v", points, want)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Fatalf("points[%d] = %v, want %v", i, points[i], want[i])
		}
	}
	if today != 101 {
		t.Fatalf("today = %d, want 101", today)
	}
	// 日均按窗口天数（含今天）整除：401 / 4 = 100。
	if avg != 100 {
		t.Fatalf("avg = %d, want 100", avg)
	}
}
