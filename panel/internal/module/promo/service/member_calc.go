package service

import (
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

// MemberPercentMax 是会员折扣（减免百分比）的上限，与 00043 member_discounts.percent 的 CHECK 一致。
const MemberPercentMax = 100

// MemberDiscount 算一单的会员折扣（照 Xboard OrderService::setVipDiscount：按小计算、与券折扣相加）：
// 小计 × percent / 100 向零截断到分（只截这一次），再压到「小计 − 券折扣」以内，两者合计不超过小计。
// percent ≤ 0（没有会员折扣）或小计不为正时回 0；percent 超过 100 按 100 算。
func MemberDiscount(subtotal, couponDiscount decimal.Decimal, percent int) decimal.Decimal {
	if percent <= 0 || !subtotal.IsPositive() {
		return decimal.Zero
	}
	if percent > MemberPercentMax {
		percent = MemberPercentMax
	}
	d := money.ApplyBP(subtotal, int64(percent)*100)
	room := subtotal.Sub(couponDiscount)
	if room.IsNegative() {
		room = decimal.Zero
	}
	if d.GreaterThan(room) {
		d = room
	}
	return d
}
