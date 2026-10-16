package service

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestProrationCredit(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }

	subMonth := func(mutate func(*prorationBasis)) prorationBasis {
		b := prorationBasis{
			Value: decimal.RequireFromString("30.00"), PaidSpan: 30 * day,
			PeriodStart: start, PeriodEnd: start.Add(30 * day),
		}
		if mutate != nil {
			mutate(&b)
		}
		return b
	}

	cases := []struct {
		name string
		b    prorationBasis
		now  time.Time
		want string
	}{
		{
			name: "订阅过半时间折一半",
			b:    subMonth(nil),
			now:  at(15 * day),
			want: "15.00",
		},
		{
			name: "订阅流量用完仍按时间",
			b: subMonth(func(b *prorationBasis) {
				b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 100}}
			}),
			now:  at(15 * day),
			want: "15.00",
		},
		{
			name: "赠送天数最后用",
			b: subMonth(func(b *prorationBasis) {
				b.PeriodEnd = start.Add(60 * day)
			}),
			now:  at(10 * day),
			want: "20.00",
		},
		{
			name: "一次性永久按流量",
			b: prorationBasis{
				OneTime: true, Permanent: true,
				Value:       decimal.RequireFromString("100.00"),
				PeriodStart: start, PeriodEnd: start.AddDate(100, 0, 0),
				PaidSpan: start.AddDate(100, 0, 0).Sub(start),
				Traffic:  []trafficAllowance{{Cap: 1000, Consumed: 700}},
			},
			now:  start,
			want: "30.00",
		},
		{
			name: "一次性有天数取较小比例",
			b: prorationBasis{
				OneTime:     true,
				Value:       decimal.RequireFromString("100.00"),
				PeriodStart: start, PeriodEnd: start.Add(100 * day),
				PaidSpan: 100 * day,
				Traffic:  []trafficAllowance{{Cap: 100, Consumed: 20}},
			},
			now:  at(75 * day),
			want: "25.00",
		},
		{
			name: "一次性不限流量只看天数",
			b: prorationBasis{
				OneTime:     true,
				Value:       decimal.RequireFromString("40.00"),
				PeriodStart: start, PeriodEnd: start.Add(100 * day),
				PaidSpan: 100 * day,
			},
			now:  at(50 * day),
			want: "20.00",
		},
		{
			name: "一次性已用尽",
			b: prorationBasis{
				OneTime:     true,
				Value:       decimal.RequireFromString("50.00"),
				PeriodStart: start, PeriodEnd: start.Add(30 * day),
				PaidSpan: 30 * day,
				Traffic:  []trafficAllowance{{Cap: 200, Consumed: 200}},
			},
			now:  at(10 * day),
			want: "0.00",
		},
		{
			name: "本期没有付费",
			b: prorationBasis{
				PeriodStart: start, PeriodEnd: start.Add(30 * day),
				PaidSpan: 30 * day,
			},
			now:  start,
			want: "0.00",
		},
		{
			name: "订阅向下取整到分",
			b: subMonth(func(b *prorationBasis) {
				b.Value = decimal.RequireFromString("10.00")
			}),
			now:  at(20 * day),
			want: "3.33",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := prorationCredit(tc.b, tc.now)
			want := decimal.RequireFromString(tc.want)
			if !got.Equal(want) {
				t.Fatalf("credit=%s want %s", got, want)
			}
		})
	}
}

func TestGiftedDaysLeft(t *testing.T) {
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	month := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Sub(start)
	cases := []struct {
		name string
		b    prorationBasis
		now  time.Time
		want int
	}{
		{"一次性套餐恒为 0", prorationBasis{OneTime: true, PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day)}, start, 0},
		{"只有付费的一期：没有赠送", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month)}, start.Add(3 * day), 0},
		{"付费一期 + 加时长卡 7 天", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day)}, start.Add(3 * day), 7},
	}
	for _, c := range cases {
		if got := giftedDaysLeft(c.b, c.now); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestPlanChangeRefund(t *testing.T) {
	cases := []struct{ credit, subtotal, discount, want string }{
		{"0.00", "10.00", "0.00", "0.00"},
		{"15.00", "10.00", "0.00", "5.00"},
	}
	for _, c := range cases {
		got := planChangeRefund(decimal.RequireFromString(c.credit), decimal.RequireFromString(c.subtotal), decimal.RequireFromString(c.discount))
		if !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("refund=%s want %s", got, c.want)
		}
	}
}
