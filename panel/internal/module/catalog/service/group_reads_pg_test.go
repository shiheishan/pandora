package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestGroupRefCountsPG(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	g1 := seedGroup(t, ctx, e, "")
	g2 := seedGroup(t, ctx, e, "")
	g3 := seedGroup(t, ctx, e, "")

	_ = publishedPlan(t, ctx, e, publishedPlanOpts{
		Visibility:      VisibilityGroup,
		VisibleGroupIDs: []uuid.UUID{g1},
	})
	plan2 := publishedPlan(t, ctx, e, publishedPlanOpts{
		Visibility:      VisibilityGroup,
		VisibleGroupIDs: []uuid.UUID{g1, g2},
	})

	_, err := e.svc.CreatePrice(ctx, plan2.ID, PriceInput{
		Currency:        "CNY",
		UnitAmount:      decimal.RequireFromString("8.00"),
		BillingInterval: "month",
		IntervalCount:   1,
		UserGroupID:     &g1,
	})
	if err != nil {
		t.Fatal(err)
	}

	counts, err := e.svc.GroupRefCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	assertRefCount(t, counts, g1, 2, 1)
	assertRefCount(t, counts, g2, 1, 0)
	if _, ok := counts[g3]; ok {
		t.Fatalf("未引用的组 %s 不应出现在结果里", g3)
	}
}

func assertRefCount(t *testing.T, counts map[uuid.UUID]GroupRefCount, gid uuid.UUID, plans, prices int64) {
	t.Helper()
	c, ok := counts[gid]
	if !ok {
		t.Fatalf("组 %s 应在结果里", gid)
	}
	if c.Plans != plans || c.Prices != prices {
		t.Fatalf("组 %s: 期望 plans=%d prices=%d，得到 plans=%d prices=%d", gid, plans, prices, c.Plans, c.Prices)
	}
}
