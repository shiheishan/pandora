package billing

import (
	"testing"
	"time"
)

// 换掉一份时不保留的赠送天数（用户 10-08）：付费天数先用、赠送天数最后用。
func TestGiftedDaysLeft(t *testing.T) {
	day := 24 * time.Hour
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	month := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Sub(start) // 31 天
	cases := []struct {
		name string
		b    prorationBasis
		now  time.Time
		want int
	}{
		{"只有付费的一期：没有赠送", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month)}, start.Add(3 * day), 0},
		{"付费一期 + 加时长卡 7 天：付费期内看，赠送的整 7 天", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day)}, start.Add(3 * day), 7},
		{"付费一期 + 7 天 + 套餐卡续一个月", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day + 30*day)}, start.Add(1 * time.Hour), 37},
		{"付费期已用完、正在用赠送的：只算剩下的整天", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day)}, start.Add(month + 2*day + time.Hour), 4},
		{"没有付费单（套餐卡开通的）：剩下的都是赠送的", prorationBasis{PeriodStart: start, PeriodEnd: start.Add(30 * day)}, start.Add(10*day + time.Minute), 19},
		{"不满一天不写", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 20*time.Hour)}, start, 0},
		{"已到期", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month)}, start.Add(month + day), 0},
	}
	for _, c := range cases {
		if got := giftedDaysLeft(c.b, c.now); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}
