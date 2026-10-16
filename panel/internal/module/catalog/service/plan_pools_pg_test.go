package service

// 套餐绑池（plan_node_pools）的 PG 集成测试。PoolGuard 用假实现（真实现是 node 模块）。

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type fakePoolGuard struct {
	calls       [][]uuid.UUID
	markChanged int
	err         error
}

func (f *fakePoolGuard) LockAssignablePools(_ context.Context, ids []uuid.UUID) error {
	cp := append([]uuid.UUID(nil), ids...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].String() < cp[j].String() })
	f.calls = append(f.calls, cp)
	return f.err
}

func (f *fakePoolGuard) MarkUsersChanged(_ context.Context) {
	f.markChanged++
}

func seedPool(t *testing.T, ctx context.Context, e *env, status string) uuid.UUID {
	t.Helper()
	code := uniqCode(t, "p")
	name := "池 " + code
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO node_pools (code, name, status) VALUES (?, ?, ?) RETURNING id`, code, name, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func poolAssertSorted(t *testing.T, ids []uuid.UUID) {
	t.Helper()
	for i := 1; i < len(ids); i++ {
		if ids[i-1].String() >= ids[i].String() {
			t.Fatalf("池 id 未按 String() 升序: %v", ids)
		}
	}
}

func poolEqualIDs(t *testing.T, got, want []uuid.UUID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("池 id 数量 %d vs %d: got=%v want=%v", len(got), len(want), got, want)
	}
	poolAssertSorted(t, got)
	g := append([]uuid.UUID(nil), got...)
	w := append([]uuid.UUID(nil), want...)
	sort.Slice(g, func(i, j int) bool { return g[i].String() < g[j].String() })
	sort.Slice(w, func(i, j int) bool { return w[i].String() < w[j].String() })
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("池 id 集合不等: got=%v want=%v", got, want)
		}
	}
}

func poolCountAuditPlanPools(t *testing.T, ctx context.Context, e *env, planID uuid.UUID) int64 {
	t.Helper()
	return scanInt64(t, ctx, e,
		`SELECT COUNT(*)::bigint FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionPlanPools, planID)
}

func TestSetPlanPoolsDraftAndPublished(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "已发布绑池"})
	p1 := seedPool(t, ctx, e, "active")
	p2 := seedPool(t, ctx, e, "active")

	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	rv := detail.RowVersion
	// 建套餐时写线路也会推进一次名单纪元；这里只数改线路这一次
	markBefore := g.markChanged
	upd, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: rv,
		PoolIDs:            []uuid.UUID{p2, p1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.RowVersion != rv+1 {
		t.Fatalf("row_version 期望 %d，得到 %d", rv+1, upd.RowVersion)
	}
	poolEqualIDs(t, upd.PoolIDs, []uuid.UUID{p1, p2})
	if len(g.calls) != 1 || len(g.calls[0]) != 2 {
		t.Fatalf("guard 锁池调用: %v", g.calls)
	}
	if n := g.markChanged - markBefore; n != 1 {
		t.Fatalf("MarkUsersChanged 期望 1，得到 %d", n)
	}
	if n := poolCountAuditPlanPools(t, ctx, e, plan.ID); n != 1 {
		t.Fatalf("审计 plan.pools_update 期望 1，得到 %d", n)
	}

	draft := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "草稿绑池", Draft: true})
	p3 := seedPool(t, ctx, e, "active")
	detail, _ = e.svc.GetPlan(ctx, draft.ID)
	_, err = e.svc.SetPlanPools(ctx, draft.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p3},
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.PlanPools(ctx, []uuid.UUID{draft.ID})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, m[draft.ID], []uuid.UUID{p3})
}

func TestSetPlanPoolsStaleRowVersion(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	p1 := seedPool(t, ctx, e, "active")
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	_, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p1},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p1},
	})
	if errCode(err) != errs.Conflict {
		t.Fatalf("陈旧 row_version 期望 409，得到 %v", err)
	}
	if errFields(err)["row_version"] == "" {
		t.Fatal("fields 缺 row_version")
	}

	_, err = e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: 0,
		PoolIDs:            []uuid.UUID{p1},
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("expectedRowVersion=0 期望 422，得到 %v", err)
	}
}

func TestSetPlanPoolsArchivedRejected(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "归档绑池"})
	p1 := seedPool(t, ctx, e, "active")
	rv := plan.RowVersion
	_, err := e.svc.ArchivePlan(ctx, plan.ID, rv)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	_, err = e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p1},
	})
	if !errors.Is(err, ErrPlanArchived) {
		t.Fatalf("归档套餐改线路期望 ErrPlanArchived，得到 %v", err)
	}
}

func TestSetPlanPoolsUnknownPlan(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)
	p1 := seedPool(t, ctx, e, "active")

	_, err := e.svc.SetPlanPools(ctx, uuid.New(), PoolsUpdate{
		ExpectedRowVersion: 1,
		PoolIDs:            []uuid.UUID{p1},
	})
	if errCode(err) != errs.NotFound {
		t.Fatalf("未知套餐期望 404，得到 %v", err)
	}
}

func TestSetPlanPoolsSameSetNoBump(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{PoolIDs: []uuid.UUID{p1}})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	beforeMark := g.markChanged
	beforeAudit := poolCountAuditPlanPools(t, ctx, e, plan.ID)

	upd, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.RowVersion != detail.RowVersion {
		t.Fatalf("相同集合不应推进 row_version: %d vs %d", upd.RowVersion, detail.RowVersion)
	}
	if g.markChanged != beforeMark {
		t.Fatal("相同集合不应调 MarkUsersChanged")
	}
	if poolCountAuditPlanPools(t, ctx, e, plan.ID) != beforeAudit {
		t.Fatal("相同集合不应写审计")
	}
}

func TestSetPlanPoolsClearAndGuardRollback(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{PoolIDs: []uuid.UUID{p1}})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	upd, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upd.PoolIDs) != 0 {
		t.Fatalf("清空后 PoolIDs=%v", upd.PoolIDs)
	}
	m, _ := e.svc.PlanPools(ctx, []uuid.UUID{plan.ID})
	if len(m) != 0 {
		t.Fatal("清空后 PlanPools 不应有该套餐")
	}

	g2 := &fakePoolGuard{err: errs.Invalid(map[string]string{"pool_ids": "不可用"})}
	e.svc.UsePoolGuard(g2)
	detail, _ = e.svc.GetPlan(ctx, plan.ID)
	_, err = e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p1},
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("guard 报错期望 422，得到 %v", err)
	}
	detail, _ = e.svc.GetPlan(ctx, plan.ID)
	if len(detail.PoolIDs) != 0 {
		t.Fatal("guard 失败应整笔回滚，线路仍为空")
	}
}

func TestSetPlanPoolsWithoutGuardUnavailable(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	p1 := seedPool(t, ctx, e, "active")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	_, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{p1},
	})
	if errCode(err) != errs.Unavailable {
		t.Fatalf("无 guard 非空 pool_ids 期望 503，得到 %v", err)
	}
}

func TestCreatePlanWithPools(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	pA := seedPool(t, ctx, e, "active")
	pB := seedPool(t, ctx, e, "active")

	detail, err := e.svc.CreatePlan(ctx, PlanCreate{
		Kind: KindSubscription,
		Name: "创建绑池",
		Tiers: []TierInput{{
			BillingInterval: IntervalMonth,
			IntervalCount:   1,
			UnitAmount:      decimal.RequireFromString("9.90"),
		}},
		Usage:   UsageInput{TrafficBytes: int64PtrPools(10 * 1024 * 1024 * 1024)},
		PoolIDs: []uuid.UUID{pB, pA},
		Sales: SalesInput{
			Visibility: VisibilityPublic, AllowNewPurchase: true,
			AllowRenewal: true, AllowUpgrade: true,
		},
		Publish: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, detail.PoolIDs, []uuid.UUID{pA, pB})
	if len(g.calls) != 1 {
		t.Fatalf("guard 调用次数 %d", len(g.calls))
	}
	poolEqualIDs(t, g.calls[0], []uuid.UUID{pA, pB})

}

// 用提交态：回滚态的用例整体在一个外层事务里，service 的 tx.Do 加入它而不是自己开事务，
// 出错时不会回滚已插的套餐行，测不出「整笔回滚」。
func TestCreatePlanWithoutGuardRollback(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, _ = seedAdmin(t, ctx, e)
	p1 := seedPool(t, ctx, e, "active")
	// t.Cleanup 后进先出，这里先于删套餐执行：回滚真失败时先拆掉套餐对池的绑定（RESTRICT），免得多报一条清理错误
	t.Cleanup(func() {
		if err := e.exec(context.Background(), `DELETE FROM plan_node_pools WHERE pool_id = ?`, p1); err != nil {
			t.Errorf("清理套餐线路: %v", err)
		}
		if err := e.exec(context.Background(), `DELETE FROM node_pools WHERE id = ?`, p1); err != nil {
			t.Errorf("清理节点池: %v", err)
		}
	})
	name := "无guard回滚" + uniqCode(t, "ng")
	_, err := e.svc.CreatePlan(ctx, PlanCreate{
		Kind: KindSubscription,
		Name: name,
		Tiers: []TierInput{{
			BillingInterval: IntervalMonth, IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("1.00"),
		}},
		Usage:   UsageInput{TrafficBytes: int64PtrPools(1 * 1024 * 1024 * 1024)},
		PoolIDs: []uuid.UUID{p1},
		Sales: SalesInput{
			Visibility: VisibilityPublic, AllowNewPurchase: true,
			AllowRenewal: true, AllowUpgrade: true,
		},
	})
	if errCode(err) != errs.Unavailable {
		t.Fatalf("无 guard 带池创建期望 503，得到 %v", err)
	}
	if n := scanInt64(t, ctx, e, `SELECT count(*) FROM plans WHERE name = ?`, name); n != 0 {
		var leaked uuid.UUID
		if err := e.row(ctx, `SELECT id FROM plans WHERE name = ?`, name).Scan(&leaked); err == nil {
			e.plans = append(e.plans, leaked)
		}
		t.Fatal("创建失败应整笔回滚，不留套餐行")
	}
}

func TestUpdateUsageKeepsPools(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{PoolIDs: []uuid.UUID{p1}})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	_, err := e.svc.UpdateUsage(ctx, plan.ID, UsageUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Usage:              UsageInput{TrafficBytes: int64PtrPools(20 * 1024 * 1024 * 1024)},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.GetPlan(ctx, plan.ID)
	poolEqualIDs(t, after.PoolIDs, []uuid.UUID{p1})
}

func int64PtrPools(n int64) *int64 { return &n }

func TestPlanPoolsBatch(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")
	p2 := seedPool(t, ctx, e, "active")
	a := publishedPlan(t, ctx, e, publishedPlanOpts{PoolIDs: []uuid.UUID{p2, p1}})
	b := publishedPlan(t, ctx, e, publishedPlanOpts{})

	m, err := e.svc.PlanPools(ctx, []uuid.UUID{a.ID, b.ID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, m[a.ID], []uuid.UUID{p1, p2})
	if _, ok := m[b.ID]; ok {
		t.Fatal("未绑池的套餐不应在 map 里")
	}
}

func TestPoolUsageOnSaleAndVersionIDs(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")

	active := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "在售套餐",
		PoolIDs: []uuid.UUID{p1},
	})
	v1 := *active.CurrentVersionID

	detail, _ := e.svc.GetPlan(ctx, active.ID)
	_, err := e.svc.UpdateUsage(ctx, active.ID, UsageUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Usage:              UsageInput{TrafficBytes: int64PtrPools(30 * 1024 * 1024 * 1024)},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ = e.svc.GetPlan(ctx, active.ID)
	_, err = e.svc.PublishVersion(ctx, active.ID, *detail.DraftVersionID, detail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ = e.svc.GetPlan(ctx, active.ID)
	v2 := *detail.CurrentVersionID

	draftPlan := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "草稿套餐",
		Draft:   true,
		PoolIDs: []uuid.UUID{p1},
	})

	arch := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:    "归档套餐",
		PoolIDs: []uuid.UUID{p1},
	})
	archDetail, _ := e.svc.GetPlan(ctx, arch.ID)
	_, _ = e.svc.ArchivePlan(ctx, arch.ID, archDetail.RowVersion)

	usage, err := e.svc.PoolUsage(ctx, []uuid.UUID{p1})
	if err != nil {
		t.Fatal(err)
	}
	u := usage[p1]
	if u.Plans != 3 {
		t.Fatalf("Plans 期望 3，得到 %d", u.Plans)
	}
	names := append([]string(nil), u.PlanNames...)
	sort.Strings(names)
	if len(names) != 3 {
		t.Fatalf("PlanNames=%v", names)
	}

	var activeBinding *PoolBinding
	for _, b := range u.Bindings {
		if b.PlanID == active.ID {
			activeBinding = &b
			break
		}
	}
	if activeBinding == nil {
		t.Fatal("缺在售套餐绑定")
	}
	if !activeBinding.OnSale {
		t.Fatal("active 且有当前版本 OnSale 应为 true")
	}
	if len(activeBinding.VersionIDs) != 2 {
		t.Fatalf("在售套餐应有两个版本 id，得到 %v", activeBinding.VersionIDs)
	}
	ids := append([]uuid.UUID(nil), activeBinding.VersionIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	want := []uuid.UUID{v1, v2}
	sort.Slice(want, func(i, j int) bool { return want[i].String() < want[j].String() })
	if ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("版本 id 期望 %v，得到 %v", want, ids)
	}

	for _, b := range u.Bindings {
		if b.PlanID == draftPlan.ID && b.OnSale {
			t.Fatal("草稿套餐 OnSale 应为 false")
		}
		if b.PlanID == arch.ID && b.OnSale {
			t.Fatal("归档套餐 OnSale 应为 false")
		}
	}
}

type fakePublishGate struct{ err error }

func (f fakePublishGate) CheckPublishable(context.Context, uuid.UUID) error { return f.err }

// 审查 1016-A1 应改 1：原来绑着的池后来停用了，改别的线路时它照样保留，guard 只检查新加的池。
func TestSetPlanPoolsKeepsDisabledExisting(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	old := seedPool(t, ctx, e, "active")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{PoolIDs: []uuid.UUID{old}})
	added := seedPool(t, ctx, e, "active")
	g.calls = nil

	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	upd, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{old, added},
	})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, upd.PoolIDs, []uuid.UUID{old, added})
	if len(g.calls) != 1 || len(g.calls[0]) != 1 || g.calls[0][0] != added {
		t.Fatalf("guard 只应检查新加的池 %s，调用 %v", added, g.calls)
	}
}

// 审查 1016-A1 应改 2：已发布的套餐改线路后要过上架检查；不过就整笔回滚，线路不变。
func TestSetPlanPoolsPublishedRunsGate(t *testing.T) {
	// 提交态：回滚态里 service 的 tx.Do 加入外层事务，出错时测不出整笔回滚（同 TestCreatePlanWithoutGuardRollback）
	e, ctx := newEnvCommitted(t)
	ctx, _ = seedAdmin(t, ctx, e)
	e.svc.UsePoolGuard(&fakePoolGuard{})

	p1 := seedPool(t, ctx, e, "active")
	// 后进先出，先于删套餐执行：先拆套餐对池的绑定（RESTRICT）再删池
	t.Cleanup(func() {
		if err := e.exec(context.Background(), `DELETE FROM plan_node_pools WHERE pool_id = ?`, p1); err != nil {
			t.Errorf("清理套餐线路: %v", err)
		}
		if err := e.exec(context.Background(), `DELETE FROM node_pools WHERE id = ?`, p1); err != nil {
			t.Errorf("清理节点池: %v", err)
		}
	})
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{PoolIDs: []uuid.UUID{p1}})
	e.svc.UsePublishGate(fakePublishGate{err: errs.Invalid(map[string]string{"pool_ids": "线路里没有可服务的节点"})})
	t.Cleanup(func() { e.svc.UsePublishGate(nil) })

	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	_, err := e.svc.SetPlanPools(ctx, plan.ID, PoolsUpdate{
		ExpectedRowVersion: detail.RowVersion,
		PoolIDs:            []uuid.UUID{},
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("已发布套餐清空线路期望 422，得到 %v", err)
	}
	detail, _ = e.svc.GetPlan(ctx, plan.ID)
	poolEqualIDs(t, detail.PoolIDs, []uuid.UUID{p1})

	// 草稿（没发布过）不过闸
	draft := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "草稿不过闸", Draft: true})
	detail, _ = e.svc.GetPlan(ctx, draft.ID)
	if _, err := e.svc.SetPlanPools(ctx, draft.ID, PoolsUpdate{ExpectedRowVersion: detail.RowVersion, PoolIDs: []uuid.UUID{}}); err != nil {
		t.Fatalf("草稿改线路不应过上架检查：%v", err)
	}
}
