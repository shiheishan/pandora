package service

import (
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

var (
	// MaxFeeFixed 是固定手续费的上限（元），与库里 CHECK 一致。
	MaxFeeFixed = decimal.RequireFromString("1000.00")
	// MaxFeePercent 是比例手续费的上限（%），与库里 CHECK 一致。
	MaxFeePercent = decimal.RequireFromString("100.00")
)

// Surcharge 按渠道配置算这次向用户加收的手续费：amount × percent / 100 + fixed，向上取整到分
// （不足一分按一分收；照 Xboard 的「固定 + 比例」，Xboard 是四舍五入，这里按产品定向上取整）。
// amount 不为正或两项都是 0 时返回 0。金额两位小数。
func Surcharge(amount, fixed, percent decimal.Decimal) decimal.Decimal {
	if !amount.IsPositive() || (fixed.IsZero() && percent.IsZero()) {
		return decimal.Zero
	}
	return amount.Mul(percent).Shift(-2).Add(fixed).RoundCeil(2)
}

// ValidFeeFixed 报告固定手续费是否合法：不为负、最多两位小数、不超过 MaxFeeFixed。
func ValidFeeFixed(d decimal.Decimal) bool {
	if d.IsNegative() || money.Check(d) != nil {
		return false
	}
	return !d.GreaterThan(MaxFeeFixed)
}

// ValidFeePercent 报告比例手续费是否合法：不为负、最多两位小数、不超过 MaxFeePercent。
func ValidFeePercent(d decimal.Decimal) bool {
	if d.IsNegative() || money.Check(d) != nil {
		return false
	}
	return !d.GreaterThan(MaxFeePercent)
}
