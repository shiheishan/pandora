package service

// 换套餐的剩余价值（R3 向零截断、R5 比值用交叉相乘）：
//
// 原订阅是订阅套餐：剩余价值 = 本周期付费合计 × 剩余付费时间比例，不看流量。
// 原订阅是一次性套餐：剩余价值 = 本周期付费合计 × min(剩余天数比例, 剩余流量比例)；
// 永久（期末是永久哨兵）时天数比例视为 1；没有限额流量行时流量比例视为 1。
//
// 「本周期」从最近一次让周期重新起算的事件开始，由 subscription 给出这段时间里挂了订单的事件（PeriodOrderIDs）。
// 基数是本周期付费来源订单的 max(小计 − 折扣 − 免掉的零头, 0) 之和（含叠加单 KindStack）。
//
// 订阅套餐的时间比例按「已付费天数先用、赠送天数最后用」：
//
//	付费总时长 P = 从周期起点起依次接上每张付费单的计费周期
//	剩余付费   = min(P − (now − 周期起点), 周期末 − now, P)，不小于 0
//	时间比例   = 剩余付费 / P
//
// 一次性套餐的时间比例（非永久）：(周期末 − now) / (周期末 − 周期起点)，≤ 0 则剩余价值为 0。
// 一次性套餐的流量比例：限额 total 行剩余 / 上限；多条取最小比例。

import (
	"math/big"
	"time"

	"github.com/shopspring/decimal"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// day 是一天的时长（剩余天数、赠送天数按整天向下取整）。
const day = 24 * time.Hour

// trafficAllowance 是一条参与折算的流量配额：Cap 是上限（不含赠送量），Consumed 是已用。
type trafficAllowance struct {
	Cap      int64
	Consumed int64
}

// prorationBasis 是折算的全部输入。
type prorationBasis struct {
	Value decimal.Decimal // 本周期付费合计（元）；本周期没有付费单时为 0
	// OneTime 为 true 表示原订阅是一次性套餐；Permanent 为一次性且期末是永久哨兵
	OneTime   bool
	Permanent bool
	// PaidSpan 是本周期付费单买下的总时长（一次性按各付费单价格的有效天数相加）；礼品卡、加时长卡送的不算，
	// 折算时付费的先用、送的最后用，送的那段不折钱
	PaidSpan    time.Duration
	PeriodStart time.Time
	PeriodEnd   time.Time
	Traffic     []trafficAllowance // 仅一次性套餐有内容；Cap 只算付费单买下的流量
}

// CreditDetail 是剩余价值「怎么算的」（报价的 credit_detail）：本期付费合计、剩余与总的付费整天数、
// 起决定作用的那条流量配额的剩余与总量、取到的比例（百万分之一，向下取整，只用于展示）。
type CreditDetail struct {
	Paid         decimal.Decimal
	DaysLeft     int
	DaysTotal    int
	TrafficLeft  int64
	TrafficTotal int64
	RatioPPM     int64
}

// prorationCredit 算 now 时刻的剩余价值与明细。全程整数（分）与有理数比较，不经浮点；
// 钱的取整只发生一次，而且只向下。
func prorationCredit(b prorationBasis, now time.Time) (decimal.Decimal, CreditDetail) {
	d := CreditDetail{Paid: decimal.Max(b.Value, decimal.Zero)}
	if b.OneTime {
		for _, t := range b.Traffic {
			d.TrafficTotal += max(t.Cap, 0)
			d.TrafficLeft += max(min(t.Cap-t.Consumed, t.Cap), 0)
		}
		if !b.Permanent {
			d.DaysTotal = int(b.PaidSpan / day)
			if rem := paidRemaining(b, now); rem > 0 {
				d.DaysLeft = int(rem / day)
			}
		}
	} else {
		d.DaysTotal = int(b.PaidSpan / day)
	}

	if !b.Value.IsPositive() {
		return decimal.Zero, d
	}

	var num, den *big.Int
	if b.OneTime {
		num, den = oneTimeTimeRatio(b, now, &d)
		if num == nil {
			return decimal.Zero, d
		}
		num, den = applyTrafficRatio(b.Traffic, num, den, &d)
	} else {
		if b.PaidSpan <= 0 {
			return decimal.Zero, d
		}
		remaining := paidRemaining(b, now)
		if remaining <= 0 {
			return decimal.Zero, d
		}
		d.DaysLeft = int(remaining / day)
		num, den = big.NewInt(int64(remaining)), big.NewInt(int64(b.PaidSpan))
	}

	ppm := new(big.Int).Mul(num, big.NewInt(1_000_000))
	d.RatioPPM = ppm.Quo(ppm, den).Int64()
	cents := b.Value.Shift(money.Scale).BigInt()
	credit := new(big.Int).Mul(cents, num)
	credit.Quo(credit, den)
	return decimal.NewFromBigInt(credit, -money.Scale), d
}

// oneTimeTimeRatio 算一次性套餐的时间比例分子分母；永久或仍有余量时 num/den 非 nil。
func oneTimeTimeRatio(b prorationBasis, now time.Time, d *CreditDetail) (*big.Int, *big.Int) {
	if b.Permanent {
		return big.NewInt(1), big.NewInt(1)
	}
	if b.PaidSpan <= 0 {
		return nil, nil
	}
	rem := paidRemaining(b, now)
	if rem <= 0 {
		return nil, nil
	}
	return big.NewInt(int64(rem)), big.NewInt(int64(b.PaidSpan))
}

// paidRemaining 是 now 时刻还没用掉的付费时长：付费的从周期起点先用，送的接在后面最后用。
func paidRemaining(b prorationBasis, now time.Time) time.Duration {
	return min(b.PaidSpan-now.Sub(b.PeriodStart), b.PeriodEnd.Sub(now), b.PaidSpan)
}

// applyTrafficRatio 在已有时间比例上再与流量比例取较小者。
func applyTrafficRatio(traffic []trafficAllowance, num, den *big.Int, d *CreditDetail) (*big.Int, *big.Int) {
	for _, t := range traffic {
		left := max(min(t.Cap-t.Consumed, t.Cap), 0)
		if t.Cap <= 0 || left == 0 {
			d.TrafficLeft, d.TrafficTotal = left, max(t.Cap, 0)
			return big.NewInt(0), big.NewInt(1)
		}
		l := new(big.Int).Mul(big.NewInt(left), den)
		r := new(big.Int).Mul(num, big.NewInt(t.Cap))
		if l.Cmp(r) < 0 {
			num, den = big.NewInt(left), big.NewInt(t.Cap)
			d.TrafficLeft, d.TrafficTotal = left, t.Cap
		}
	}
	return num, den
}

// giftedDaysLeft 是到期前来自赠送的剩余整天数：订阅套餐按「付费天数先用、赠送天数最后用」；
// 一次性套餐同样（礼品卡叠加送的天数）；永久的不算天数，恒为 0。换掉这一份时这段不折钱，后台开单预览写「赠送的 N 天不保留」。
func giftedDaysLeft(b prorationBasis, now time.Time) int {
	if b.Permanent || subsvc.IsPermanent(b.PeriodEnd) {
		return 0
	}
	from := b.PeriodStart.Add(max(b.PaidSpan, 0))
	if now.After(from) {
		from = now
	}
	left := b.PeriodEnd.Sub(from)
	if left <= 0 {
		return 0
	}
	return int(left / day)
}

// planChangeRefund 是换套餐履约时退进余额的钱：剩余价值抵完新价（优惠后）还有余的部分。
func planChangeRefund(credit, subtotal, discount decimal.Decimal) decimal.Decimal {
	return decimal.Max(credit.Sub(subtotal.Sub(discount)), decimal.Zero)
}
