package service

// 门户目录、下单快照、流量包与用户组外键的 PG 测试。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestPortalCatalogVisibility(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := seedGroup(t, ctx, e, "")
	public := publishedPlan(t, ctx, e, publishedPlanOpts{})
	grpOnly := groupOnlyPublishedPlan(t, ctx, e, g)

	anon, err := e.svc.PortalCatalog(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !portalHasPlan(anon, public.ID) {
		t.Fatal("匿名应看到 public 套餐")
	}
	if portalHasPlan(anon, grpOnly.ID) {
		t.Fatal("匿名不应看到 group 套餐")
	}

	in := seedUser(t, ctx, e, &g)
	inGrp, err := e.svc.PortalCatalog(ctx, &in)
	if err != nil {
		t.Fatal(err)
	}
	if !portalHasPlan(inGrp, grpOnly.ID) {
		t.Fatal("组内用户应看到 group 套餐")
	}
	out := seedUser(t, ctx, e, nil)
	outGrp, _ := e.svc.PortalCatalog(ctx, &out)
	if portalHasPlan(outGrp, grpOnly.ID) {
		t.Fatal("组外用户不应看到 group 套餐")
	}
}

func portalHasPlan(list []OfferPlan, id uuid.UUID) bool {
	for _, o := range list {
		if o.Plan.ID == id {
			return true
		}
	}
	return false
}

func TestPortalSkipsExpiredPriceAndNoPrice(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	expired := publishedPlan(t, ctx, e, publishedPlanOpts{})
	// 已发布套餐的价格整行冻结，产品代码造不出「窗口已过期」的在售价；绕过触发器直接改（CHECK 仍生效，窗口两端都设成过去）。
	now := time.Now().UTC()
	execBypassTriggers(t, ctx, e, `UPDATE prices SET valid_from = ?, valid_until = ? WHERE plan_id = ?`,
		now.Add(-2*time.Hour), now.Add(-time.Hour), expired.ID)

	noprice, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "nop"), Name: "无价门户", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(1),
		Prices:    []WizardPrice{{BillingInterval: "month", IntervalCount: 1, UnitAmount: decimal.RequireFromString("1.00"), Currency: "CNY"}},
		Publish:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE plan_id = ?`, noprice.Plan.ID); err != nil {
		t.Fatal(err)
	}

	closed := publishedPlan(t, ctx, e, publishedPlanOpts{AllowNewPurchase: boolPtr(false)})

	list, _ := e.svc.PortalCatalog(ctx, nil)
	if portalHasPlan(list, expired.ID) {
		t.Fatal("过期价套餐不应出现")
	}
	if portalHasPlan(list, noprice.Plan.ID) {
		t.Fatal("无可用价套餐不应出现")
	}
	if !portalHasPlan(list, closed.ID) {
		t.Fatal("关新购仍应出现在目录")
	}
}

func TestOfferGates(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	closed := publishedPlan(t, ctx, e, publishedPlanOpts{AllowNewPurchase: boolPtr(false)})
	detail, _ := e.svc.GetPlan(ctx, closed.ID)
	priceID := detail.Prices[0].ID
	_, err := e.svc.Offer(ctx, priceID, OfferRequest{Use: UseNew, UserID: uuidPtr(seedUser(t, ctx, e, nil))})
	if !errors.Is(err, ErrNewPurchaseClosed) {
		t.Fatalf("新购关闭: %v", err)
	}

	arch := publishedPlan(t, ctx, e, publishedPlanOpts{})
	d2, _ := e.svc.GetPlan(ctx, arch.ID)
	p := d2.Prices[0]
	_, _ = e.svc.ArchivePrice(ctx, arch.ID, p.ID, p.RowVersion)
	_, err = e.svc.Offer(ctx, p.ID, OfferRequest{Use: UseNew, UserID: uuidPtr(seedUser(t, ctx, e, nil))})
	if !errors.Is(err, ErrPriceArchived) {
		t.Fatalf("新购归档价: %v", err)
	}
	_, err = e.svc.Offer(ctx, p.ID, OfferRequest{Use: UseRenewal, UserID: uuidPtr(seedUser(t, ctx, e, nil))})
	if !errors.Is(err, ErrRenewPriceGone) {
		t.Fatalf("续费归档价: %v", err)
	}

	hidden := publishedPlan(t, ctx, e, publishedPlanOpts{Visibility: VisibilityHidden})
	hd, _ := e.svc.GetPlan(ctx, hidden.ID)
	hp := hd.Prices[0].ID
	_, err = e.svc.Offer(ctx, hp, OfferRequest{Use: UseNew, Manual: true, UserID: uuidPtr(seedUser(t, ctx, e, nil))})
	if err != nil {
		t.Fatalf("人工开单 hidden: %v", err)
	}
}

func TestPackOfferArchived(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	pack, err := e.svc.CreateTrafficPack(ctx, PackInput{
		Name: "小包", TrafficBytes: 1 << 30, Currency: "CNY",
		UnitAmount: decimal.RequireFromString("5.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetTrafficPackStatus(ctx, pack.ID, PackArchived, &pack.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.PackOffer(ctx, pack.ID, false)
	if !errors.Is(err, ErrPackArchived) {
		t.Fatalf("下架包: %v", err)
	}
}

// --- 11. 流量包 ---

func TestTrafficPackLifecycle(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	pack, err := e.svc.CreateTrafficPack(ctx, PackInput{
		Name: "流量", TrafficBytes: 2 << 30, Currency: "CNY",
		UnitAmount: decimal.RequireFromString("3.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := e.svc.UpdateTrafficPack(ctx, pack.ID, &pack.UpdatedAt, PackInput{
		Name: "流量改", TrafficBytes: 2 << 30, Currency: "CNY",
		UnitAmount: decimal.RequireFromString("4.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := pack.UpdatedAt
	_, err = e.svc.UpdateTrafficPack(ctx, pack.ID, &stale, PackInput{
		Name: "旧锁", TrafficBytes: 2 << 30, Currency: "CNY",
		UnitAmount: decimal.RequireFromString("5.00"),
	})
	if errCode(err) != errs.Conflict {
		t.Fatalf("旧 updated_at 应 409，得到 %v", err)
	}
	if errFields(err)["updated_at"] == "" {
		t.Fatal("fields 缺 updated_at")
	}

	arch, err := e.svc.SetTrafficPackStatus(ctx, upd.ID, PackArchived, &upd.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := e.svc.SetTrafficPackStatus(ctx, arch.ID, PackActive, &arch.UpdatedAt)
	if err != nil || restored.Status != PackActive {
		t.Fatalf("上架: err=%v status=%s", err, restored.Status)
	}
	_, err = e.svc.SetTrafficPackStatus(ctx, restored.ID, PackActive, &restored.UpdatedAt)
	if errCode(err) != errs.Conflict {
		t.Fatalf("重复上架应 409，得到 %v", err)
	}
}

// --- 12. 删用户组：可见组级联删除，只往收紧方向变 ---

func TestDeleteUserGroupCascadesPlanVisibility(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := seedGroup(t, ctx, e, "")
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{
		Visibility: VisibilityGroup, VisibleGroupIDs: []uuid.UUID{g},
	})
	if err := e.exec(ctx, `DELETE FROM user_groups WHERE id = ?`, g); err != nil {
		t.Fatalf("只被套餐可见组引用的分组应能删除：%v", err)
	}
	if n := scanInt64(t, ctx, e, `SELECT count(*) FROM plan_visible_groups WHERE plan_id = ?`, plan.ID); n != 0 {
		t.Fatalf("可见组关联行应被级联删除，剩 %d", n)
	}
	got, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != VisibilityGroup || len(got.VisibleGroupIDs) != 0 {
		t.Fatalf("可见性应仍是 group 且组为空：%s %v", got.Visibility, got.VisibleGroupIDs)
	}
	other := seedGroup(t, ctx, e, "")
	viewer := seedUser(t, ctx, e, &other)
	list, err := e.svc.PortalCatalog(ctx, &viewer)
	if err != nil {
		t.Fatal(err)
	}
	if portalHasPlan(list, plan.ID) {
		t.Fatal("删掉最后一个可见组后套餐不应对任何人可见")
	}
}

// 组价没被买过时删组连价格一起删（00050：prices.user_group_id 是 ON DELETE CASCADE，价格守卫只放行这种级联删除）。
// 买过的组价挡住删组由 identity 的 TestPGDeleteGroupRefusedWhenPriceSold 覆盖。
func TestDeleteUserGroupCascadesUnsoldPrices(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g := seedGroup(t, ctx, e, "")
	_ = groupOnlyPublishedPlan(t, ctx, e, g)
	if n := scanInt64(t, ctx, e, `SELECT count(*) FROM prices WHERE user_group_id = ?`, g); n == 0 {
		t.Fatal("夹具应有组专属价")
	}
	if err := execSavepoint(t, ctx, e, "sp_delgrp", `DELETE FROM user_groups WHERE id = ?`, g); err != nil {
		t.Fatalf("没卖过的组价应随组级联删除，得到 %v", err)
	}
	if n := scanInt64(t, ctx, e, `SELECT count(*) FROM prices WHERE user_group_id = ?`, g); n != 0 {
		t.Fatalf("删组后组专属价应为 0 条，得到 %d", n)
	}
}

// groupOnlyPublishedPlan 发布仅组价、无公开价的 group 可见套餐（价格触发器禁止 DELETE，不能靠删公开价）。
func groupOnlyPublishedPlan(t *testing.T, ctx context.Context, e *env, g uuid.UUID) PlanDetail {
	t.Helper()
	plan, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "gonly"), Name: "仅组价", Visibility: VisibilityGroup,
		VisibleGroupIDs:  []uuid.UUID{g},
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ver, err := e.svc.CreateVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	verRV, err := e.svc.UpdateVersion(ctx, plan.ID, ver.ID, ver.RowVersion, VersionInput{
		QuotaResetStrategy:   ResetBillingCycle,
		RenewalExtendsPeriod: true, RenewalResetsQuota: true, RenewalKeepsAddons: true,
		DeviceReleaseHours: 24, OveragePolicy: OveragePolicySuspend,
		Quotas: []Quota{{Metric: MetricTraffic, Limit: int64Ptr(1 << 30), Unit: "bytes", Period: PeriodCycle}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreatePrice(ctx, plan.ID, PriceInput{
		Currency: "CNY", UnitAmount: decimal.RequireFromString("7.00"),
		BillingInterval: "month", IntervalCount: 1, UserGroupID: &g,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	_, _, err = e.svc.PublishVersion(ctx, plan.ID, ver.ID, detail.RowVersion, verRV)
	if err != nil {
		t.Fatal(err)
	}
	detail, err = e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

// 本文件专用小工具（不与 helpers 重名）。

func boolPtr(b bool) *bool { return &b }

func uuidPtr(id uuid.UUID) *uuid.UUID { return &id }
