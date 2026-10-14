package service

import (
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 折算规则的边界（期望逐分一致）：
// 时间比与流量比取小、向下取整、付费天数先用、赠送天数折不成钱、0 元单不折。
func TestProrationCredit(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	month := prorationBasis{Value: dec("30.00"), PaidSpan: 30 * day, PeriodStart: start, PeriodEnd: start.Add(30 * day)}
	at := func(d time.Duration) time.Time { return start.Add(d) }
	with := func(b prorationBasis, mutate func(*prorationBasis)) prorationBasis {
		mutate(&b)
		return b
	}

	cases := []struct {
		name string
		b    prorationBasis
		now  time.Time
		want decimal.Decimal
	}{
		{"整期没用值全部付款", month, at(0), dec("30.00")},
		{"没有流量配额按时间比", month, at(10 * day), dec("20.00")},
		{"向下取整到分", with(month, func(b *prorationBasis) { b.Value = dec("10.00") }), at(20 * day), dec("3.33")},
		{"流量比更小取流量比", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 75}}
		}), at(10 * day), dec("7.50")},
		{"时间比更小取时间比", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 10}}
		}), at(15 * day), dec("15.00")},
		{"多条流量配额取最小", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 10}, {Cap: 50, Consumed: 40}}
		}), at(0), dec("6.00")},
		{"流量用完不值钱", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 100}}
		}), at(day), decimal.Zero},
		{"流量超用不值钱", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 180}}
		}), at(day), decimal.Zero},
		{"上限不为正不值钱", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 0, Consumed: 0}}
		}), at(day), decimal.Zero},
		{"0 元单不值钱", with(month, func(b *prorationBasis) { b.Value = decimal.Zero }), at(0), decimal.Zero},
		{"没有付费时长不值钱", with(month, func(b *prorationBasis) { b.PaidSpan = 0 }), at(0), decimal.Zero},
		{"周期已过不值钱", month, at(31 * day), decimal.Zero},
		// 赠送 30 天接在付费 30 天后面：已用 10 天全算付费的，剩余付费 20/30
		{"赠送天数最后用", with(month, func(b *prorationBasis) { b.PeriodEnd = start.Add(60 * day) }), at(10 * day), dec("20.00")},
		// 已用 40 天：付费的 30 天用完了，剩下的全是赠送，折不成钱
		{"只剩赠送天数不值钱", with(month, func(b *prorationBasis) { b.PeriodEnd = start.Add(60 * day) }), at(40 * day), decimal.Zero},
		// 两张单叠出 60 天付费：已用 45 天，剩 15/60
		{"叠加的付费单共用一个基数", with(month, func(b *prorationBasis) {
			b.Value, b.PaidSpan, b.PeriodEnd = dec("60.00"), 60*day, start.Add(60*day)
		}), at(45 * day), dec("15.00")},
		// 周期被缩短时按周期末封顶，不会超过付费时长
		{"周期末封顶剩余付费时间", with(month, func(b *prorationBasis) { b.PeriodEnd = start.Add(20 * day) }), at(10 * day), dec("10.00")},
		{"大金额大配额不溢出", prorationBasis{
			Value: money.FromCents(math.MaxInt64 / 2), PaidSpan: 3650 * day, PeriodStart: start,
			PeriodEnd: start.Add(3650 * day),
			Traffic:   []trafficAllowance{{Cap: math.MaxInt64 / 2, Consumed: 0}},
		}, at(0), money.FromCents(math.MaxInt64 / 2)},
	}
	for _, tc := range cases {
		got, _ := prorationCredit(tc.b, tc.now)
		if !got.Equal(tc.want) {
			t.Errorf("%s: credit=%s want %s", tc.name, got, tc.want)
		}
	}
}

// 明细：付费合计、剩余与总天数、起决定作用的流量行与比例（百万分之一，向下取整）。
func TestProrationCreditDetail(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	b := prorationBasis{Value: dec("30.00"), PaidSpan: 30 * day, PeriodStart: start, PeriodEnd: start.Add(30 * day),
		Traffic: []trafficAllowance{{Cap: 100, Consumed: 10}, {Cap: 300, Consumed: 225}}}
	credit, d := prorationCredit(b, start.Add(10*day))
	// 时间比 20/30，第一条 90/100，第二条 75/300 = 0.25 最小
	if !credit.Equal(dec("7.50")) || !d.Paid.Equal(dec("30.00")) || d.DaysLeft != 20 || d.DaysTotal != 30 ||
		d.TrafficLeft != 75 || d.TrafficTotal != 300 || d.RatioPPM != 250_000 {
		t.Fatalf("credit=%s detail=%+v", credit, d)
	}
	// 没有付费单：明细里流量照样汇总，金额为 0
	_, d = prorationCredit(prorationBasis{PeriodStart: start, PeriodEnd: start.Add(30 * day),
		Traffic: []trafficAllowance{{Cap: 100, Consumed: 40}}}, start)
	if !d.Paid.IsZero() || d.TrafficLeft != 60 || d.TrafficTotal != 100 || d.RatioPPM != 0 {
		t.Fatalf("detail=%+v", d)
	}
	// 时间比取 1/3 的比例按百万分之一向下取整
	_, d = prorationCredit(prorationBasis{Value: dec("10.00"), PaidSpan: 30 * day, PeriodStart: start,
		PeriodEnd: start.Add(30 * day)}, start.Add(20*day))
	if d.RatioPPM != 333_333 {
		t.Fatalf("ratio=%d", d.RatioPPM)
	}
}

// 换掉一份时不保留的赠送天数：付费天数先用、赠送天数最后用。
func TestGiftedDaysLeft(t *testing.T) {
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	month := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).Sub(start) // 31 天
	cases := []struct {
		name string
		b    prorationBasis
		now  time.Time
		want int
	}{
		{"只有付费的一期：没有赠送", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month)}, start.Add(3 * day), 0},
		{"付费一期 + 加时长卡 7 天", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day)}, start.Add(3 * day), 7},
		{"付费一期 + 7 天 + 套餐卡续一个月", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day + 30*day)}, start.Add(time.Hour), 37},
		{"付费期已用完、正在用赠送的", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 7*day)}, start.Add(month + 2*day + time.Hour), 4},
		{"没有付费单：剩下的都是赠送的", prorationBasis{PeriodStart: start, PeriodEnd: start.Add(30 * day)}, start.Add(10*day + time.Minute), 19},
		{"不满一天不写", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month + 20*time.Hour)}, start, 0},
		{"已到期", prorationBasis{PaidSpan: month, PeriodStart: start, PeriodEnd: start.Add(month)}, start.Add(month + day), 0},
	}
	for _, c := range cases {
		if got := giftedDaysLeft(c.b, c.now); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

// 提前续费后 cycle 行之后还没开始的几期额度：按行长度等比算、向下取整。
func TestPendingCycleAllowance(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * day)
	cases := []struct {
		name   string
		cap    int64
		subEnd time.Time
		want   int64
	}{
		{"没有待开始的周期", 1000, end, 0},
		{"周期末在行末之前", 1000, end.Add(-day), 0},
		{"续了一整期", 1000, end.Add(30 * day), 1000},
		{"续了两期", 1000, end.Add(60 * day), 2000},
		{"半期向下取整", 999, end.Add(15 * day), 499},
		{"不限量的行", 0, end.Add(30 * day), 0},
	}
	for _, c := range cases {
		if got := pendingCycleAllowance(c.cap, start, end, c.subEnd); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
	if got := pendingCycleAllowance(1000, end, end, end.Add(day)); got != 0 {
		t.Errorf("行长度为 0: got %d", got)
	}
}

// 退进余额的差额：剩余价值抵完优惠后的新价还有余的部分。
func TestPlanChangeRefund(t *testing.T) {
	cases := []struct{ credit, subtotal, discount, want string }{
		{"0.00", "10.00", "0.00", "0.00"},  // 没有剩余价值
		{"3.00", "10.00", "0.00", "0.00"},  // 升级补差价
		{"10.00", "10.00", "0.00", "0.00"}, // 恰好抵平
		{"15.00", "10.00", "0.00", "5.00"}, // 降级退差额
		{"9.00", "10.00", "3.00", "2.00"},  // 降级 + 券：折扣让差额变大，多出来的一并退
		{"5.00", "10.00", "10.00", "5.00"}, // 人工赠送：新价算 0，剩余价值全额退
	}
	for _, c := range cases {
		if got := planChangeRefund(dec(c.credit), dec(c.subtotal), dec(c.discount)); !got.Equal(dec(c.want)) {
			t.Errorf("refund(%s,%s,%s)=%s want %s", c.credit, c.subtotal, c.discount, got, c.want)
		}
	}
}
