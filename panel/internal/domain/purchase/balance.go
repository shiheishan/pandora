package purchase

// MaxSmallDueWaive 是 SmallDue 一次最多免掉的钱（分）：用户 8.1 第 1 题的承诺是「每次最多少收
// 不到 ¥1」，所以是固定常量，与渠道的最低付款额无关。
const MaxSmallDueWaive = 99

// Balance 是一单用多少余额、还要在线付多少。
type Balance struct {
	// Applied 用掉的余额
	Applied int64 `json:"applied"`
	// Payable 还需在线支付
	Payable int64 `json:"payable"`
	// Kept 为了凑够支付最低额而少用、留在余额里的钱
	Kept int64 `json:"kept"`
	// Forced 应付本身低于最低额且余额够付：只能全用余额付，前端把余额开关锁成打开
	Forced bool `json:"forced"`
	// Short 应付本身低于最低额、用尽余额后剩下的钱仍然在线付不了。Applied 是全部可用余额，
	// Payable 是剩下的零头。只有换套餐的零头（≤ MaxSmallDueWaive）能免（见 WaiveSmallDue）；
	// 其余情形不能下单，前端提示「先充值或使用余额支付」
	Short bool `json:"below_minimum"`
	// SmallDue 剩下的零头已按用户 8.1 第 1 题推荐 A 免掉：Payable 归零、记进 Waived，调用方把它
	// 并进订单折扣，审计 digest 记 small_due_waived
	SmallDue bool `json:"small_due"`
	// Waived 免掉的零头（分）
	Waived int64 `json:"waived"`
}

// ApplyBalance 计算余额的用法（原型 payCalc，加上应付本身低于最低额的情形）。
//
//	due        这单应付（已减去折扣与原套餐没用完的部分）
//	available  用户当前可用余额
//	requested  用户想用的余额（开关打开时前端传 available，关掉传 0）
//	minPay     能在线付的最低额；0 或 1 等于没有限制
//
// 规则：
//  1. requested 限制在 0 到 min(due, available) 之间。
//  2. payable = due − requested。若 0 < payable < minPay：
//     due ≥ minPay：只用 due − minPay 的余额，让还需支付正好等于 minPay，少用的记 Kept；
//     due < minPay 且余额够付整单：全用余额，Forced（与开关无关）；
//     due < minPay 且余额不够：用尽余额，剩下的零头标 Short（与开关无关，关掉余额不会让零头变大）。
func ApplyBalance(due, available, requested, minPay int64) Balance {
	if due < 0 {
		due = 0
	}
	if available < 0 {
		available = 0
	}
	limit := due
	if available < limit {
		limit = available
	}
	if requested < 0 {
		requested = 0
	}
	if requested > limit {
		requested = limit
	}
	b := Balance{Applied: requested, Payable: due - requested}
	if b.Payable <= 0 || minPay <= 1 || b.Payable >= minPay {
		return b
	}
	switch {
	case due >= minPay:
		applied := due - minPay
		b.Kept = b.Applied - applied
		b.Applied = applied
		b.Payable = minPay
	case available >= due:
		b.Applied = due
		b.Payable = 0
		b.Forced = true
	default:
		b.Applied = available
		b.Payable = due - available
		b.Short = true
	}
	return b
}

// WaiveSmallDue 按用户 8.1 第 1 题的推荐 A 处理零头：只有 allowed（换套餐抵扣后剩下的零头）且
// 零头不超过 MaxSmallDueWaive 时免掉——Payable 归零、记进 Waived、Short 清掉、SmallDue 置位。
// 其余情形原样返回，Short 仍为真，调用方拒绝下单。
func WaiveSmallDue(b Balance, allowed bool) Balance {
	if !allowed || !b.Short || b.Payable <= 0 || b.Payable > MaxSmallDueWaive {
		return b
	}
	b.Waived = b.Payable
	b.Payable = 0
	b.Short = false
	b.SmallDue = true
	return b
}
