package service

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestParseRateSchedule(t *testing.T) {
	d1 := decimal.RequireFromString("1.5")
	d2 := decimal.RequireFromString("2")

	cases := []struct {
		name    string
		in      []RateRangeInput
		want    []RateRange
		wantErr string
	}{
		{
			name: "空输入",
			in:   nil,
			want: nil,
		},
		{
			name: "合法两段乱序已排序",
			in: []RateRangeInput{
				{Start: "13:00", End: "18:00", Rate: d2},
				{Start: "08:00", End: "12:00", Rate: d1},
			},
			want: []RateRange{
				{StartMin: 8 * 60, EndMin: 12 * 60, Rate: d1},
				{StartMin: 13 * 60, EndMin: 18 * 60, Rate: d2},
			},
		},
		{
			name: "24:00 结束合法",
			in: []RateRangeInput{
				{Start: "22:00", End: "24:00", Rate: d1},
			},
			want: []RateRange{
				{StartMin: 22 * 60, EndMin: 1440, Rate: d1},
			},
		},
		{
			name: "8:00 非法",
			in: []RateRangeInput{
				{Start: "8:00", End: "12:00", Rate: d1},
			},
			wantErr: "时间要写成 HH:MM",
		},
		{
			name: "24:00 作开始非法",
			in: []RateRangeInput{
				{Start: "24:00", End: "24:00", Rate: d1},
			},
			wantErr: "时间要写成 HH:MM",
		},
		{
			name: "12:60 非法",
			in: []RateRangeInput{
				{Start: "12:60", End: "13:00", Rate: d1},
			},
			wantErr: "时间要写成 HH:MM",
		},
		{
			name: "start 等于 end",
			in: []RateRangeInput{
				{Start: "08:00", End: "08:00", Rate: d1},
			},
			wantErr: "开始要早于结束",
		},
		{
			name: "重叠",
			in: []RateRangeInput{
				{Start: "08:00", End: "12:00", Rate: d1},
				{Start: "11:00", End: "13:00", Rate: d2},
			},
			wantErr: "时间重叠",
		},
		{
			name:    "13 段",
			in:      makeManySegments(13, d1),
			wantErr: "最多 12 段",
		},
		{
			name: "倍率 -1",
			in: []RateRangeInput{
				{Start: "08:00", End: "12:00", Rate: decimal.RequireFromString("-1")},
			},
			wantErr: "倍率要在 0 到 9999.99",
		},
		{
			name: "倍率三位小数",
			in: []RateRangeInput{
				{Start: "08:00", End: "12:00", Rate: decimal.RequireFromString("1.234")},
			},
			wantErr: "倍率要在 0 到 9999.99",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRateSchedule(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("期望错误含 %q，得到 nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误 %q 应含 %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRateSchedule: %v", err)
			}
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("段数 %d，期望 %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].StartMin != tc.want[i].StartMin || got[i].EndMin != tc.want[i].EndMin {
					t.Fatalf("段 %d 时间 %+v 期望 %+v", i, got[i], tc.want[i])
				}
				if !got[i].Rate.Equal(tc.want[i].Rate) {
					t.Fatalf("段 %d 倍率 %s 期望 %s", i, got[i].Rate, tc.want[i].Rate)
				}
			}
		})
	}
}

func TestEncodeDecodeRateSchedule(t *testing.T) {
	rs := []RateRange{
		{StartMin: 8 * 60, EndMin: 12 * 60, Rate: decimal.RequireFromString("1.5")},
	}
	text := encodeRateSchedule(rs)
	back, err := decodeRateSchedule(text)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(back) != 1 || back[0].StartMin != rs[0].StartMin || !back[0].Rate.Equal(rs[0].Rate) {
		t.Fatalf("往返不等: %+v", back)
	}

	nilBack, err := decodeRateSchedule("[]")
	if err != nil || nilBack != nil {
		t.Fatalf("decode []: got %v err %v", nilBack, err)
	}
}

func TestEffectiveRate(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	base := decimal.RequireFromString("1")
	hit := decimal.RequireFromString("2")
	sched := []RateRange{{StartMin: 8 * 60, EndMin: 12 * 60, Rate: hit}}

	// UTC 00:30 = 上海 08:30，应命中 08:00–12:00
	at := time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC)
	if r := effectiveRate(base, sched, at, loc); !r.Equal(hit) {
		t.Fatalf("08:30 应命中，得 %s", r)
	}

	// 上海 12:00 整不命中（半开）
	atNoon := time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC) // 12:00 CST
	if r := effectiveRate(base, sched, atNoon, loc); !r.Equal(base) {
		t.Fatalf("12:00 不应命中，得 %s", r)
	}

	if r := effectiveRate(base, nil, at, loc); !r.Equal(base) {
		t.Fatalf("无段应返回 base，得 %s", r)
	}
}

func TestTrafficPeriodStart(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	assertStart := func(t *testing.T, resetDay int, now time.Time, wantY, wantM, wantD, wantH, wantMin int) {
		start := trafficPeriodStart(resetDay, now, loc).In(loc)
		if start.Year() != wantY || start.Month() != time.Month(wantM) || start.Day() != wantD ||
			start.Hour() != wantH || start.Minute() != wantMin {
			t.Fatalf("resetDay=%d now=%v → %v，期望 %04d-%02d-%02d %02d:%02d",
				resetDay, now.In(loc), start, wantY, wantM, wantD, wantH, wantMin)
		}
	}

	t.Run("当月20号重置日15", func(t *testing.T) {
		now := time.Date(2026, 5, 20, 10, 0, 0, 0, loc)
		assertStart(t, 15, now, 2026, 5, 15, 0, 0)
	})
	t.Run("当月10号取上月15", func(t *testing.T) {
		now := time.Date(2026, 5, 10, 10, 0, 0, 0, loc)
		assertStart(t, 15, now, 2026, 4, 15, 0, 0)
	})
	t.Run("reset31三月10日", func(t *testing.T) {
		now := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)
		assertStart(t, 31, now, 2026, 2, 28, 0, 0)
	})
	t.Run("正好15号零点", func(t *testing.T) {
		now := time.Date(2026, 5, 15, 0, 0, 0, 0, loc)
		assertStart(t, 15, now, 2026, 5, 15, 0, 0)
	})
}

func makeManySegments(n int, rate decimal.Decimal) []RateRangeInput {
	out := make([]RateRangeInput, n)
	for i := 0; i < n; i++ {
		h := i % 20
		out[i] = RateRangeInput{
			Start: formatHHMM(h * 60),
			End:   formatHHMM(h*60 + 30),
			Rate:  rate,
		}
	}
	return out
}
