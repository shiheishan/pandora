package service

// 套餐版本绑池（plan_node_pools）的 PG 集成测试。PoolGuard 用假实现（真实现是 node 模块）。

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type fakePoolGuard struct {
	calls [][]uuid.UUID
	err   error
}

func (f *fakePoolGuard) LockAssignablePools(_ context.Context, ids []uuid.UUID) error {
	cp := append([]uuid.UUID(nil), ids...)
	f.calls = append(f.calls, cp)
	return f.err
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

func draftOf(t *testing.T, ctx context.Context, e *env, planID uuid.UUID) Version {
	t.Helper()
	ver, err := e.svc.CreateVersion(ctx, planID)
	if err != nil {
		t.Fatal(err)
	}
	return ver
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

func poolCountAuditPoolsChanged(t *testing.T, ctx context.Context, e *env, versionID uuid.UUID) int64 {
	t.Helper()
	return scanInt64(t, ctx, e,
		`SELECT COUNT(*)::bigint FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionVersionPoolsChanged, versionID)
}

func TestSetVersionPoolsDraft(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	draft := draftOf(t, ctx, e, plan.ID)
	p1 := seedPool(t, ctx, e, "active")
	p2 := seedPool(t, ctx, e, "active")

	newRV, err := e.svc.SetVersionPools(ctx, plan.ID, draft.ID, draft.RowVersion, []uuid.UUID{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	if newRV != draft.RowVersion+1 {
		t.Fatalf("row_version 期望 %d，得到 %d", draft.RowVersion+1, newRV)
	}

	m, err := e.svc.VersionPools(ctx, []uuid.UUID{draft.ID})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, m[draft.ID], []uuid.UUID{p1, p2})

	newRV2, err := e.svc.SetVersionPools(ctx, plan.ID, draft.ID, newRV, []uuid.UUID{p2})
	if err != nil {
		t.Fatal(err)
	}
	if newRV2 != newRV+1 {
		t.Fatalf("第二次 row_version 期望 %d，得到 %d", newRV+1, newRV2)
	}
	m2, err := e.svc.VersionPools(ctx, []uuid.UUID{draft.ID})
	if err != nil {
		t.Fatal(err)
	}
	poolEqualIDs(t, m2[draft.ID], []uuid.UUID{p2})

	n := poolCountAuditPoolsChanged(t, ctx, e, draft.ID)
	if n != 2 {
		t.Fatalf("audit_events plan_version.pools_changed 期望 2 条，得到 %d", n)
	}
}

func TestSetVersionPoolsStaleRowVersion(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	draft := draftOf(t, ctx, e, plan.ID)
	p1 := seedPool(t, ctx, e, "active")

	_, err := e.svc.SetVersionPools(ctx, plan.ID, draft.ID, draft.RowVersion, []uuid.UUID{p1})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.SetVersionPools(ctx, plan.ID, draft.ID, draft.RowVersion, []uuid.UUID{p1})
	if errCode(err) != errs.Conflict {
		t.Fatalf("陈旧 row_version 期望 409，得到 %v", err)
	}
	if errFields(err)["row_version"] == "" {
		t.Fatal("fields 缺 row_version")
	}

	_, err = e.svc.SetVersionPools(ctx, plan.ID, draft.ID, 0, []uuid.UUID{p1})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("expectedRowVersion=0 期望 422，得到 %v", err)
	}
}

func TestSetVersionPoolsPublishedRejected(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	if plan.CurrentVersionID == nil {
		t.Fatal("无当前版本")
	}
	pubID := *plan.CurrentVersionID
	p1 := seedPool(t, ctx, e, "active")

	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pubRV int64
	for _, v := range detail.Versions {
		if v.ID == pubID {
			pubRV = v.RowVersion
			break
		}
	}

	_, err = e.svc.SetVersionPools(ctx, plan.ID, pubID, pubRV, []uuid.UUID{p1})
	if errCode(err) != errs.Conflict {
		t.Fatalf("已发布版本 SetVersionPools 期望 409，得到 %v", err)
	}

	err = execSavepoint(t, ctx, e, "sp_pub_ins",
		`INSERT INTO plan_node_pools (plan_version_id, pool_id) VALUES (?, ?)`, pubID, p1)
	assertRestrictViolation(t, err)
}

func TestSetVersionPoolsUnknownVersion(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	other := publishedPlan(t, ctx, e, publishedPlanOpts{})
	otherDraft := draftOf(t, ctx, e, other.ID)
	p1 := seedPool(t, ctx, e, "active")

	_, err := e.svc.SetVersionPools(ctx, plan.ID, uuid.New(), 1, []uuid.UUID{p1})
	if errCode(err) != errs.NotFound {
		t.Fatalf("随机 version 期望 404，得到 %v", err)
	}

	_, err = e.svc.SetVersionPools(ctx, plan.ID, otherDraft.ID, otherDraft.RowVersion, []uuid.UUID{p1})
	if errCode(err) != errs.NotFound {
		t.Fatalf("别套餐版本 期望 404，得到 %v", err)
	}
}

func TestPoolBindingVersion(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	if plan.CurrentVersionID == nil {
		t.Fatal("无当前版本")
	}
	cur := *plan.CurrentVersionID

	bv, err := e.svc.PoolBindingVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bv.Editable || bv.Status != VersionPublished || bv.VersionID == nil || *bv.VersionID != cur {
		t.Fatalf("仅已发布: %+v", bv)
	}
	if bv.PoolIDs == nil {
		t.Fatal("PoolIDs 不应为 nil")
	}

	draft := draftOf(t, ctx, e, plan.ID)
	p1 := seedPool(t, ctx, e, "active")
	_, err = e.svc.SetVersionPools(ctx, plan.ID, draft.ID, draft.RowVersion, []uuid.UUID{p1})
	if err != nil {
		t.Fatal(err)
	}

	bv2, err := e.svc.PoolBindingVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bv2.Editable || bv2.Status != VersionDraft || bv2.VersionID == nil || *bv2.VersionID != draft.ID {
		t.Fatalf("有草稿: %+v", bv2)
	}
	poolEqualIDs(t, bv2.PoolIDs, []uuid.UUID{p1})

	_, err = e.svc.PoolBindingVersion(ctx, uuid.New())
	if errCode(err) != errs.NotFound {
		t.Fatalf("不存在套餐 期望 404，得到 %v", err)
	}

	bare, err := e.svc.CreatePlan(ctx, PlanInput{
		Code:             uniqCode(t, "nopub"),
		Name:             "未发布无版本",
		Visibility:       VisibilityPublic,
		AllowNewPurchase: true,
		AllowRenewal:     true,
		AllowUpgrade:     true,
		Highlights:       []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	bv3, err := e.svc.PoolBindingVersion(ctx, bare.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bv3.VersionID != nil {
		t.Fatalf("无版本时 VersionID 应为 nil: %+v", bv3)
	}
	if bv3.PoolIDs == nil || len(bv3.PoolIDs) != 0 {
		t.Fatalf("无版本时 PoolIDs 应为空切片: %+v", bv3)
	}
}

func TestPoolUsage(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	p1 := seedPool(t, ctx, e, "active")
	p2 := seedPool(t, ctx, e, "active")
	p3 := seedPool(t, ctx, e, "active")

	planA, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "ua"), Name: "套餐甲", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	planB, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "ub"), Name: "套餐乙", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 一个套餐同时只能有一个草稿：甲的草稿绑 p1、p2，乙的草稿只绑 p1
	da := draftOf(t, ctx, e, planA.ID)
	dB := draftOf(t, ctx, e, planB.ID)
	_, err = e.svc.SetVersionPools(ctx, planA.ID, da.ID, da.RowVersion, []uuid.UUID{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetVersionPools(ctx, planB.ID, dB.ID, dB.RowVersion, []uuid.UUID{p1})
	if err != nil {
		t.Fatal(err)
	}

	usage, err := e.svc.PoolUsage(ctx, []uuid.UUID{p1, p2, p3})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := usage[p3]; ok {
		t.Fatal("p3 不应在 map 里")
	}
	u1 := usage[p1]
	if u1.Versions != 2 {
		t.Fatalf("p1 Versions=%d", u1.Versions)
	}
	gotNames := append([]string(nil), u1.PlanNames...)
	sort.Strings(gotNames)
	wantNames := []string{"套餐甲", "套餐乙"}
	sort.Strings(wantNames)
	if len(gotNames) != 2 || gotNames[0] != wantNames[0] || gotNames[1] != wantNames[1] {
		t.Fatalf("p1 PlanNames=%v want %v（升序）", u1.PlanNames, wantNames)
	}
	u2 := usage[p2]
	if u2.Versions != 1 || len(u2.PlanNames) != 1 || u2.PlanNames[0] != "套餐甲" {
		t.Fatalf("p2 用量 %+v", u2)
	}
}

func TestWizardCreateBindsPools(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	pA := seedPool(t, ctx, e, "active")
	pB := seedPool(t, ctx, e, "active")

	out, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "wcp"), Name: "绑池向导", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		PoolIDs:   []uuid.UUID{pB, pA},
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 1 {
		t.Fatalf("guard 调用次数 %d", len(g.calls))
	}
	poolEqualIDs(t, g.calls[0], []uuid.UUID{pA, pB})

	var ver *Version
	for i := range out.Plan.Versions {
		if out.Plan.Versions[i].ID == out.VersionID {
			ver = &out.Plan.Versions[i]
			break
		}
	}
	if ver == nil {
		t.Fatal("版本缺失")
	}
	poolEqualIDs(t, ver.PoolIDs, []uuid.UUID{pA, pB})

	detail, err := e.svc.GetPlan(ctx, out.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range detail.Versions {
		if detail.Versions[i].ID == out.VersionID {
			poolEqualIDs(t, detail.Versions[i].PoolIDs, []uuid.UUID{pA, pB})
			return
		}
	}
	t.Fatal("GetPlan 未找到版本")
}

func TestWizardCreatePoolsWithoutGuard(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	p1 := seedPool(t, ctx, e, "active")
	_, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "ng"), Name: "无 guard", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		PoolIDs:   []uuid.UUID{p1},
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: false,
	})
	if errCode(err) != errs.Unavailable {
		t.Fatalf("非空 PoolIDs 无 guard 期望 503，得到 %v", err)
	}

	_, err = e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "eg"), Name: "空池", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		PoolIDs:   []uuid.UUID{},
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: false,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWizardCreatePoolGuardError(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{err: errs.Invalid(map[string]string{"pool_ids": "x"})}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")
	_, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "ge"), Name: "guard 错", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		PoolIDs:   []uuid.UUID{p1},
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: false,
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("期望 422，得到 %v", err)
	}
	if errFields(err)["pool_ids"] == "" {
		t.Fatal("fields 缺 pool_ids")
	}
}

func TestWizardUpdatePoolsRollVersion(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")
	p2 := seedPool(t, ctx, e, "active")

	created, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "wur"), Name: "池滚版本", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		PoolIDs:   []uuid.UUID{p1},
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldCur := created.VersionID

	poolIDs12 := []uuid.UUID{p1, p2}
	upd, err := e.svc.UpdatePlanComplete(ctx, created.Plan.ID, WizardUpdate{
		ExpectedRowVersion: created.Plan.RowVersion,
		Code:               created.Plan.Code,
		Name:               created.Plan.Name,
		Visibility:         VisibilityPublic,
		PoolIDs:            &poolIDs12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Plan.CurrentVersionID == nil || *upd.Plan.CurrentVersionID == oldCur {
		t.Fatal("改池应滚出新当前版本")
	}
	newCur := *upd.Plan.CurrentVersionID
	var curVer *Version
	for i := range upd.Plan.Versions {
		if upd.Plan.Versions[i].ID == newCur {
			curVer = &upd.Plan.Versions[i]
			break
		}
	}
	if curVer == nil {
		t.Fatal("找不到新当前版本")
	}
	poolEqualIDs(t, curVer.PoolIDs, poolIDs12)

	lineMsg := false
	for _, c := range upd.Changed {
		if strings.HasPrefix(c, "可用线路已更新") {
			lineMsg = true
			break
		}
	}
	if !lineMsg {
		t.Fatalf("Changed 应含可用线路说明: %v", upd.Changed)
	}

	poolIDs21 := []uuid.UUID{p2, p1}
	upd2, err := e.svc.UpdatePlanComplete(ctx, created.Plan.ID, WizardUpdate{
		ExpectedRowVersion: upd.Plan.RowVersion,
		Code:               upd.Plan.Code,
		Name:               upd.Plan.Name,
		Visibility:         VisibilityPublic,
		PoolIDs:            &poolIDs21,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd2.Plan.CurrentVersionID == nil || *upd2.Plan.CurrentVersionID != newCur {
		t.Fatalf("同集合改顺序不应滚版本: old=%v new=%v", newCur, upd2.Plan.CurrentVersionID)
	}
}

func TestWizardUpdateQuotaKeepsPools(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := &fakePoolGuard{}
	e.svc.UsePoolGuard(g)

	p1 := seedPool(t, ctx, e, "active")

	created, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "wk"), Name: "配额保池", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		PoolIDs:   []uuid.UUID{p1},
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldCur := created.VersionID

	upd, err := e.svc.UpdatePlanComplete(ctx, created.Plan.ID, WizardUpdate{
		ExpectedRowVersion: created.Plan.RowVersion,
		Code:               created.Plan.Code,
		Name:               created.Plan.Name,
		Visibility:         VisibilityPublic,
		TrafficGB:          int64Ptr(20),
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Plan.CurrentVersionID == nil || *upd.Plan.CurrentVersionID == oldCur {
		t.Fatal("改流量应滚版本")
	}
	newCur := *upd.Plan.CurrentVersionID
	for i := range upd.Plan.Versions {
		if upd.Plan.Versions[i].ID == newCur {
			poolEqualIDs(t, upd.Plan.Versions[i].PoolIDs, []uuid.UUID{p1})
			return
		}
	}
	t.Fatal("未找到新当前版本")
}
