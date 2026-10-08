// Package confnum 把面板下发配置里的数值、时长字段归一成 Go 类型。
//
// 同一个字段会以几种形态到达：
//   - 签名通道（node/signed_config.go）用 json.Decoder.UseNumber 解码，数字是 json.Number；
//   - 兼容通道与本地缓存按普通 json.Unmarshal 解码，数字是 float64；
//   - 测试与内部构造直接写 int；
//   - 后台允许写字符串（"443"、时长 "15s"）。
//
// 以前每个协议各写一个 type switch，只认其中几种：hy2 的 up_mbps、tuic 的
// heartbeat 在签名通道上收到 json.Number 就判非法，入站整个起不来。所有协议的
// 数值 / 时长解析一律经这里，不要再各写一份。
package confnum

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Int64 把整数值归一成 int64。接受各整数类型、整值浮点、json.Number 与十进制
// 字符串；小数、NaN、Inf、越界与其它类型返回 false。nil 也返回 false，缺省值由
// 调用方决定。
func Int64(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		return uintToInt64(uint64(v))
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		return uintToInt64(v)
	case float32:
		return floatToInt64(float64(v))
	case float64:
		return floatToInt64(v)
	case json.Number:
		return parseIntText(string(v))
	case string:
		return parseIntText(v)
	default:
		return 0, false
	}
}

// Int 同 Int64，结果还须落在 int 范围内。
func Int(value any) (int, bool) {
	n, ok := Int64(value)
	if !ok || int64(int(n)) != n {
		return 0, false
	}
	return int(n), true
}

// Float64 把数值归一成 float64（允许小数）。接受各数值类型、json.Number 与数字
// 字符串；NaN、Inf 与其它类型返回 false。
func Float64(value any) (float64, bool) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case json.Number:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(string(v)), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		n, ok := Int64(value)
		if !ok {
			return 0, false
		}
		f = float64(n)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// Duration 解析时长：数字（含 json.Number、纯整数字符串）按整秒，其它字符串按
// Go 时长语法（"15s"、"1m30s"）。负值、小数秒与无法解析的值返回 false。
func Duration(value any) (time.Duration, bool) {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if d, err := time.ParseDuration(text); err == nil {
			return d, d >= 0
		}
	}
	seconds, ok := Int64(value)
	if !ok || seconds < 0 || seconds > int64(math.MaxInt64/int64(time.Second)) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// Truthy 把开关类字段归一：bool 原样；数字大于 0 为真（负数为假）；字符串 "true" / "1" 为真。
// 第二个返回值表示这个值是否是可识别的开关形态。
func Truthy(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1":
			return true, true
		case "false", "0", "":
			return false, true
		}
		return false, false
	}
	if f, ok := Float64(value); ok {
		return f > 0, true
	}
	return false, false
}

func parseIntText(text string) (int64, bool) {
	text = strings.TrimSpace(text)
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}
	// json.Number 可能是 "1e3"、"443.0" 这类整值写法。
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return floatToInt64(f)
}

func floatToInt64(f float64) (int64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
		return 0, false
	}
	// 2^63 本身已越界；比较用浮点常量避免转换溢出。
	if f < -9.223372036854775808e18 || f >= 9.223372036854775808e18 {
		return 0, false
	}
	return int64(f), true
}

func uintToInt64(v uint64) (int64, bool) {
	if v > math.MaxInt64 {
		return 0, false
	}
	return int64(v), true
}
