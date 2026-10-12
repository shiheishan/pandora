// Package money 是金额的唯一口径：币种、小数位、与整数分和渠道字符串的换算、按比例取整（docs/architecture.md 第 6 节）。
//
// 金额一律用 shopspring/decimal，库里是 NUMERIC(20,2)、单位元；/v1 的 JSON 仍是整数分。
// 按比例得出的金额一律向零截断，每个公式只在最后一步截断一次。
package money

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/shopspring/decimal"
)

// Currency 是币种代码。
type Currency string

// 支持的币种；库里有同样的 CHECK。
const (
	CNY Currency = "CNY"
	USD Currency = "USD"
)

// Scale 是金额的小数位数（两种币种都是 2 位，单位元）。
const Scale int32 = 2

var (
	// ErrPrecision 表示金额超过 2 位小数。
	ErrPrecision = errors.New("金额超过 2 位小数")
	// ErrBadAmount 表示金额格式非法。
	ErrBadAmount = errors.New("金额格式非法")
	// ErrNegative 表示金额为负。
	ErrNegative = errors.New("金额不能为负")
	// ErrCurrency 表示不支持的币种。
	ErrCurrency = errors.New("不支持的币种")
)

// ParseCurrency 校验并规范化币种代码（大小写不敏感）。
func ParseCurrency(s string) (Currency, error) {
	c := Currency(strings.ToUpper(strings.TrimSpace(s)))
	if !c.Valid() {
		return "", fmt.Errorf("%w: %q", ErrCurrency, s)
	}
	return c, nil
}

// Valid 报告币种是否受支持。
func (c Currency) Valid() bool { return c == CNY || c == USD }

// Zero 是 0 元。
var Zero = decimal.Zero

// Check 断言金额最多 2 位小数；repo 写入前必须调用（PostgreSQL 遇到多余小数会静默四舍五入）。
func Check(d decimal.Decimal) error {
	if !d.Equal(d.Truncate(Scale)) {
		return fmt.Errorf("%w: %s", ErrPrecision, d.String())
	}
	return nil
}

// CheckNonNegative 断言金额最多 2 位小数且不为负。
func CheckNonNegative(d decimal.Decimal) error {
	if d.IsNegative() {
		return fmt.Errorf("%w: %s", ErrNegative, d.String())
	}
	return Check(d)
}

// FromCents 把 /v1 请求里的整数分换成金额。
func FromCents(cents int64) decimal.Decimal { return decimal.New(cents, -Scale) }

// ToCents 把金额换成 /v1 响应里的整数分；超过 2 位小数或超出 int64 时报错（不静默截断）。
func ToCents(d decimal.Decimal) (int64, error) {
	if err := Check(d); err != nil {
		return 0, err
	}
	s := d.Shift(Scale)
	if !s.BigInt().IsInt64() {
		return 0, fmt.Errorf("%w: %s 超出 int64 分", ErrBadAmount, d.String())
	}
	return s.IntPart(), nil
}

// MustCents 是 ToCents 的断言版，只用在已经过 Check 的值（如从 NUMERIC(20,2) 列读出的值）上，换算失败直接 panic。
func MustCents(d decimal.Decimal) int64 {
	n, err := ToCents(d)
	if err != nil {
		panic(err)
	}
	return n
}

// ParseProvider 解析渠道回调与查单里的金额字符串（如易支付的 "9.90"）：只收十进制数字，超过 2 位小数直接拒绝。
func ParseProvider(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "eE") {
		return Zero, fmt.Errorf("%w: %q", ErrBadAmount, s)
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Zero, fmt.Errorf("%w: %q", ErrBadAmount, s)
	}
	if err := CheckNonNegative(d); err != nil {
		return Zero, err
	}
	return d, nil
}

// FormatProvider 把金额写成渠道要的固定 2 位小数字符串。
func FormatProvider(d decimal.Decimal) string { return d.StringFixed(Scale) }

// Truncate 把金额向零截断到分。只在领域计算的最后一步调用。
func Truncate(d decimal.Decimal) decimal.Decimal { return d.Truncate(Scale) }

// MulRatio 计算 d × num / den，结果向零截断到分（百分比券、佣金、折算都用它）；den 必须大于 0。
// 用有理数精确计算后只截断一次，不经过中间小数。
func MulRatio(d decimal.Decimal, num, den int64) decimal.Decimal {
	if den <= 0 {
		panic("money.MulRatio: 分母必须大于 0")
	}
	cents := d.Shift(Scale)
	if !cents.Equal(cents.Truncate(0)) {
		panic("money.MulRatio: 输入超过 2 位小数")
	}
	r := new(big.Rat).SetInt(cents.BigInt())
	r.Mul(r, big.NewRat(num, den))
	// big.Int.Quo 向零截断
	return decimal.NewFromBigInt(new(big.Int).Quo(r.Num(), r.Denom()), -Scale)
}

// BasisPoints 是万分比的分母。
const BasisPoints = 10000

// ApplyBP 按万分比取金额（d × bp / 10000，向零截断）。
func ApplyBP(d decimal.Decimal, bp int64) decimal.Decimal { return MulRatio(d, bp, BasisPoints) }
