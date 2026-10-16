package service

// 目录后台分块写用例的 PG 测试：用量、价格、重置、基本信息、销售。

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type editFakePoolGuard struct{}

func (editFakePoolGuard) LockAssignablePools(context.Context, []uuid.UUID) error { return nil }
func (editFakePoolGuard) MarkUsersChanged(context.Context)                       {}

func TestUpdateUsageDraftVersioning(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	curVer := *detail.CurrentVersionID

	traffic := int64(20 << 30)
	after1, err := e.svc.UpdateUsage(ctx, plan.ID, UsageUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Usage:              UsageInput{TrafficBytes: &traffic},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after1.DraftVersionID == nil || after1.CurrentVersionID == nil || *after1.CurrentVersionID != curVer {
		t.Fatalf("第一次改用量: draft=%v current=%v", after1.DraftVersionID, after1.CurrentVersionID)
	}
	draft1 := *after1.DraftVersionID
	if after1.DraftVersion == nil || *after1.DraftVersion != 2 {
		t.Fatalf("草稿版本号=%v", after1.DraftVersion)
	}

	traffic2 := int64(25 << 30)
	after2, err := e.svc.UpdateUsage(ctx, plan.ID, UsageUpdate{
		ExpectedRowVersion: after1.RowVersion,
		Usage:              UsageInput{TrafficBytes: &traffic2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after2.DraftVersionID == nil || *after2.DraftVersionID != draft1 {
		t.Fatalf("应复用同一草稿: %v vs %v", after2.DraftVersionID, draft1)
	}

	pub, err := e.svc.PublishVersion(ctx, plan.ID, draft1, after2.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if pub.CurrentVersionID == nil || *pub.CurrentVersionID != draft1 {
		t.Fatal("发布后 current 应为版本 2")
	}
	if pub.DraftVersionID != nil {
		t.Fatalf("发布后不应有草稿: %v", pub.DraftVersionID)
	}
	if pub.Version == nil || *pub.Version != 2 {
		t.Fatalf("version=%v", pub.Version)
	}
}

func TestUpdatePricesSubscription(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	amt := decimal.RequireFromString("12.90")
	updated, err := e.svc.UpdatePrices(ctx, plan.ID, PricesUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Tiers: []TierChange{{
			BillingInterval: IntervalMonth,
			IntervalCount:   1,
			UnitAmount:      &amt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RowVersion != detail.RowVersion+1 {
		t.Fatalf("row_version=%d", updated.RowVersion)
	}
	var activeMonth int
	var archived int
	for _, p := range updated.Prices {
		if p.BillingInterval == IntervalMonth && p.IntervalCount == 1 {
			if p.Status == PriceActive {
				activeMonth++
				if !p.UnitAmount.Equal(amt) {
					t.Fatalf("金额 %s", p.UnitAmount)
				}
			}
			if p.Status == PriceArchived {
				archived++
			}
		}
	}
	if activeMonth != 1 || archived < 1 {
		t.Fatalf("active=%d archived=%d", activeMonth, archived)
	}

	// 在售套餐下架唯一一档应 422
	nilAmt := (*decimal.Decimal)(nil)
	_, err = e.svc.UpdatePrices(ctx, plan.ID, PricesUpdate{
		ExpectedRowVersion: updated.RowVersion,
		Tiers:              []TierChange{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: nilAmt}},
	})
	if errCode(err) != errs.ValidationFailed || errFields(err)["prices"] == "" {
		t.Fatalf("在售至少一档: %v fields=%v", err, errFields(err))
	}
}

func TestUpdatePricesOneTime(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{Kind: KindOneTime})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	days := 60
	amt := decimal.RequireFromString("29.90")
	updated, err := e.svc.UpdatePrices(ctx, plan.ID, PricesUpdate{
		ExpectedRowVersion: detail.RowVersion,
		OneTime:            &OneTimeInput{UnitAmount: amt, ValidityDays: &days},
	})
	if err != nil {
		t.Fatal(err)
	}
	var active Price
	for _, p := range updated.Prices {
		if p.Status == PriceActive {
			active = p
		}
	}
	if !active.UnitAmount.Equal(amt) || active.ValidityDays == nil || *active.ValidityDays != 60 {
		t.Fatalf("价格 %+v", active)
	}

	_, err = e.svc.UpdatePrices(ctx, plan.ID, PricesUpdate{
		ExpectedRowVersion: updated.RowVersion,
		Tiers:              []TierChange{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: &amt}},
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("一次性传 Tiers: %v", err)
	}
}

func TestUpdateReset(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	on, err := e.svc.UpdateReset(ctx, plan.ID, ResetUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Enabled:            true,
		Price:              decimal.RequireFromString("5.00"),
		Limit:              intPtr(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if on.ResetPrice == nil || !on.ResetPrice.Equal(decimal.RequireFromString("5.00")) || on.ResetLimit == nil || *on.ResetLimit != 2 {
		t.Fatalf("reset %+v %+v", on.ResetPrice, on.ResetLimit)
	}

	off, err := e.svc.UpdateReset(ctx, plan.ID, ResetUpdate{
		ExpectedRowVersion: on.RowVersion,
		Enabled:            false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if off.ResetPrice != nil || off.ResetLimit != nil {
		t.Fatalf("关闭后 reset_price=%v limit=%v", off.ResetPrice, off.ResetLimit)
	}

	_, err = e.svc.UpdateReset(ctx, plan.ID, ResetUpdate{
		ExpectedRowVersion: off.RowVersion,
		Enabled:            true,
		Price:              decimal.Zero,
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("price 0: %v", err)
	}
}

func intPtr(n int) *int { return &n }

func TestUpdateBasicKeepsCode(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	detail := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "原名套餐"})
	code := detail.Code

	updated, err := e.svc.UpdateBasic(ctx, detail.ID, BasicUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Name:               "新显示名",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "新显示名" || updated.Code != code {
		t.Fatalf("name=%s code=%s", updated.Name, updated.Code)
	}
}

func TestUpdateSalesGroupAndOneTimeRenewal(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := seedGroup(t, ctx, e, "")

	sub := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, sub.ID)
	updated, err := e.svc.UpdateSales(ctx, sub.ID, SalesUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Sales: SalesInput{
			Visibility:       VisibilityGroup,
			VisibleGroupIDs:  []uuid.UUID{g},
			AllowNewPurchase: true,
			AllowRenewal:     true,
			AllowUpgrade:     true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.VisibleGroupIDs) != 1 || updated.VisibleGroupIDs[0] != g {
		t.Fatalf("可见组 %v", updated.VisibleGroupIDs)
	}

	ot := publishedPlan(t, ctx, e, publishedPlanOpts{Kind: KindOneTime})
	otDetail, _ := e.svc.GetPlan(ctx, ot.ID)
	_, err = e.svc.UpdateSales(ctx, ot.ID, SalesUpdate{
		ExpectedRowVersion: otDetail.RowVersion,
		Sales: SalesInput{
			Visibility:       VisibilityPublic,
			AllowNewPurchase: true,
			AllowRenewal:     true,
			AllowUpgrade:     true,
		},
	})
	if errCode(err) != errs.ValidationFailed || errFields(err)["allow_renewal"] == "" {
		t.Fatalf("一次性续费: %v", err)
	}
}

func TestSetPlanPoolsOptimisticLock(t *testing.T) {
	e, ctx := newEnv(t)
	e.svc.UsePoolGuard(editFakePoolGuard{})
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	pool := seedPool(t, ctx, e, "active")

	_, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion + 1,
		PoolIDs:            []uuid.UUID{pool},
	})
	assertRowVersionConflict(t, err)

	updated, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{pool},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RowVersion != detail.RowVersion+1 {
		t.Fatalf("row_version=%d", updated.RowVersion)
	}
}
