package service

// 换套餐的剩余价值（对照旧 plan_change_quote.go，R3 向零截断、R5 比值用交叉相乘）：
//
//	剩余价值 = 本周期付费合计 × min(剩余付费时间比例, 剩余流量比例)，向下取整到分
//
// 「本周期」从最近一次让周期重新起算的事件开始（开通、换套餐、过期后续回、救回的延期），由 subscription 给出这段时间里
// 挂了订单的事件（PeriodOrderIDs）。提前续费是接在原到期之后，不改周期起点，所以一个周期可能由几张付费单拼成，基数是合计。
//
// 每张单的付费按 max(小计 − 折扣 − 免掉的零头, 0) 计：0 元单、全额券、人工赠送单为 0；换套餐单的「小计 − 折扣」里含它用掉的
// 上一段剩余价值，那部分本来就是真金白银折过来的，要计进去，否则连换两次会把第一次的钱弄丢。
//
// 时间比例按「已付费天数先用、赠送天数最后用」：
//
//	付费总时长 P = 从周期起点起依次接上每张付费单的计费周期
//	剩余付费   = min(P − (now − 周期起点), 周期末 − now, P)，不小于 0
//	时间比例   = 剩余付费 / P
//
// 流量比例只看跟着整个付费周期走的 traffic.bytes 配额（period 为 cycle 或 total、有上限）：剩余 / 上限。
// 按日、按月的配额只反映当天、当月，不参与。不限量的套餐没有这种行，流量比例视为 1。

import (
	"math/big"
	"time"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

// day 是一天的时长（剩余天数、赠送天数按整天向下取整）。
const day = 24 * time.Hour

// trafficAllowance 是一条参与折算的流量配额：Cap 是本期上限（含提前续费后还没开始的几期），Consumed 是已用。
type trafficAllowance struct {
	Cap      int64
	Consumed int64
}

// prorationBasis 是折算的全部输入。
type prorationBasis struct {
	// Value 是本周期付费合计（元）；Currency 是它的币种，本周期没有付费单时为空
	Value    decimal.Decimal
	Currency money.Currency
	// PaidSpan 是本周期付费单买下的总时长
	PaidSpan    time.Duration
	PeriodStart time.Time
	PeriodEnd   time.Time
	Traffic     []trafficAllowance
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
	d := CreditDetail{Paid: decimal.Max(b.Value, decimal.Zero), DaysTotal: int(b.PaidSpan / day)}
	for _, t := range b.Traffic {
		d.TrafficTotal += max(t.Cap, 0)
		d.TrafficLeft += max(min(t.Cap-t.Consumed, t.Cap), 0)
	}
	if !b.Value.IsPositive() || b.PaidSpan <= 0 {
		return decimal.Zero, d
	}
	remaining := min(b.PaidSpan-now.Sub(b.PeriodStart), b.PeriodEnd.Sub(now), b.PaidSpan)
	if remaining <= 0 {
		return decimal.Zero, d
	}
	d.DaysLeft = int(remaining / day)
	num, den := big.NewInt(int64(remaining)), big.NewInt(int64(b.PaidSpan))

	for _, t := range b.Traffic {
		left := max(min(t.Cap-t.Consumed, t.Cap), 0)
		if t.Cap <= 0 || left == 0 {
			d.TrafficLeft, d.TrafficTotal = left, max(t.Cap, 0)
			return decimal.Zero, d
		}
		// left/cap < num/den  ⇔  left·den < num·cap
		l := new(big.Int).Mul(big.NewInt(left), den)
		r := new(big.Int).Mul(num, big.NewInt(t.Cap))
		if l.Cmp(r) < 0 {
			num, den = big.NewInt(left), big.NewInt(t.Cap)
			d.TrafficLeft, d.TrafficTotal = left, t.Cap
		}
	}

	ppm := new(big.Int).Mul(num, big.NewInt(1_000_000))
	d.RatioPPM = ppm.Quo(ppm, den).Int64()
	// 金额按分算：Value 来自 NUMERIC(20,2) 列，移两位就是整数分
	cents := b.Value.Shift(money.Scale).BigInt()
	credit := new(big.Int).Mul(cents, num)
	credit.Quo(credit, den)
	return decimal.NewFromBigInt(credit, -money.Scale), d
}

// giftedDaysLeft 是到期前来自赠送的剩余整天数：按「付费天数先用、赠送天数最后用」，赠送的那段是
// max(现在, 周期起点 + 付费总时长) 到周期末。换掉这一份时这段不折钱，后台开单预览写「赠送的 N 天不保留」。
func giftedDaysLeft(b prorationBasis, now time.Time) int {
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

// pendingCycleAllowance 是 cycle 配额行之后、订阅周期末之前还没开始的那几期的额度：
// cap × (订阅周期末 − 行末) / (行末 − 行起点)，向下取整。提前续费后 cycle 行停在原到期日、订阅周期末已经往后推，
// 后面几期的流量还一点没用，不算进来的话本期流量用完就把下一期已付的钱也折成 0。没有待开始的周期时为 0。
func pendingCycleAllowance(rowCap int64, rowStart, rowEnd, subEnd time.Time) int64 {
	span := rowEnd.Sub(rowStart)
	if rowCap <= 0 || span <= 0 || !subEnd.After(rowEnd) {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(rowCap), big.NewInt(int64(subEnd.Sub(rowEnd))))
	n.Quo(n, big.NewInt(int64(span)))
	if !n.IsInt64() {
		return 0
	}
	return n.Int64()
}

// planChangeRefund 是换套餐履约时退进余额的钱：剩余价值抵完新价（优惠后）还有余的部分。
func planChangeRefund(credit, subtotal, discount decimal.Decimal) decimal.Decimal {
	return decimal.Max(credit.Sub(subtotal.Sub(discount)), decimal.Zero)
}
