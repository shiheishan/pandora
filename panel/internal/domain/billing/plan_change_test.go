// [INPUT]: 依赖 plan_change_quote.go 的 prorationCredit、reservations.go 的 orderTotal
// [OUTPUT]: 对外提供 TestProrationCredit、TestOrderTotalWithProration
// [POS]: billing 变更套餐折算的纯函数边界测试；真实 SQL 下的基数与履约在 plan_change_pg18_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"math"
	"testing"
	"time"
)

// 折算规则（D-E-2 与 2026-09-24 补充）的边界：时间比与流量比取小、向下取整、
// 付费天数先用、赠送天数折不成钱、0 元单不折。
func TestProrationCredit(t *testing.T) {
	day := 24 * time.Hour
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	month := prorationBasis{Value: 3000, PaidSpan: 30 * day,
		PeriodStart: start, PeriodEnd: start.Add(30 * day)}
	at := func(d time.Duration) time.Time { return start.Add(d) }
	with := func(b prorationBasis, mutate func(*prorationBasis)) prorationBasis {
		mutate(&b)
		return b
	}

	cases := []struct {
		name string
		b    prorationBasis
		now  time.Time
		want int64
	}{
		{"unused period is worth the whole payment", month, at(0), 3000},
		{"time ratio when no traffic quota", month, at(10 * day), 2000},
		{"floor to the cent", with(month, func(b *prorationBasis) { b.Value = 1000 }), at(20 * day), 333},
		{"traffic ratio wins when smaller", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 75}}
		}), at(10 * day), 750},
		{"time ratio wins when smaller", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 10}}
		}), at(15 * day), 1500},
		{"smallest of several traffic rows", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 10}, {Cap: 50, Consumed: 40}}
		}), at(0), 600},
		{"traffic used up is worth nothing", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 100}}
		}), at(day), 0},
		{"traffic over quota is worth nothing", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 100, Consumed: 180}}
		}), at(day), 0},
		{"non-positive cap is worth nothing", with(month, func(b *prorationBasis) {
			b.Traffic = []trafficAllowance{{Cap: 0, Consumed: 0}}
		}), at(day), 0},
		{"zero-priced order is worth nothing", with(month, func(b *prorationBasis) { b.Value = 0 }), at(0), 0},
		{"no paid time is worth nothing", with(month, func(b *prorationBasis) { b.PaidSpan = 0 }), at(0), 0},
		{"ended period is worth nothing", month, at(31 * day), 0},
		// 赠送 30 天接在付费 30 天后面：已用 10 天全算付费的，剩余付费 20/30。
		{"gift days are used last", with(month, func(b *prorationBasis) {
			b.PeriodEnd = start.Add(60 * day)
		}), at(10 * day), 2000},
		// 已用 40 天：付费的 30 天用完了，剩下的全是赠送，折不成钱。
		{"only gift days left are worth nothing", with(month, func(b *prorationBasis) {
			b.PeriodEnd = start.Add(60 * day)
		}), at(40 * day), 0},
		// 两张单叠出 60 天付费：已用 45 天，剩 15/60。
		{"stacked paid orders share one basis", with(month, func(b *prorationBasis) {
			b.Value, b.PaidSpan, b.PeriodEnd = 6000, 60*day, start.Add(60*day)
		}), at(45 * day), 1500},
		// 周期被管理员缩短时按周期末封顶，不会超过付费时长。
		{"period end caps the remaining paid time", with(month, func(b *prorationBasis) {
			b.PeriodEnd = start.Add(20 * day)
		}), at(10 * day), 1000},
		{"no overflow on large amounts and quotas", prorationBasis{
			Value: math.MaxInt64 / 2, PaidSpan: 3650 * day, PeriodStart: start,
			PeriodEnd: start.Add(3650 * day),
			Traffic:   []trafficAllowance{{Cap: math.MaxInt64 / 2, Consumed: 0}},
		}, at(0), math.MaxInt64 / 2},
	}
	for _, tc := range cases {
		if got := prorationCredit(tc.b, tc.now); got != tc.want {
			t.Errorf("%s: credit=%d want %d", tc.name, got, tc.want)
		}
	}
}

// 金额恒等式（00071 的 orders_total_identity）：剩余价值先抵新价，抵不完的不进 total。
func TestOrderTotalWithProration(t *testing.T) {
	cases := []struct {
		subtotal, discount, credit, tax, want int64
	}{
		{1000, 0, 0, 0, 1000},    // 普通订单不受影响
		{1000, 200, 0, 0, 800},   // 优惠券
		{1000, 0, 300, 0, 700},   // 升级补差价
		{1000, 200, 300, 0, 500}, // 升级 + 优惠券
		{1000, 0, 1000, 0, 0},    // 恰好抵平
		{1000, 0, 1500, 0, 0},    // 降级：total 为 0，差额另退余额
		{1000, 300, 900, 0, 0},   // 降级 + 优惠券
		{1000, 0, 1500, 7, 7},    // 税照加
	}
	for _, tc := range cases {
		if got := orderTotal(tc.subtotal, tc.discount, tc.credit, tc.tax); got != tc.want {
			t.Errorf("orderTotal(%d,%d,%d,%d)=%d want %d",
				tc.subtotal, tc.discount, tc.credit, tc.tax, got, tc.want)
		}
	}
}
