package purchase

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
	// SmallDue 应付本身低于最低额、余额又不够：Payable 无法在线支付。
	// 处理口径（用户 8.1 第 1 题，按推荐 A）：由调用方把 Payable 作为折扣免掉，
	// 审计 digest 记 small_due_waived
	SmallDue bool `json:"small_due"`
	// Waived 是调用方按 SmallDue 免掉、记作折扣的部分（ApplyBalance 本身不填，见 WaiveSmallDue）
	Waived int64 `json:"waived"`
}

// WaiveSmallDue 按用户 8.1 第 1 题的推荐 A 处理 SmallDue：剩下那点在线付不了的钱免掉，
// Payable 归零、记进 Waived（调用方把它并进订单折扣，审计记 small_due_waived）。
// 不是 SmallDue 时原样返回。
func WaiveSmallDue(b Balance) Balance {
	if !b.SmallDue || b.Payable <= 0 {
		return b
	}
	b.Waived = b.Payable
	b.Payable = 0
	return b
}

// ApplyBalance 计算余额的用法（原型 payCalc，加上应付本身低于最低额的情形）。
//
//	due        这单应付（已减去折扣与原套餐没用完的部分）
//	available  用户当前可用余额
//	requested  用户想用的余额（开关打开时前端传 available，关掉传 0）
//	minPay     支付最低额；0 或 1 等于没有限制
//
// 规则：
//  1. requested 限制在 0 到 min(due, available) 之间。
//  2. payable = due − requested。若 0 < payable < minPay：
//     due ≥ minPay：只用 due − minPay 的余额，让还需支付正好等于 minPay，少用的记 Kept；
//     due < minPay 且余额够付整单：全用余额，Forced；
//     due < minPay 且余额不够：SmallDue，Applied 与 Payable 照第 1 步。
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
		b.SmallDue = true
	}
	return b
}
