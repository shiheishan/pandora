package service

import (
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
)

// withMember 给一条已算好券折扣（q.Discount）与剩余价值（q.Credit）的报价补上会员折扣，并按「券 + 会员」重算总额与
// 换套餐退回余额的钱（Refund；没有剩余价值的种类 Credit 为 0，算出来恒为 0）。
// 会员折扣照 Xboard：按小计算、与券折扣相加，合计不超过小计（promosvc.MemberDiscount，向零截断只截一次）。
// pct 是用户的会员折扣率（0 = 没有），一次报价只读一次（s.promo.MemberPercent）。建单（place）与履约用同一口径：
// discount_amount = 券 + 会员，planChangeRefund(credit, subtotal, discount_amount)。
func withMember(q *Quote, pct int) {
	q.MemberDiscount = promosvc.MemberDiscount(q.Subtotal, q.Discount, pct)
	q.MemberPercent = 0
	if q.MemberDiscount.IsPositive() {
		q.MemberPercent = pct
	}
	all := q.Discount.Add(q.MemberDiscount)
	q.Total = OrderTotal(q.Subtotal, all, q.Credit)
	q.Refund = planChangeRefund(q.Credit, q.Subtotal, all)
}
