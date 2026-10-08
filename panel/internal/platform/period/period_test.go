package period

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
}
