package service

// 套餐绑池与发布门禁的 PG 集成测试（Catalog 端口用假实现）。

import (
	"context"
	"testing"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

type poolDeliveryFixture struct {
	planID uuid.UUID
	p1     uuid.UUID
	p2     uuid.UUID
	p3     uuid.UUID
	p4     uuid.UUID
	p5     uuid.UUID
	poolA  uuid.UUID
	poolB  uuid.UUID
	poolC  uuid.UUID
	poolD  uuid.UUID
}

// seedPoolDeliveryFixture 建池 A–D 与节点夹具。
func seedPoolDeliveryFixture(t *testing.T, ctx context.Context, e *poolEnv) poolDeliveryFixture {
	t.Helper()
	fx := poolDeliveryFixture{
		planID: uuid.New(),
		p1:     uuid.New(),
		p2:     uuid.New(),
		p3:     uuid.New(),
		p4:     uuid.New(),
		p5:     uuid.New(),
	}
	fx.poolA = seedPoolRow(t, ctx, e, PoolActive)
	fx.poolB = seedPoolRow(t, ctx, e, PoolActive)
	fx.poolC = seedPoolRow(t, ctx, e, PoolDisabled)
	fx.poolD = seedPoolRow(t, ctx, e, PoolActive)

	s1 := seedPoolServer(t, ctx, e, "ready", true)
	s2 := seedPoolServer(t, ctx, e, "ready", false)
	s3 := seedPoolServer(t, ctx, e, PoolDraining, true)

	seedPoolNode(t, ctx, e, fx.poolA, &s1, "active", 443)
	seedPoolNode(t, ctx, e, fx.poolA, &s2, "active", 444)
	seedPoolNode(t, ctx, e, fx.poolA, &s1, "draft", 0)
	n4 := seedPoolNode(t, ctx, e, fx.poolA, &s1, "active", 445)
	deletePoolNode(t, ctx, e, n4)

	seedPoolNode(t, ctx, e, fx.poolB, &s3, "active", 446)
	seedPoolNode(t, ctx, e, fx.poolB, &s1, "active", 447)

	seedPoolNode(t, ctx, e, fx.poolC, &s1, "active", 448)

	return fx
}

func poolIDsInResult(pools []PlanPoolOption) []uuid.UUID {
	ids := make([]uuid.UUID, len(pools))
	for i, p := range pools {
		ids[i] = p.ID
	}
	return ids
}

func poolContainsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func poolOptionByID(pools []PlanPoolOption, id uuid.UUID) (PlanPoolOption, bool) {
	for _, p := range pools {
		if p.ID == id {
			return p, true
		}
	}
	return PlanPoolOption{}, false
}

func TestPlanPoolsCountsAndBinding(t *testing.T) {
	e, ctx := newPoolEnv(t)
	fx := seedPoolDeliveryFixture(t, ctx, e)

	// 套餐不在 plans 里应报 NotFound
	if _, err := e.svc.PlanPools(ctx, fx.planID); poolErrCode(err) != errs.NotFound {
		t.Fatalf("套餐不存在期望 NotFound，得到 %v", err)
	}

	e.cat.plans[fx.planID] = catalogsvc.Plan{ID: fx.planID, Name: "测试套餐"}
	e.cat.planPools[fx.planID] = []uuid.UUID{fx.poolB}

	got, err := e.svc.PlanPools(ctx, fx.planID)
	if err != nil {
		t.Fatal(err)
	}

	listed := poolIDsInResult(got.Pools)
	if poolContainsID(listed, fx.poolC) {
		t.Fatal("停用池 C 不应出现在可选列表")
	}
	for _, id := range []uuid.UUID{fx.poolA, fx.poolB, fx.poolD} {
		if !poolContainsID(listed, id) {
			t.Fatalf("缺少池 %v，列表=%v", id, listed)
		}
	}

	a, ok := poolOptionByID(got.Pools, fx.poolA)
	if !ok || a.ActiveNodes != 2 || a.DeliverableNodes != 1 || a.Bound {
		t.Fatalf("池 A: %+v", a)
	}
	b, ok := poolOptionByID(got.Pools, fx.poolB)
	if !ok || b.ActiveNodes != 2 || b.DeliverableNodes != 1 || !b.Bound {
		t.Fatalf("池 B: %+v", b)
	}
	d, ok := poolOptionByID(got.Pools, fx.poolD)
	if !ok || d.ActiveNodes != 0 || d.DeliverableNodes != 0 || d.Bound {
		t.Fatalf("池 D: %+v", d)
	}

	e.cat.planPools[fx.planID] = []uuid.UUID{fx.poolB, fx.poolC}
	got2, err := e.svc.PlanPools(ctx, fx.planID)
	if err != nil {
		t.Fatal(err)
	}
	if !poolContainsID(poolIDsInResult(got2.Pools), fx.poolC) {
		t.Fatal("binding 含 C 后 C 应出现在列表")
	}
	c, ok := poolOptionByID(got2.Pools, fx.poolC)
	if !ok || !c.Bound {
		t.Fatalf("池 C 应 Bound: %+v", c)
	}
}

func TestDeliverableMatchesServingNodeCounts(t *testing.T) {
	e, ctx := newPoolEnv(t)
	fx := seedPoolDeliveryFixture(t, ctx, e)

	e.cat.plans[fx.planID] = catalogsvc.Plan{ID: fx.planID}
	e.cat.planPools = map[uuid.UUID][]uuid.UUID{
		fx.p1: {fx.poolA, fx.poolB},
		fx.p2: {fx.poolC},
		fx.p3: {},
	}

	pp, err := e.svc.PlanPools(ctx, fx.planID)
	if err != nil {
		t.Fatal(err)
	}
	var sumAB int64
	for _, id := range []uuid.UUID{fx.poolA, fx.poolB} {
		p, ok := poolOptionByID(pp.Pools, id)
		if !ok {
			t.Fatalf("PlanPools 缺池 %v", id)
		}
		sumAB += p.DeliverableNodes
	}
	if sumAB != 2 {
		t.Fatalf("A+B deliverable 期望 2，得到 %d", sumAB)
	}

	counts, err := e.svc.ServingNodeCounts(ctx, []uuid.UUID{fx.p1, fx.p2, fx.p3, fx.p4})
	if err != nil {
		t.Fatal(err)
	}
	if counts[fx.p1] != sumAB {
		t.Fatalf("p1 期望 %d，得到 %d", sumAB, counts[fx.p1])
	}
	if counts[fx.p2] != 1 {
		t.Fatalf("p2 期望 1，得到 %d", counts[fx.p2])
	}
	if counts[fx.p3] != 0 || counts[fx.p4] != 0 {
		t.Fatalf("p3/p4 期望 0，得到 p3=%d p4=%d", counts[fx.p3], counts[fx.p4])
	}
	for _, pid := range []uuid.UUID{fx.p1, fx.p2, fx.p3, fx.p4} {
		if _, ok := counts[pid]; !ok {
			t.Fatalf("缺少套餐 %v 的计数", pid)
		}
	}
}

func TestCheckPublishable(t *testing.T) {
	e, ctx := newPoolEnv(t)
	fx := seedPoolDeliveryFixture(t, ctx, e)

	e.cat.planPools = map[uuid.UUID][]uuid.UUID{
		fx.p1: {fx.poolA},
		fx.p2: {},
		fx.p3: {fx.poolC},
		fx.p4: {fx.poolD},
		fx.p5: {fx.poolB},
	}

	if err := e.svc.CheckPublishable(ctx, fx.p1); err != nil {
		t.Fatalf("p1→[A] 应通过: %v", err)
	}

	err := e.svc.CheckPublishable(ctx, fx.p2)
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("p2 空绑池期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] != poolNoteUnbound {
		t.Fatalf("p2 fields: %v", poolErrFields(err))
	}

	err = e.svc.CheckPublishable(ctx, fx.p3)
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("p3→[C] 期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] != poolNoteAllDisabled {
		t.Fatalf("p3 fields: %v", poolErrFields(err))
	}

	err = e.svc.CheckPublishable(ctx, fx.p4)
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("p4→[D] 期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] != "绑定池中至少需要一个可服务节点" {
		t.Fatalf("p4 fields: %v", poolErrFields(err))
	}

	if err := e.svc.CheckPublishable(ctx, fx.p5); err != nil {
		t.Fatalf("p5→[B] 应通过: %v", err)
	}
}

func TestLockAssignablePools(t *testing.T) {
	e, ctx := newPoolEnv(t)
	fx := seedPoolDeliveryFixture(t, ctx, e)

	if err := e.svc.LockAssignablePools(ctx, []uuid.UUID{fx.poolA, fx.poolB}); err != nil {
		t.Fatal(err)
	}

	err := e.svc.LockAssignablePools(ctx, []uuid.UUID{fx.poolA, fx.poolC})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("含停用池 C 期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] == "" {
		t.Fatal("fields.pool_ids 应有文案")
	}

	random := uuid.New()
	err = e.svc.LockAssignablePools(ctx, []uuid.UUID{fx.poolA, random})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("含随机 id 期望 422，得到 %v", err)
	}

	if err := e.svc.LockAssignablePools(ctx, nil); err != nil {
		t.Fatalf("空列表期望 nil: %v", err)
	}
}
