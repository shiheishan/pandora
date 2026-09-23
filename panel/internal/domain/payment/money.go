// Package payment 定义支付渠道适配器（PAY-002）与跨渠道通用的金额换算。
package payment

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

var (
	ErrBadAmount    = errors.New("金额格式非法")
	ErrPrecision    = errors.New("金额精度超出币种允许位数")
	ErrNotSupported = errors.New("该渠道不支持此操作")
)

// Exponent 返回币种的最小单位位数。
//
// 绝大多数币种是 2 位（分），但日元、韩元等是 0 位，
// 突尼斯第纳尔等是 3 位。写死 100 会在这些币种上算错。
func Exponent(currency string) int32 {
	switch strings.ToUpper(currency) {
	case "JPY", "KRW", "VND", "CLP", "ISK", "TWD":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}

// ParseMinor 把十进制金额字符串解析为最小单位整数。
//
// 为什么不用 float：strconv.ParseFloat("9.90", 64) 得到的是最接近 9.9 的
// 二进制浮点数，乘 100 后是 989.9999999999999，取整就丢了一分钱。
// 易支付、支付宝、微信的回调金额全都是十进制字符串，
// 每一笔都要经过这个函数，所以它必须是纯整数运算。
//
// 精度超出币种位数时报错而不是静默截断 —— 截断意味着悄悄吞掉用户的钱。
func ParseMinor(s string, exponent int32) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: 空字符串", ErrBadAmount)
	}

	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg = true
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("%w: 只有符号位", ErrBadAmount)
	}

	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if hasDot && strings.Contains(fracPart, ".") {
		return 0, fmt.Errorf("%w: 多个小数点", ErrBadAmount)
	}
	// ".5" 视为 "0.5"；"5." 视为 "5.0"
	if intPart == "" {
		intPart = "0"
	}

	if !isDigits(intPart) || (fracPart != "" && !isDigits(fracPart)) {
		return 0, fmt.Errorf("%w: 含非数字字符 %q", ErrBadAmount, s)
	}

	exp := int(exponent)
	switch {
	case len(fracPart) > exp:
		// 允许末尾多余的零，如 JPY 的 "100.00"
		if strings.Trim(fracPart[exp:], "0") != "" {
			return 0, fmt.Errorf("%w: %q 超过 %d 位小数", ErrPrecision, s, exp)
		}
		fracPart = fracPart[:exp]
	case len(fracPart) < exp:
		fracPart += strings.Repeat("0", exp-len(fracPart))
	}

	digits := strings.TrimLeft(intPart+fracPart, "0")
	if digits == "" {
		return 0, nil
	}

	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q 超出 int64 范围", ErrBadAmount, s)
	}
	if neg {
		v = -v
	}
	return v, nil
}

// FormatMinor 把最小单位整数格式化为十进制字符串。
// 用于向渠道提交金额，同样全程整数运算。
// MulDiv 计算 a * b / c，中间乘法不溢出 int64。
//
// 金额字段在数据库里是 bigint，但 int64 乘法阶段仍可能溢出：合法的
// 大额订单乘上万分比/千分比系数就超过 2^63。这里用 big.Int 做中间
// 运算，结果再校验能否落回 int64，溢出时报错而不是静默给出错误数字。
func MulDiv(a, b, c int64) (int64, error) {
	if c == 0 {
		return 0, errors.New("除数不能为 0")
	}
	r := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	r = r.Div(r, big.NewInt(c))
	if !r.IsInt64() {
		return 0, errors.New("金额乘法溢出 int64")
	}
	return r.Int64(), nil
}

func FormatMinor(minor int64, exponent int32) string {
	exp := int(exponent)
	if exp == 0 {
		return strconv.FormatInt(minor, 10)
	}

	neg := minor < 0
	if neg {
		// math.MinInt64 取反会溢出回自身，走无符号绝对值处理。
		if minor == math.MinInt64 {
			u := uint64(1) << 63
			s := strconv.FormatUint(u, 10)
			if len(s) <= exp {
				s = strings.Repeat("0", exp-len(s)+1) + s
			}
			return "-" + s[:len(s)-exp] + "." + s[len(s)-exp:]
		}
		minor = -minor
	}

	s := strconv.FormatInt(minor, 10)
	if len(s) <= exp {
		s = strings.Repeat("0", exp-len(s)+1) + s
	}

	out := s[:len(s)-exp] + "." + s[len(s)-exp:]
	if neg {
		out = "-" + out
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
