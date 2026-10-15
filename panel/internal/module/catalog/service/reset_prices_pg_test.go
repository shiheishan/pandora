package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

func TestPlanResetPricesPG(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})

	rows, err := e.svc.ReplacePlanResetPrices(ctx, plan.ID, []ResetPrice{
		{Currency: money.CNY, UnitAmount: decimal.RequireFromString("18.00")},
		{Currency: money.USD, UnitAmount: decimal.RequireFromString("3.00")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Currency != money.CNY || rows[1].Currency != money.USD {
		t.Fatalf("Replace 返回 %+v", rows)
	}

	listed, err := e.svc.PlanResetPrices(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Currency != money.CNY || listed[1].Currency != money.USD {
		t.Fatalf("PlanResetPrices %+v", listed)
	}

	amt, ok, err := e.svc.ResetPrice(ctx, plan.ID, money.CNY)
	if err != nil || !ok || !amt.Equal(decimal.RequireFromString("18.00")) {
		t.Fatalf("ResetPrice CNY amt=%s ok=%v err=%v", amt, ok, err)
	}

	_, ok, err = e.svc.ResetPrice(ctx, uuid.New(), money.CNY)
	if err != nil || ok {
		t.Fatalf("未配置套餐 ResetPrice ok=%v err=%v", ok, err)
	}

	_, err = e.svc.ReplacePlanResetPrices(ctx, plan.ID, []ResetPrice{})
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err = e.svc.ResetPrice(ctx, plan.ID, money.CNY)
	if err != nil || ok {
		t.Fatalf("清空后 ResetPrice ok=%v err=%v", ok, err)
	}
}

func TestReplacePlanResetPricesValidationPG(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})

	cases := []struct {
		sp string
		in []ResetPrice
	}{
		{"zero", []ResetPrice{{Currency: money.CNY, UnitAmount: decimal.Zero}}},
		{"prec", []ResetPrice{{Currency: money.CNY, UnitAmount: decimal.RequireFromString("1.001")}}},
		{"dup", []ResetPrice{
			{Currency: money.CNY, UnitAmount: decimal.RequireFromString("1.00")},
			{Currency: money.CNY, UnitAmount: decimal.RequireFromString("2.00")},
		}},
		{"cur", []ResetPrice{{Currency: money.Currency("EUR"), UnitAmount: decimal.RequireFromString("1.00")}}},
	}
	for _, tc := range cases {
		err := inSavepoint(t, ctx, e, "rp_val_"+tc.sp, func() error {
			_, err := e.svc.ReplacePlanResetPrices(ctx, plan.ID, tc.in)
			return err
		})
		if errCode(err) != errs.ValidationFailed {
			t.Fatalf("%s: 期望 validation_failed，得到 %v", tc.sp, err)
		}
	}
}

func TestPlanResetPricesNotFoundPG(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	missing := uuid.New()

	_, err := e.svc.PlanResetPrices(ctx, missing)
	if errCode(err) != errs.NotFound {
		t.Fatalf("PlanResetPrices: %v", err)
	}
	_, err = e.svc.ReplacePlanResetPrices(ctx, missing, []ResetPrice{
		{Currency: money.CNY, UnitAmount: decimal.RequireFromString("1.00")},
	})
	if errCode(err) != errs.NotFound {
		t.Fatalf("ReplacePlanResetPrices: %v", err)
	}
}
