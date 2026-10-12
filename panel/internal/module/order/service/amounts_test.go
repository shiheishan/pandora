package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

func cents(n int64) decimal.Decimal { return money.FromCents(n) }

func assertDecimalEqual(t *testing.T, name, field string, got, want decimal.Decimal) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s %s: got %s want %s", name, field, got.String(), want.String())
	}
}

func assertBalancePlanEqual(t *testing.T, name string, got, want BalancePlan) {
	t.Helper()
	assertDecimalEqual(t, name, "Applied", got.Applied, want.Applied)
	assertDecimalEqual(t, name, "Payable", got.Payable, want.Payable)
	assertDecimalEqual(t, name, "Kept", got.Kept, want.Kept)
	assertDecimalEqual(t, name, "Waived", got.Waived, want.Waived)
	if got.Forced != want.Forced {
		t.Fatalf("%s Forced: got %v want %v", name, got.Forced, want.Forced)
	}
	if got.BelowMinimum != want.BelowMinimum {
		t.Fatalf("%s BelowMinimum: got %v want %v", name, got.BelowMinimum, want.BelowMinimum)
	}
	if got.SmallDue != want.SmallDue {
		t.Fatalf("%s SmallDue: got %v want %v", name, got.SmallDue, want.SmallDue)
	}
}

func TestOrderTotal(t *testing.T) {
	cases := []struct {
		name                          string
		subtotal, discount, proration decimal.Decimal
		want                          decimal.Decimal
	}{
		{
			name:      "普通",
			subtotal:  cents(3000),
			discount:  cents(200),
			proration: cents(100),
			want:      cents(2700),
		},
		{
			name:      "折扣等于小计",
			subtotal:  cents(500),
			discount:  cents(500),
			proration: decimal.Zero,
			want:      decimal.Zero,
		},
		{
			name:      "抵扣超过小计",
			subtotal:  cents(1000),
			discount:  cents(200),
			proration: cents(900),
			want:      decimal.Zero,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OrderTotal(tc.subtotal, tc.discount, tc.proration)
			assertDecimalEqual(t, tc.name, "total", got, tc.want)
		})
	}
}

func TestPlanBalance(t *testing.T) {
	const minPay = 100 // 支付宝、微信最低 ¥1.00
	cases := []struct {
		name                          string
		due, available, requested, mp int64
		want                          BalancePlan
	}{
		{name: "S7 余额抵一部分", due: 3000, available: 1200, requested: 1200, mp: minPay,
			want: BalancePlan{Applied: cents(1200), Payable: cents(1800)}},
		{name: "S7 关掉余额", due: 3000, available: 1200, requested: 0, mp: minPay,
			want: BalancePlan{Applied: decimal.Zero, Payable: cents(3000)}},
		{name: "S7b 余额够付", due: 3000, available: 5000, requested: 5000, mp: minPay,
			want: BalancePlan{Applied: cents(3000), Payable: decimal.Zero}},
		{name: "S7c 只差几毛：少用 0.5 让还需支付正好 1 元", due: 3000, available: 2950, requested: 2950, mp: minPay,
			want: BalancePlan{Applied: cents(2900), Payable: cents(100), Kept: cents(50)}},
		{name: "S7c 用户自己填了一个会剩几分钱的数", due: 3000, available: 5000, requested: 2990, mp: minPay,
			want: BalancePlan{Applied: cents(2900), Payable: cents(100), Kept: cents(90)}},
		{name: "还需支付正好等于最低额：不动", due: 3000, available: 2900, requested: 2900, mp: minPay,
			want: BalancePlan{Applied: cents(2900), Payable: cents(100)}},
		{name: "Forced 应付 0.3 元 余额够：只能用余额付", due: 30, available: 850, requested: 0, mp: minPay,
			want: BalancePlan{Applied: cents(30), Payable: decimal.Zero, Forced: true}},
		{name: "Forced 余额正好等于应付", due: 30, available: 30, requested: 10, mp: minPay,
			want: BalancePlan{Applied: cents(30), Payable: decimal.Zero, Forced: true}},
		{name: "Forced 开着余额、余额正好够付整单低于最低额（B6c）：同样标 Forced", due: 50, available: 50, requested: 50, mp: minPay,
			want: BalancePlan{Applied: cents(50), Payable: decimal.Zero, Forced: true}},
		{name: "Forced 开着余额、余额有富余", due: 30, available: 850, requested: 850, mp: minPay,
			want: BalancePlan{Applied: cents(30), Payable: decimal.Zero, Forced: true}},
		{name: "余额付清但整单不低于最低额：不 Forced", due: 100, available: 100, requested: 100, mp: minPay,
			want: BalancePlan{Applied: cents(100), Payable: decimal.Zero}},
		{name: "余额付清、没有最低额限制：不 Forced", due: 30, available: 30, requested: 30, mp: 1,
			want: BalancePlan{Applied: cents(30), Payable: decimal.Zero}},
		{name: "Short 余额为 0", due: 30, available: 0, requested: 0, mp: minPay,
			want: BalancePlan{Applied: decimal.Zero, Payable: cents(30), BelowMinimum: true}},
		{name: "Short 余额不够 开着余额：用尽余额", due: 30, available: 20, requested: 20, mp: minPay,
			want: BalancePlan{Applied: cents(20), Payable: cents(10), BelowMinimum: true}},
		{name: "Short 余额不够 关掉余额：同样用尽余额，零头不会变大", due: 30, available: 20, requested: 0, mp: minPay,
			want: BalancePlan{Applied: cents(20), Payable: cents(10), BelowMinimum: true}},
		{name: "requested 超过余额：截到余额", due: 3000, available: 1200, requested: 99999, mp: minPay,
			want: BalancePlan{Applied: cents(1200), Payable: cents(1800)}},
		{name: "requested 超过应付：截到应付", due: 500, available: 99999, requested: 99999, mp: minPay,
			want: BalancePlan{Applied: cents(500), Payable: decimal.Zero}},
		{name: "负数 requested 当 0", due: 500, available: 1000, requested: -5, mp: minPay,
			want: BalancePlan{Applied: decimal.Zero, Payable: cents(500)}},
		{name: "应付为 0", due: 0, available: 1000, requested: 1000, mp: minPay,
			want: BalancePlan{}},
		{name: "minPay 为 0 等于没有限制", due: 3000, available: 2950, requested: 2950, mp: 0,
			want: BalancePlan{Applied: cents(2950), Payable: cents(50)}},
		{name: "minPay 为 1 等于没有限制", due: 30, available: 0, requested: 0, mp: 1,
			want: BalancePlan{Applied: decimal.Zero, Payable: cents(30)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanBalance(cents(tc.due), cents(tc.available), cents(tc.requested), cents(tc.mp))
			assertBalancePlanEqual(t, tc.name, got, tc.want)

			due := cents(tc.due)
			if due.IsNegative() {
				due = decimal.Zero
			}
			sum := got.Applied.Add(got.Payable)
			if !sum.Equal(due) {
				t.Fatalf("%s applied+payable=%s due=%s", tc.name, sum.String(), due.String())
			}
			avail := cents(tc.available)
			if avail.IsNegative() {
				avail = decimal.Zero
			}
			if got.Applied.GreaterThan(avail) || got.Applied.IsNegative() {
				t.Fatalf("%s applied %s outside [0,%s]", tc.name, got.Applied.String(), avail.String())
			}
			min := cents(tc.mp)
			oneCent := decimal.New(1, -2)
			if !got.BelowMinimum && min.GreaterThan(oneCent) && got.Payable.IsPositive() && got.Payable.LessThan(min) {
				t.Fatalf("%s payable %s below minimum %s", tc.name, got.Payable.String(), min.String())
			}
		})
	}
}

func TestWaiveSmallDue(t *testing.T) {
	minPay := cents(100)
	// 换套餐的零头：免掉，BelowMinimum 清掉
	got := WaiveSmallDue(PlanBalance(cents(30), cents(20), decimal.Zero, minPay), true)
	want := BalancePlan{Applied: cents(20), Payable: decimal.Zero, SmallDue: true, Waived: cents(10)}
	assertBalancePlanEqual(t, "waive", got, want)

	// 不是换套餐（新购、续费、流量包、后台开单）：不免，仍是 BelowMinimum，调用方拒绝
	got = WaiveSmallDue(PlanBalance(cents(30), decimal.Zero, decimal.Zero, minPay), false)
	if !got.BelowMinimum || got.SmallDue || !got.Payable.Equal(cents(30)) {
		t.Fatalf("not allowed = %+v", got)
	}

	// 零头上限 99 分，与最低额无关：最低额 ¥5 时差 ¥1.00 也不免
	got = WaiveSmallDue(PlanBalance(cents(100), decimal.Zero, decimal.Zero, cents(500)), true)
	if !got.BelowMinimum || got.SmallDue {
		t.Fatalf("over the cap = %+v", got)
	}
	got = WaiveSmallDue(PlanBalance(cents(99), decimal.Zero, decimal.Zero, cents(500)), true)
	if !got.SmallDue || !got.Waived.Equal(cents(99)) {
		t.Fatalf("at the cap = %+v", got)
	}

	// 不是 BelowMinimum 的原样返回
	for _, b := range []BalancePlan{
		PlanBalance(cents(3000), cents(1200), cents(1200), minPay),
		PlanBalance(cents(30), cents(50), decimal.Zero, minPay),
	} {
		if !balancePlanFieldsEqual(WaiveSmallDue(b, true), b) {
			t.Fatalf("non short balance must be unchanged: %+v", b)
		}
	}
}

func balancePlanFieldsEqual(a, b BalancePlan) bool {
	return a.Applied.Equal(b.Applied) && a.Payable.Equal(b.Payable) && a.Kept.Equal(b.Kept) &&
		a.Waived.Equal(b.Waived) && a.Forced == b.Forced && a.BelowMinimum == b.BelowMinimum && a.SmallDue == b.SmallDue
}

func TestManualDueBelowMinimum(t *testing.T) {
	cases := []struct {
		due, min int64
		want     bool
	}{
		{2500, 3000, true},
		{3000, 3000, false},
		{0, 3000, false},
		{50, 1, false},
		{50, 0, false},
	}
	for _, tc := range cases {
		got := manualDueBelowMinimum(cents(tc.due), cents(tc.min))
		if got != tc.want {
			t.Fatalf("manualDueBelowMinimum(%d,%d)=%v want %v", tc.due, tc.min, got, tc.want)
		}
	}
}

func TestQuoteTime(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	at, err := quoteTime(nil, now)
	if err != nil || !at.Equal(now) {
		t.Fatalf("nil expectation: at=%s err=%v", at, err)
	}

	at, err = quoteTime(&Expect{}, now)
	if err != nil || !at.Equal(now) {
		t.Fatalf("zero AsOf: at=%s err=%v", at, err)
	}

	for name, asOf := range map[string]time.Time{
		"11 minutes ago": now.Add(-11 * time.Minute),
		"in the future":  now.Add(time.Second),
	} {
		_, err := quoteTime(&Expect{AsOf: asOf}, now)
		if !errors.Is(err, ErrQuoteChanged) {
			t.Errorf("%s: err=%v want ErrQuoteChanged", name, err)
		}
	}

	wantAsOf := now.Add(-9 * time.Minute)
	at, err = quoteTime(&Expect{AsOf: wantAsOf}, now)
	if err != nil || !at.Equal(wantAsOf) {
		t.Fatalf("inside the window: at=%s err=%v want %s", at, err, wantAsOf)
	}
}

func TestCheckExpect(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	b := BalancePlan{Applied: cents(2900), Payable: cents(100)}
	total := cents(3000)

	ok := &Expect{
		AsOf:           now.Add(-2 * time.Minute),
		Total:          total,
		BalanceApplied: cents(2900),
		Payable:        cents(100),
	}
	if err := checkExpect(ok, total, b, now); err != nil {
		t.Fatalf("matching expectation: %v", err)
	}
	if err := checkExpect(nil, cents(1), b, now); err != nil {
		t.Fatalf("no expectation must pass: %v", err)
	}

	for name, e := range map[string]Expect{
		"total":   {AsOf: ok.AsOf, Total: cents(2999), BalanceApplied: cents(2900), Payable: cents(100)},
		"applied": {AsOf: ok.AsOf, Total: total, BalanceApplied: cents(2950), Payable: cents(100)},
		"payable": {AsOf: ok.AsOf, Total: total, BalanceApplied: cents(2900), Payable: cents(50)},
		"stale":   {AsOf: now.Add(-time.Hour), Total: total, BalanceApplied: cents(2900), Payable: cents(100)},
	} {
		if err := checkExpect(&e, total, b, now); !errors.Is(err, ErrQuoteChanged) {
			t.Errorf("%s mismatch err=%v", name, err)
		}
	}
}

func TestNewOrderNo(t *testing.T) {
	fixed := time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)
	prefix := "AO20261012-"
	seen := make(map[string]bool)

	for i := 0; i < 200; i++ {
		no, err := newOrderNo(fixed)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if len(no) != 19 {
			t.Fatalf("iteration %d: len %d want 19, no=%q", i, len(no), no)
		}
		if !strings.HasPrefix(no, prefix) {
			t.Fatalf("iteration %d: prefix want %q got %q", i, prefix, no)
		}
		if !orderNoPattern.MatchString(no) {
			t.Fatalf("iteration %d: pattern mismatch %q", i, no)
		}
		if seen[no] {
			t.Fatalf("iteration %d: duplicate %q", i, no)
		}
		seen[no] = true
	}
}
