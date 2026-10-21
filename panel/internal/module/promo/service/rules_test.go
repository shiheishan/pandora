package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/promo/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func ptrDec(s string) *decimal.Decimal { d := dec(s); return &d }

func ptrTime(t time.Time) *time.Time { return &t }

func ptrInt(n int) *int { return &n }

func TestDiscountFor(t *testing.T) {
	cases := []struct {
		name     string
		face     Face
		max      *decimal.Decimal
		subtotal decimal.Decimal
		want     string
	}{
		{"百分比向零 9.99×33.33%", Face{Kind: KindPercent, PercentBP: 3333}, nil, dec("9.99"), "3.32"},
		{"百分比向零 0.01×50%", Face{Kind: KindPercent, PercentBP: 5000}, nil, dec("0.01"), "0.00"},
		{"百分比 100%", Face{Kind: KindPercent, PercentBP: 10000}, nil, dec("100.00"), "100.00"},
		{"封顶 30", Face{Kind: KindPercent, PercentBP: 5000}, ptrDec("30.00"), dec("200.00"), "30.00"},
		{"封顶 120 仍不超小计", Face{Kind: KindPercent, PercentBP: 5000}, ptrDec("120.00"), dec("200.00"), "100.00"},
		{"定额 10 对 50", Face{Kind: KindFixed, AmountOff: dec("10.00")}, nil, dec("50.00"), "10.00"},
		{"定额超过小计", Face{Kind: KindFixed, AmountOff: dec("80.00")}, nil, dec("50.00"), "50.00"},
		{"小计 0", Face{Kind: KindFixed, AmountOff: dec("10.00")}, nil, dec("0"), "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := discountFor(tc.face, tc.max, tc.subtotal)
			if !got.Equal(dec(tc.want)) {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestCheckBasicsOrder(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	baseCoupon := func() model.Coupon {
		return model.Coupon{
			Status:         StatusActive,
			MinOrderAmount: decimal.Zero,
			MaxRedemptions: nil,
			RedeemedCount:  0,
			ReservedCount:  0,
		}
	}
	baseTarget := Target{Subtotal: dec("50.00")}

	cases := []struct {
		name string
		mut  func(*model.Coupon)
		want error
	}{
		{"正常", func(c *model.Coupon) {}, nil},
		{"paused", func(c *model.Coupon) { c.Status = StatusPaused }, ErrCouponNotFound},
		{"未开始", func(c *model.Coupon) { c.ValidFrom = ptrTime(future) }, ErrCouponNotFound},
		{"已过期", func(c *model.Coupon) { c.ValidUntil = ptrTime(past) }, ErrCouponExpired},
		{"容量用尽", func(c *model.Coupon) {
			c.MaxRedemptions = ptrInt(2)
			c.RedeemedCount = 1
			c.ReservedCount = 1
		}, ErrCouponUsedUp},
		{"未达门槛", func(c *model.Coupon) { c.MinOrderAmount = dec("60.00") }, ErrCouponMinAmount},
		{"暂停优先于过期", func(c *model.Coupon) {
			c.Status = StatusPaused
			c.ValidUntil = ptrTime(past)
		}, ErrCouponNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := baseCoupon()
			if tc.mut != nil {
				tc.mut(&c)
			}
			err := checkBasics(c, baseTarget, now)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestCheckPlan(t *testing.T) {
	planA := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	planB := uuid.MustParse("00000000-0000-4000-8000-000000000002")

	if err := checkPlan(nil, &planA); err != nil {
		t.Fatalf("空列表任意 plan: %v", err)
	}
	if err := checkPlan(nil, nil); err != nil {
		t.Fatalf("空列表 nil plan: %v", err)
	}

	if err := checkPlan([]uuid.UUID{planA}, &planA); err != nil {
		t.Fatalf("A 应通过: %v", err)
	}
	if !errors.Is(checkPlan([]uuid.UUID{planA}, &planB), ErrCouponNotForPlan) {
		t.Fatal("B 应拒绝")
	}
	if !errors.Is(checkPlan([]uuid.UUID{planA}, nil), ErrCouponNotForPlan) {
		t.Fatal("nil plan 应拒绝")
	}
}

func TestCanTransition(t *testing.T) {
	trueCases := [][]string{
		{RedemptionHeld, RedemptionCaptured},
		{RedemptionHeld, RedemptionReleased},
	}
	for _, p := range trueCases {
		if !CanTransition(p[0], p[1]) {
			t.Fatalf("%s→%s 应为 true", p[0], p[1])
		}
	}
	falseCases := [][]string{
		{RedemptionCaptured, RedemptionReleased},
		{RedemptionReleased, RedemptionHeld},
		{RedemptionHeld, RedemptionHeld},
		{RedemptionCaptured, RedemptionCaptured},
	}
	for _, p := range falseCases {
		if CanTransition(p[0], p[1]) {
			t.Fatalf("%s→%s 应为 false", p[0], p[1])
		}
	}
}

func TestValidateSpec(t *testing.T) {
	planA := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	planB := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

	valid := Spec{
		Name:       "夏季",
		Face:       Face{Kind: KindPercent, PercentBP: 1000},
		MinOrder:   decimal.Zero,
		MaxPerUser: 1,
		PlanIDs:    []uuid.UUID{planA, planB, planA},
	}
	out, err := validateSpec(valid)
	if err != nil {
		t.Fatalf("合法规格: %v", err)
	}
	if len(out.PlanIDs) != 2 || out.PlanIDs[0] != planA || out.PlanIDs[1] != planB {
		t.Fatalf("去重顺序: %v", out.PlanIDs)
	}

	invalid := []struct {
		name string
		spec Spec
		key  string
	}{
		{"percent 0", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 0}, MaxPerUser: 1}, "discount_value"},
		{"percent 10001", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 10001}, MaxPerUser: 1}, "discount_value"},
		{"fixed 0", Spec{Name: "x", Face: Face{Kind: KindFixed, AmountOff: decimal.Zero}, MaxPerUser: 1}, "discount_value"},
		{"fixed 0.001", Spec{Name: "x", Face: Face{Kind: KindFixed, AmountOff: dec("0.001")}, MaxPerUser: 1}, "discount_value"},
		{"kind x", Spec{Name: "x", Face: Face{Kind: "x"}, MaxPerUser: 1}, "discount_type"},
		{"fixed 带封顶", Spec{Name: "x", Face: Face{Kind: KindFixed, AmountOff: dec("1")}, MaxDiscount: ptrDec("1"), MaxPerUser: 1}, "max_discount"},
		{"percent 封顶 0", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 100}, MaxDiscount: ptrDec("0"), MaxPerUser: 1}, "max_discount"},
		{"门槛 -1", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 100}, MinOrder: dec("-1"), MaxPerUser: 1}, "min_order_amount"},
		{"max_redemptions 0", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 100}, MaxRedemptions: ptrInt(0), MaxPerUser: 1}, "max_redemptions"},
		{"max_per_user 0", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 100}, MaxPerUser: 0}, "max_redemptions_per_user"},
		{"valid_until", Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 100}, MaxPerUser: 1, ValidFrom: ptrTime(until), ValidUntil: ptrTime(from)}, "valid_until"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateSpec(tc.spec)
			if err == nil {
				t.Fatal("应失败")
			}
			fe := errs.As(err)
			if fe.Fields == nil || fe.Fields[tc.key] == "" {
				t.Fatalf("Fields 缺 %q: %v", tc.key, fe.Fields)
			}
		})
	}

	many := make([]uuid.UUID, 201)
	for i := range many {
		many[i] = uuid.New()
	}
	_, err = validateSpec(Spec{Name: "x", Face: Face{Kind: KindPercent, PercentBP: 100}, MaxPerUser: 1, PlanIDs: many})
	if err == nil {
		t.Fatal("201 套餐应失败")
	}
	if errs.As(err).Fields["applicable_plan_ids"] == "" {
		t.Fatalf("应含 applicable_plan_ids: %v", errs.As(err).Fields)
	}

	for _, name := range []string{"\x00", "   "} {
		_, err := validateSpec(Spec{Name: name, Face: Face{Kind: KindPercent, PercentBP: 100}, MaxPerUser: 1})
		if err == nil || errs.As(err).Fields["name"] == "" {
			t.Fatalf("name %q: %v", name, err)
		}
	}
}

func TestValidateCodeAndPrefix(t *testing.T) {
	if NormalizeCode("  ab-1 ") != "AB-1" {
		t.Fatal("NormalizeCode 空白与短横线")
	}
	if NormalizeCode("ıd") == "ID" {
		t.Fatal("土耳其无点 i 不应折成 ID")
	}
	if NormalizeCode("ſale") == "SALE" {
		t.Fatal("长 s 不应折成 SALE")
	}
	for _, bad := range []string{
		"", "A B", "\x00", "AB\x00C", strings.Repeat("A", 65),
		"新年", "ÄB", "ab", "A.B",
	} {
		if err := validateCode(bad); err == nil {
			t.Fatalf("validateCode 应拒 %q", bad)
		}
	}
	for _, ok := range []string{"SUMMER2026", "A-B_1", strings.Repeat("Z", 64)} {
		if err := validateCode(ok); err != nil {
			t.Fatalf("validateCode 应过 %q: %v", ok, err)
		}
	}
	p, err := normalizePrefix(" vip ")
	if err != nil || p != "VIP" {
		t.Fatalf("normalizePrefix vip: %q %v", p, err)
	}
	for _, pair := range []struct{ in, want string }{
		{"a-b", "A-B"},
		{"x_1", "X_1"},
	} {
		got, err := normalizePrefix(pair.in)
		if err != nil || got != pair.want {
			t.Fatalf("normalizePrefix %q: got %q err %v", pair.in, got, err)
		}
	}
	for _, bad := range []string{"TOOLONGXX", "新", "A.B", "A B"} {
		if _, err := normalizePrefix(bad); err == nil {
			t.Fatalf("prefix 应拒 %q", bad)
		}
	}
}

func TestInvalidCodeNotFound(t *testing.T) {
	ctx := context.Background()
	s := &Service{now: time.Now}
	tg := Target{
		UserID:   uuid.New(),
		Subtotal: decimal.RequireFromString("10.00"),
	}
	badCodes := []string{
		"AB\x00C", "新年", "ÄB", strings.Repeat("A", 65), "A B",
	}
	for _, code := range badCodes {
		_, err := s.Evaluate(ctx, code, tg)
		if !errors.Is(err, ErrCouponNotFound) {
			t.Fatalf("Evaluate %q: %v", code, err)
		}
		if fe := errs.As(err); fe.Code == errs.Internal {
			t.Fatalf("Evaluate %q 不应 Internal", code)
		}
		_, err = s.Reserve(ctx, code, tg, OrderRef{ID: uuid.New(), No: "T1"})
		if !errors.Is(err, ErrCouponNotFound) {
			t.Fatalf("Reserve %q: %v", code, err)
		}
		if fe := errs.As(err); fe.Code == errs.Internal {
			t.Fatalf("Reserve %q 不应 Internal", code)
		}
	}
	got, err := s.Evaluate(ctx, "   ", tg)
	if err != nil || got != nil {
		t.Fatalf("空白码 Evaluate: got=%v err=%v", got, err)
	}
}

func TestNewCode(t *testing.T) {
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		code, err := newCode("VIP")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(code, "VIP") || len(code) != 13 {
			t.Fatalf("格式: %q", code)
		}
		for _, r := range code[3:] {
			if !strings.ContainsRune(couponAlphabet, r) {
				t.Fatalf("非法字符 %c in %q", r, code)
			}
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("重复: %q", code)
		}
		seen[code] = struct{}{}
	}
}

func TestIsRejection(t *testing.T) {
	for _, err := range []error{
		ErrCouponNotFound, ErrCouponExpired, ErrCouponUsedUp, ErrCouponUserLimit,
		ErrCouponMinAmount, ErrCouponNotForPlan, ErrCouponNotForUser,
	} {
		if !IsRejection(err) {
			t.Fatalf("应为 rejection: %v", err)
		}
	}
	for _, err := range []error{ErrCodeExists, ErrNotHeld, errors.New("x")} {
		if IsRejection(err) {
			t.Fatalf("不应为 rejection: %v", err)
		}
	}
}
