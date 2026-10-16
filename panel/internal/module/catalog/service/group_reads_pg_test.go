package service

import (
	"testing"

	"github.com/google/uuid"
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
	_ = publishedPlan(t, ctx, e, publishedPlanOpts{
		Visibility:      VisibilityGroup,
		VisibleGroupIDs: []uuid.UUID{g1, g2},
	})

	counts, err := e.svc.GroupRefCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	assertRefCount(t, counts, g1, 2)
	assertRefCount(t, counts, g2, 1)
	if _, ok := counts[g3]; ok {
		t.Fatalf("未引用的组 %s 不应出现在结果里", g3)
	}
}

func assertRefCount(t *testing.T, counts map[uuid.UUID]GroupRefCount, gid uuid.UUID, plans int64) {
	t.Helper()
	c, ok := counts[gid]
	if !ok {
		t.Fatalf("组 %s 应在结果里", gid)
	}
	if c.Plans != plans {
		t.Fatalf("组 %s: 期望 plans=%d，得到 plans=%d", gid, plans, c.Plans)
	}
}
