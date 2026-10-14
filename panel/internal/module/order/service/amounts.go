package service

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"time"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

// orderNoAlphabet 是订单号随机后缀用的字符集（去掉易混淆的 I/O/0/1）。
const orderNoAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

var orderNoPattern = regexp.MustCompile(`^[A-Z0-9-]{1,64}$`)

// OrderTotal 计算订单总额（免 waived 之前）：max(小计 − 折扣 − 剩余价值折算, 0)。
// 库里恒等式 total = greatest(subtotal − discount − proration_credit, 0) − waived，waived 由调用方另减。
func OrderTotal(subtotal, discount, prorationCredit decimal.Decimal) decimal.Decimal {
	t := subtotal.Sub(discount).Sub(prorationCredit)
	if t.IsNegative() {
		return decimal.Zero
	}
	return t
}

// PlanBalance 计算一单用多少余额、还要在线付多少（对照旧 purchase.ApplyBalance）。
func PlanBalance(due, available, requested, minPay decimal.Decimal) BalancePlan {
	if due.IsNegative() {
		due = decimal.Zero
	}
	if available.IsNegative() {
		available = decimal.Zero
	}
	limit := due
	if available.LessThan(limit) {
		limit = available
	}
	if requested.IsNegative() {
		requested = decimal.Zero
	}
	if requested.GreaterThan(limit) {
		requested = limit
	}
	b := BalancePlan{
		Applied: requested,
		Payable: due.Sub(requested),
	}
	oneCent := decimal.New(1, -2)
	if !b.Payable.IsPositive() {
		// 余额已付清整单；整单本身低于最低额时标 Forced（与开关无关）。
		b.Forced = due.IsPositive() && minPay.GreaterThan(oneCent) && due.LessThan(minPay)
		return b
	}
	if minPay.LessThanOrEqual(oneCent) || !b.Payable.LessThan(minPay) {
		return b
	}
	switch {
	case !due.LessThan(minPay):
		applied := due.Sub(minPay)
		b.Kept = b.Applied.Sub(applied)
		b.Applied = applied
		b.Payable = minPay
	case !available.LessThan(due):
		b.Applied = due
		b.Payable = decimal.Zero
		b.Forced = true
	default:
		b.Applied = available
		b.Payable = due.Sub(available)
		b.BelowMinimum = true
	}
	return b
}

// WaiveSmallDue 按换套餐零头规则免掉小额应付（对照旧 purchase.WaiveSmallDue）。
func WaiveSmallDue(b BalancePlan, allowed bool) BalancePlan {
	ceiling := money.FromCents(MaxSmallDueCents)
	if !allowed || !b.BelowMinimum || !b.Payable.IsPositive() || b.Payable.GreaterThan(ceiling) {
		return b
	}
	b.Waived = b.Payable
	b.Payable = decimal.Zero
	b.BelowMinimum = false
	b.SmallDue = true
	return b
}

// manualDueBelowMinimum 是后台待支付单「用户付不了」的判定：应付为正且低于站点最低额（最低额 ≤ 0.01 等于不限）。
func manualDueBelowMinimum(due, minPay decimal.Decimal) bool {
	oneCent := decimal.New(1, -2)
	return due.IsPositive() && minPay.GreaterThan(oneCent) && due.LessThan(minPay)
}

// quoteTime 校验并返回报价时刻：nil 或 AsOf 为零用 now；否则 AsOf 须在 [now-QuoteAsOfWindow, now]。
func quoteTime(exp *Expect, now time.Time) (time.Time, error) {
	if exp == nil || exp.AsOf.IsZero() {
		return now, nil
	}
	asOf := exp.AsOf.UTC()
	if asOf.After(now) || now.Sub(asOf) > QuoteAsOfWindow {
		return time.Time{}, ErrQuoteChanged
	}
	return asOf, nil
}

// checkExpect 比对确认下单带回的报价与服务端重算：nil 通过；任一金额不等或报价时刻无效则 ErrQuoteChanged。
func checkExpect(exp *Expect, total decimal.Decimal, b BalancePlan, now time.Time) error {
	if exp == nil {
		return nil
	}
	if _, err := quoteTime(exp, now); err != nil {
		return err
	}
	if !exp.Total.Equal(total) || !exp.BalanceApplied.Equal(b.Applied) || !exp.Payable.Equal(b.Payable) {
		return ErrQuoteChanged
	}
	return nil
}

// newOrderNo 生成门户订单号：AO + UTC 日期 + "-" + 8 位随机字符。
func newOrderNo(now time.Time) (string, error) {
	suffix, err := randomOrderSuffix(8)
	if err != nil {
		return "", err
	}
	return "AO" + now.UTC().Format("20060102") + "-" + suffix, nil
}

// randomOrderSuffix 从 orderNoAlphabet 均匀抽取 n 个字符（拒绝采样，无取模偏差）。
func randomOrderSuffix(n int) (string, error) {
	alphabetLen := len(orderNoAlphabet)
	// limit 是不超过 256 的 alphabetLen 的最大倍数；字节值落在 [limit, 256) 的丢掉重抽
	limit := 256 - 256%alphabetLen
	out := make([]byte, n)
	var buf [16]byte
	for i := 0; i < n; {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("生成订单号随机段: %w", err)
		}
		for _, b := range buf {
			if i == n {
				break
			}
			if int(b) >= limit {
				continue
			}
			out[i] = orderNoAlphabet[int(b)%alphabetLen]
			i++
		}
	}
	return string(out), nil
}
