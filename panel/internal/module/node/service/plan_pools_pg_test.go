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
	v      uuid.UUID
	v1     uuid.UUID
	v2     uuid.UUID
	v3     uuid.UUID
	v4     uuid.UUID
	v5     uuid.UUID
	poolA  uuid.UUID
	poolB  uuid.UUID
	poolC  uuid.UUID
	poolD  uuid.UUID
}

// seedPoolDeliveryFixture 建池 A–D 与节点夹具（见任务说明）。
func seedPoolDeliveryFixture(t *testing.T, ctx context.Context, e *poolEnv) poolDeliveryFixture {
	t.Helper()
	fx := poolDeliveryFixture{
		planID: uuid.New(),
		v:      uuid.New(),
		v1:     uuid.New(),
		v2:     uuid.New(),
		v3:     uuid.New(),
		v4:     uuid.New(),
		v5:     uuid.New(),
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

	e.cat.binding = catalogsvc.PoolBindingVersion{
		VersionID:  &fx.v,
		Status:     "draft",
		RowVersion: 3,
		Editable:   true,
		PoolIDs:    []uuid.UUID{fx.poolB},
	}

	got, err := e.svc.PlanPools(ctx, fx.planID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionID == nil || *got.VersionID != fx.v {
		t.Fatalf("VersionID 期望 %v，得到 %v", fx.v, got.VersionID)
	}
	if got.VersionStatus != "draft" || got.RowVersion != 3 || !got.Editable {
		t.Fatalf("元数据不符: status=%q rv=%d editable=%v", got.VersionStatus, got.RowVersion, got.Editable)
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

	e.cat.binding.PoolIDs = []uuid.UUID{fx.poolB, fx.poolC}
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

	e.cat.versionPools = map[uuid.UUID][]uuid.UUID{
		fx.v1: {fx.poolA, fx.poolB},
		fx.v2: {fx.poolC},
		fx.v3: {},
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

	counts, err := e.svc.ServingNodeCounts(ctx, []uuid.UUID{fx.v1, fx.v2, fx.v3, fx.v4})
	if err != nil {
		t.Fatal(err)
	}
	if counts[fx.v1] != sumAB {
		t.Fatalf("v1 期望 %d，得到 %d", sumAB, counts[fx.v1])
	}
	if counts[fx.v2] != 1 {
		t.Fatalf("v2 期望 1，得到 %d", counts[fx.v2])
	}
	if counts[fx.v3] != 0 || counts[fx.v4] != 0 {
		t.Fatalf("v3/v4 期望 0，得到 v3=%d v4=%d", counts[fx.v3], counts[fx.v4])
	}
	for _, vid := range []uuid.UUID{fx.v1, fx.v2, fx.v3, fx.v4} {
		if _, ok := counts[vid]; !ok {
			t.Fatalf("缺少版本 %v 的计数", vid)
		}
	}
}

func TestCheckPublishable(t *testing.T) {
	e, ctx := newPoolEnv(t)
	fx := seedPoolDeliveryFixture(t, ctx, e)

	e.cat.versionPools = map[uuid.UUID][]uuid.UUID{
		fx.v1: {fx.poolA},
		fx.v2: {},
		fx.v3: {fx.poolC},
		fx.v4: {fx.poolD},
		fx.v5: {fx.poolB},
	}

	if err := e.svc.CheckPublishable(ctx, fx.v1); err != nil {
		t.Fatalf("v1→[A] 应通过: %v", err)
	}

	err := e.svc.CheckPublishable(ctx, fx.v2)
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("v2 空绑池期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] != poolNoteUnbound {
		t.Fatalf("v2 fields: %v", poolErrFields(err))
	}

	err = e.svc.CheckPublishable(ctx, fx.v3)
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("v3→[C] 期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] != poolNoteAllDisabled {
		t.Fatalf("v3 fields: %v", poolErrFields(err))
	}

	err = e.svc.CheckPublishable(ctx, fx.v4)
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("v4→[D] 期望 422，得到 %v", err)
	}
	if poolErrFields(err)["pool_ids"] != "绑定池中至少需要一个可服务节点" {
		t.Fatalf("v4 fields: %v", poolErrFields(err))
	}

	if err := e.svc.CheckPublishable(ctx, fx.v5); err != nil {
		t.Fatalf("v5→[B] 应通过: %v", err)
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

func TestSetPlanPools(t *testing.T) {
	e, ctx := newPoolEnv(t)
	fx := seedPoolDeliveryFixture(t, ctx, e)
	e.cat.setCalls = 0

	before := poolEpoch(t, e)
	next, err := e.svc.SetPlanPools(ctx, fx.planID, fx.v, 3, []uuid.UUID{fx.poolA, fx.poolB})
	if err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Fatalf("row_version 期望 4，得到 %d", next)
	}
	if e.cat.setCalls != 1 {
		t.Fatalf("SetVersionPools 调用次数期望 1，得到 %d", e.cat.setCalls)
	}
	if poolEpoch(t, e) != before {
		t.Fatal("SetPlanPools 不应推进 users_epoch")
	}

	e.cat.setCalls = 0
	_, err = e.svc.SetPlanPools(ctx, fx.planID, fx.v, 3, []uuid.UUID{fx.poolA, fx.poolC})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("含停用池期望 422，得到 %v", err)
	}
	if e.cat.setCalls != 0 {
		t.Fatalf("校验失败时不应调用 catalog，calls=%d", e.cat.setCalls)
	}

	e.cat.setErr = errs.Newf(errs.Conflict, "x")
	e.cat.setCalls = 0
	_, err = e.svc.SetPlanPools(ctx, fx.planID, fx.v, 3, []uuid.UUID{fx.poolA})
	if poolErrCode(err) != errs.Conflict {
		t.Fatalf("catalog 冲突期望 409，得到 %v", err)
	}

	many := make([]uuid.UUID, MaxPlanPools+1)
	for i := range many {
		many[i] = uuid.New()
	}
	err = poolSavepoint(t, ctx, e, func() error {
		_, err := e.svc.SetPlanPools(ctx, fx.planID, fx.v, 3, many)
		return err
	})
	if poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("超过 500 个 id 期望 422，得到 %v", err)
	}
}
