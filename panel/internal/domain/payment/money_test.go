package payment

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestParseMinor(t *testing.T) {
	cases := []struct {
		in   string
		exp  int32
		want int64
	}{
		// 易支付最常见的形态
		{"9.90", 2, 990},
		{"0.01", 2, 1},
		{"100.00", 2, 10000},
		{"1", 2, 100},
		{"1.5", 2, 150},
		{"0", 2, 0},
		{"0.00", 2, 0},
		{"1234567.89", 2, 123456789},

		// 这一组是用 float 必错的：9.90*100 = 989.9999999999999
		{"9.90", 2, 990},
		{"29.70", 2, 2970},
		{"1.10", 2, 110},
		{"2.20", 2, 220},
		{"4.70", 2, 470},
		{"8.20", 2, 820},

		// 省略整数位 / 小数位
		{".5", 2, 50},
		{"5.", 2, 500},

		// 零位币种
		{"100", 0, 100},
		{"100.00", 0, 100}, // 末尾多余的零允许
		{"0", 0, 0},

		// 三位币种
		{"1.234", 3, 1234},
		{"1.2", 3, 1200},

		// 符号与前导零
		{"+9.90", 2, 990},
		{"-9.90", 2, -990},
		{"007.50", 2, 750},

		// 空白
		{"  9.90  ", 2, 990},
	}

	for _, c := range cases {
		t.Run(c.in+"/e"+strconv.Itoa(int(c.exp)), func(t *testing.T) {
			got, err := ParseMinor(c.in, c.exp)
			if err != nil {
				t.Fatalf("ParseMinor(%q, %d) 返回错误: %v", c.in, c.exp, err)
			}
			if got != c.want {
				t.Errorf("ParseMinor(%q, %d) = %d, 期望 %d", c.in, c.exp, got, c.want)
			}
		})
	}
}

func TestParseMinorRejects(t *testing.T) {
	cases := []struct {
		in      string
		exp     int32
		wantErr error
	}{
		{"", 2, ErrBadAmount},
		{"   ", 2, ErrBadAmount},
		{"abc", 2, ErrBadAmount},
		{"9.9.9", 2, ErrBadAmount},
		{"+", 2, ErrBadAmount},
		{"-", 2, ErrBadAmount},
		{"9,90", 2, ErrBadAmount},  // 逗号小数点
		{"1e3", 2, ErrBadAmount},   // 科学计数法
		{"9.999", 2, ErrPrecision}, // 超精度必须报错，不能悄悄截断成 9.99
		{"0.001", 2, ErrPrecision},
		{"1.5", 0, ErrPrecision},                     // 日元不接受小数
		{"99999999999999999999.00", 2, ErrBadAmount}, // 溢出
	}

	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, err := ParseMinor(c.in, c.exp)
			if err == nil {
				t.Fatalf("ParseMinor(%q, %d) 应当报错但通过了", c.in, c.exp)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("ParseMinor(%q) 错误类型 = %v, 期望 %v", c.in, err, c.wantErr)
			}
		})
	}
}

func TestFormatMinor(t *testing.T) {
	cases := []struct {
		in   int64
		exp  int32
		want string
	}{
		{990, 2, "9.90"},
		{1, 2, "0.01"},
		{0, 2, "0.00"},
		{10000, 2, "100.00"},
		{123456789, 2, "1234567.89"},
		{-990, 2, "-9.90"},
		{100, 0, "100"},
		{1234, 3, "1.234"},
		{5, 3, "0.005"},
	}

	for _, c := range cases {
		got := FormatMinor(c.in, c.exp)
		if got != c.want {
			t.Errorf("FormatMinor(%d, %d) = %q, 期望 %q", c.in, c.exp, got, c.want)
		}
	}
}

// TestRoundTrip 确保 提交给渠道 → 渠道回调 → 解析回来 全程不丢精度。
// 这条链路上任何一处用了浮点，这个测试都会挂。
func TestRoundTrip(t *testing.T) {
	for _, exp := range []int32{0, 2, 3} {
		for v := int64(0); v < 20000; v += 7 {
			s := FormatMinor(v, exp)
			back, err := ParseMinor(s, exp)
			if err != nil {
				t.Fatalf("往返失败 v=%d exp=%d s=%q: %v", v, exp, s, err)
			}
			if back != v {
				t.Fatalf("往返不一致 v=%d exp=%d s=%q back=%d", v, exp, s, back)
			}
		}
	}
}

// TestFloatWouldBeWrong 用具体数字记录「为什么不能用 float」，
// 防止后来者出于简洁把 ParseMinor 改写成 ParseFloat*100。
func TestFloatWouldBeWrong(t *testing.T) {
	// 朴素浮点实现
	naive := func(s string) int64 {
		f, _ := strconv.ParseFloat(s, 64)
		return int64(f * 100)
	}

	broken := []string{"9.90", "29.70", "1.10", "2.20", "4.70", "8.20"}
	for _, s := range broken {
		want, err := ParseMinor(s, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got := naive(s); got == want {
			// 若某天平台上浮点恰好不出错，这个测试的警示意义就没了，
			// 但正确性本身仍由上面的用例保证，所以只记录不失败。
			t.Logf("注意：%q 在本平台浮点实现下恰好正确（%d）", s, got)
		} else {
			t.Logf("浮点实现 %q → %d，正确值 %d（相差 %d 个最小单位）",
				s, got, want, want-got)
		}
	}

	// 确认 9.90 的浮点表示确实小于精确值，这是 int64() 截断丢钱的根因
	f, _ := strconv.ParseFloat("9.90", 64)
	if f*100 >= 990.0 {
		t.Logf("本平台 9.90*100 = %v，未低于 990", f*100)
	} else {
		t.Logf("已确认 9.90*100 = %v < 990，截断即丢 1 分", f*100)
	}
	_ = math.Abs
}
