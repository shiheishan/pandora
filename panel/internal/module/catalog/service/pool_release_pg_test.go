package service

// 归档套餐的线路绑定不占池（PoolUsage 排除归档）、删池时 ReleaseArchivedPoolBindings 的 PG 集成测试。

import (
	"testing"

	"github.com/google/uuid"
)

func TestPoolUsageExcludesArchivedPlans(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p := seedPool(t, ctx, e, "active")

	a := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "在售套餐A",
		PoolIDs: []uuid.UUID{p},
	})
	b := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "归档套餐B",
		PoolIDs: []uuid.UUID{p},
	})
	bDetail, err := e.svc.GetPlan(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ArchivePlan(ctx, b.ID, bDetail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}

	usage, err := e.svc.PoolUsage(ctx, []uuid.UUID{p})
	if err != nil {
		t.Fatal(err)
	}
	u, ok := usage[p]
	if !ok {
		t.Fatal("池 P 应在 PoolUsage map 里")
	}
	if u.Plans != 1 {
		t.Fatalf("Plans 期望 1（仅未归档 A），得到 %d", u.Plans)
	}
	if len(u.PlanNames) != 1 || u.PlanNames[0] != a.Name {
		t.Fatalf("PlanNames 期望 [%s]，得到 %v", a.Name, u.PlanNames)
	}
	if len(u.Bindings) != 2 {
		t.Fatalf("Bindings 期望 2，得到 %d", len(u.Bindings))
	}
	var foundA, foundB bool
	for _, bind := range u.Bindings {
		if bind.PlanID == a.ID {
			foundA = true
			if bind.Archived {
				t.Fatal("在售套餐 A 的 Archived 应为 false")
			}
		}
		if bind.PlanID == b.ID {
			foundB = true
			if !bind.Archived {
				t.Fatal("归档套餐 B 的 Archived 应为 true")
			}
		}
	}
	if !foundA || !foundB {
		t.Fatal("Bindings 应同时包含 A 与 B")
	}

	q := seedPool(t, ctx, e, "active")
	c := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "仅归档套餐C",
		PoolIDs: []uuid.UUID{q},
	})
	cDetail, err := e.svc.GetPlan(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ArchivePlan(ctx, c.ID, cDetail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}

	usageQ, err := e.svc.PoolUsage(ctx, []uuid.UUID{q})
	if err != nil {
		t.Fatal(err)
	}
	uQ, ok := usageQ[q]
	if !ok {
		t.Fatal("只被归档套餐绑定的池 Q 仍应在 map 里")
	}
	if uQ.Plans != 0 {
		t.Fatalf("池 Q Plans 期望 0，得到 %d", uQ.Plans)
	}
	if len(uQ.PlanNames) != 0 {
		t.Fatalf("池 Q PlanNames 期望空，得到 %v", uQ.PlanNames)
	}
	if len(uQ.Bindings) != 1 {
		t.Fatalf("池 Q Bindings 期望 1，得到 %d", len(uQ.Bindings))
	}
	if !uQ.Bindings[0].Archived {
		t.Fatal("池 Q 唯一绑定应为归档套餐")
	}
}

func TestReleaseArchivedPoolBindings(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p := seedPool(t, ctx, e, "active")

	planA := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "在售A",
		PoolIDs: []uuid.UUID{p},
	})
	planD := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "草稿D",
		Draft:   true,
		PoolIDs: []uuid.UUID{p},
	})
	planB := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "归档B",
		PoolIDs: []uuid.UUID{p},
	})
	bDetail, err := e.svc.GetPlan(ctx, planB.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ArchivePlan(ctx, planB.ID, bDetail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}

	ids, err := e.svc.ReleaseArchivedPoolBindings(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != planB.ID {
		t.Fatalf("ReleaseArchivedPoolBindings 期望 [%s]，得到 %v", planB.ID, ids)
	}

	n := scanInt64(t, ctx, e,
		`SELECT COUNT(*)::bigint FROM plan_node_pools WHERE pool_id = ?`, p)
	if n != 2 {
		t.Fatalf("删归档绑定后 plan_node_pools 行数期望 2，得到 %d", n)
	}

	m, err := e.svc.PlanPools(ctx, []uuid.UUID{planA.ID, planD.ID, planB.ID})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, m[planA.ID], []uuid.UUID{p})
	poolEqualIDs(t, m[planD.ID], []uuid.UUID{p})
	if _, ok := m[planB.ID]; ok {
		t.Fatal("归档套餐 B 的绑定应已删除")
	}

	ids2, err := e.svc.ReleaseArchivedPoolBindings(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if ids2 == nil {
		t.Fatal("第二次调用应返回非 nil 空切片")
	}
	if len(ids2) != 0 {
		t.Fatalf("第二次调用期望空切片，得到 %v", ids2)
	}
}
