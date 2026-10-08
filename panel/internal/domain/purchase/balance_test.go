package purchase

import "testing"

func TestApplyBalance(t *testing.T) {
	const minPay = 100 // 支付宝、微信最低 ¥1.00
	cases := []struct {
		name                          string
		due, available, requested, mp int64
		want                          Balance
	}{
		{name: "S7 余额抵一部分", due: 3000, available: 1200, requested: 1200, mp: minPay,
			want: Balance{Applied: 1200, Payable: 1800}},
		{name: "S7 关掉余额", due: 3000, available: 1200, requested: 0, mp: minPay,
			want: Balance{Applied: 0, Payable: 3000}},
		{name: "S7b 余额够付", due: 3000, available: 5000, requested: 5000, mp: minPay,
			want: Balance{Applied: 3000, Payable: 0}},
		{name: "S7c 只差几毛：少用 0.5 让还需支付正好 1 元", due: 3000, available: 2950, requested: 2950, mp: minPay,
			want: Balance{Applied: 2900, Payable: 100, Kept: 50}},
		{name: "S7c 用户自己填了一个会剩几分钱的数", due: 3000, available: 5000, requested: 2990, mp: minPay,
			want: Balance{Applied: 2900, Payable: 100, Kept: 90}},
		{name: "还需支付正好等于最低额：不动", due: 3000, available: 2900, requested: 2900, mp: minPay,
			want: Balance{Applied: 2900, Payable: 100}},
		{name: "Forced 应付 0.3 元 余额够：只能用余额付", due: 30, available: 850, requested: 0, mp: minPay,
			want: Balance{Applied: 30, Payable: 0, Forced: true}},
		{name: "Forced 余额正好等于应付", due: 30, available: 30, requested: 10, mp: minPay,
			want: Balance{Applied: 30, Payable: 0, Forced: true}},
		{name: "SmallDue 余额为 0", due: 30, available: 0, requested: 0, mp: minPay,
			want: Balance{Applied: 0, Payable: 30, SmallDue: true}},
		{name: "SmallDue 余额不够 用了一部分", due: 30, available: 20, requested: 20, mp: minPay,
			want: Balance{Applied: 20, Payable: 10, SmallDue: true}},
		{name: "SmallDue 余额不够 关掉余额", due: 30, available: 20, requested: 0, mp: minPay,
			want: Balance{Applied: 0, Payable: 30, SmallDue: true}},
		{name: "requested 超过余额：截到余额", due: 3000, available: 1200, requested: 99999, mp: minPay,
			want: Balance{Applied: 1200, Payable: 1800}},
		{name: "requested 超过应付：截到应付", due: 500, available: 99999, requested: 99999, mp: minPay,
			want: Balance{Applied: 500, Payable: 0}},
		{name: "负数 requested 当 0", due: 500, available: 1000, requested: -5, mp: minPay,
			want: Balance{Applied: 0, Payable: 500}},
		{name: "应付为 0", due: 0, available: 1000, requested: 1000, mp: minPay,
			want: Balance{}},
		{name: "minPay 为 0 等于没有限制", due: 3000, available: 2950, requested: 2950, mp: 0,
			want: Balance{Applied: 2950, Payable: 50}},
		{name: "minPay 为 1 等于没有限制", due: 30, available: 0, requested: 0, mp: 1,
			want: Balance{Applied: 0, Payable: 30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyBalance(tc.due, tc.available, tc.requested, tc.mp)
			if got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
			// 不变量：用掉的余额 + 还需支付 = 应付；不用超过余额；在线付款要么为 0 要么够最低额（SmallDue 除外）
			if got.Applied+got.Payable != max(tc.due, 0) {
				t.Fatalf("applied+payable=%d due=%d", got.Applied+got.Payable, tc.due)
			}
			if got.Applied > tc.available || got.Applied < 0 {
				t.Fatalf("applied %d outside [0,%d]", got.Applied, tc.available)
			}
			if !got.SmallDue && tc.mp > 1 && got.Payable > 0 && got.Payable < tc.mp {
				t.Fatalf("payable %d below minimum %d", got.Payable, tc.mp)
			}
		})
	}
}

func TestNormalizeLabel(t *testing.T) {
	ok := map[string]string{
		"  妈妈的 iPad ": "妈妈的 iPad",
		"":            "",
		"   ":         "",
		"一二三四五六七八九十一二三四五六": "一二三四五六七八九十一二三四五六",
		"👨‍👩‍👧":  "👨‍👩‍👧",
		"Work 2": "Work 2",
	}
	for in, want := range ok {
		got, err := NormalizeLabel(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeLabel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]error{
		"一二三四五六七八九十一二三四五六七": ErrLabelTooLong,
		"a\nb":   ErrLabelInvalid,
		"a\tb":   ErrLabelInvalid,
		"a\x00b": ErrLabelInvalid,
		"a‮b":    ErrLabelInvalid, // 从右到左覆盖：能伪造文件名
		"a b":    ErrLabelInvalid,
		"a\xffb": ErrLabelInvalid,
	}
	for in, want := range bad {
		if _, err := NormalizeLabel(in); err != want {
			t.Fatalf("NormalizeLabel(%q) err=%v want %v", in, err, want)
		}
	}
}
