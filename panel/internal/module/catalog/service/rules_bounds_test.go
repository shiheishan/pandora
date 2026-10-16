package service

// rules_bounds_test.go 覆盖金额与数字上界的纯规则：负数、两位小数、千万元上界、次数与天数边界，
// 不让越界值到库里变成 22003 或被 PG 静默四舍五入。

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidateTiersAmounts(t *testing.T) {
	fields := func(tiers ...TierInput) map[string]string {
		f := map[string]string{}
		validateTiers(tiers, f)
		return f
	}
	t.Run("空列表", func(t *testing.T) {
		if f := fields(); f["prices"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("周期不在四档", func(t *testing.T) {
		for _, tier := range []TierInput{
			{BillingInterval: IntervalMonth, IntervalCount: 2},
			{BillingInterval: IntervalYear, IntervalCount: 2},
			{BillingInterval: IntervalOneTime, IntervalCount: 1},
		} {
			if f := fields(tier); f["prices.0"] == "" {
				t.Fatalf("tier=%+v fields=%v", tier, f)
			}
		}
	})
	t.Run("同档重复", func(t *testing.T) {
		f := fields(
			TierInput{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.New(1, 0)},
			TierInput{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.New(2, 0)},
		)
		if f["prices.1"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("负数金额", func(t *testing.T) {
		f := fields(TierInput{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.New(-1, 0)})
		if f["prices.0.unit_amount"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("三位小数", func(t *testing.T) {
		f := fields(TierInput{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("1.001")})
		if f["prices.0.unit_amount"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("超过 1000000 元", func(t *testing.T) {
		f := fields(TierInput{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("1000000.01")})
		if f["prices.0.unit_amount"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("0 元与上界本身合法", func(t *testing.T) {
		f := fields(
			TierInput{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.Zero},
			TierInput{BillingInterval: IntervalYear, IntervalCount: 1, UnitAmount: maxAmountYuan},
		)
		if len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
}

func TestValidateResetBounds(t *testing.T) {
	fields := func(price decimal.Decimal, limit *int) map[string]string {
		f := map[string]string{}
		validateReset(price, limit, f)
		return f
	}
	t.Run("价格 0", func(t *testing.T) {
		if f := fields(decimal.Zero, nil); f["reset.price"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("价格负数", func(t *testing.T) {
		if f := fields(decimal.New(-1, 0), nil); f["reset.price"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("价格三位小数", func(t *testing.T) {
		if f := fields(decimal.RequireFromString("0.001"), nil); f["reset.price"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("次数 0", func(t *testing.T) {
		if f := fields(decimal.New(1, 0), rulesIntPtr(0)); f["reset.limit"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("次数 1001", func(t *testing.T) {
		if f := fields(decimal.New(1, 0), rulesIntPtr(maxResetLimit+1)); f["reset.limit"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("次数 1、1000、nil 合法", func(t *testing.T) {
		for _, limit := range []*int{rulesIntPtr(1), rulesIntPtr(maxResetLimit), nil} {
			if f := fields(decimal.New(1, 0), limit); len(f) != 0 {
				t.Fatalf("limit=%v fields=%v", limit, f)
			}
		}
	})
	t.Run("合法价格", func(t *testing.T) {
		if f := fields(decimal.RequireFromString("5.00"), nil); len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
}

func TestValidateOneTimeBounds(t *testing.T) {
	fields := func(amount decimal.Decimal, days *int) map[string]string {
		f := map[string]string{}
		validateOneTime(OneTimeInput{UnitAmount: amount, ValidityDays: days}, f)
		return f
	}
	t.Run("天数 0", func(t *testing.T) {
		if f := fields(decimal.New(9, 0), rulesIntPtr(0)); f["validity_days"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("天数 36501", func(t *testing.T) {
		if f := fields(decimal.New(9, 0), rulesIntPtr(maxValidityDays+1)); f["validity_days"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("天数 1、36500、nil 合法", func(t *testing.T) {
		for _, days := range []*int{rulesIntPtr(1), rulesIntPtr(maxValidityDays), nil} {
			if f := fields(decimal.New(9, 0), days); len(f) != 0 {
				t.Fatalf("days=%v fields=%v", days, f)
			}
		}
	})
	t.Run("金额负数", func(t *testing.T) {
		if f := fields(decimal.New(-1, 0), nil); f["unit_amount"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("金额 0 元合法（永久免费）", func(t *testing.T) {
		if f := fields(decimal.Zero, nil); len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
}
