package service

// 目录后台写用例的 PG 测试：新建、发布、乐观锁、归档、列表筛选。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestCreateSubscriptionDraft(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	detail, err := e.svc.CreatePlan(ctx, PlanCreate{
		Kind:    KindSubscription,
		Name:    "标准套餐",
		Tiers:   []TierInput{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("9.90")}},
		Usage:   UsageInput{TrafficBytes: int64Ptr(10 << 30)},
		Sales:   SalesInput{Visibility: VisibilityPublic, AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true},
		Publish: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 库里可能已有别的用例留下的 standard 系列代码，只断言前缀与重名递增。
	if detail.Kind != KindSubscription || detail.Status != PlanDraft || !strings.HasPrefix(detail.Code, "standard") {
		t.Fatalf("套餐: kind=%s status=%s code=%s", detail.Kind, detail.Status, detail.Code)
	}
	if detail.RowVersion != 1 {
		t.Fatalf("row_version=%d", detail.RowVersion)
	}
	if len(detail.Versions) != 1 || detail.Versions[0].Status != VersionDraft || detail.Versions[0].Version != 1 {
		t.Fatalf("版本: %+v", detail.Versions)
	}
	if detail.CurrentVersionID != nil {
		t.Fatal("草稿不应有 current_version")
	}
	q := detail.Versions[0].Quotas
	if len(q) != 1 || q[0].Period != PeriodCycle || q[0].Metric != MetricTraffic {
		t.Fatalf("配额: %+v", q)
	}
	var active int
	for _, p := range detail.Prices {
		if p.Status == PriceActive && p.PlanKind == KindSubscription {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("在售价格 plan_kind: %d 条", active)
	}

	action := scanString(t, ctx, e, `SELECT action FROM audit_events WHERE resource_id = ? ORDER BY occurred_at DESC LIMIT 1`, detail.ID)
	if action != ActionPlanCreate {
		t.Fatalf("审计 action=%s", action)
	}

	detail2, err := e.svc.CreatePlan(ctx, PlanCreate{
		Kind:    KindSubscription,
		Name:    "标准套餐",
		Tiers:   []TierInput{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("8.00")}},
		Usage:   UsageInput{TrafficBytes: int64Ptr(10 << 30)},
		Sales:   SalesInput{Visibility: VisibilityPublic, AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true},
		Publish: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail2.Code == detail.Code || !strings.HasPrefix(detail2.Code, "standard-") {
		t.Fatalf("重名 code=%s", detail2.Code)
	}
}

func TestCreateOneTimePublished(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	days := 30
	detail, err := e.svc.CreatePlan(ctx, PlanCreate{
		Kind:    KindOneTime,
		Name:    "流量包月",
		OneTime: &OneTimeInput{UnitAmount: decimal.RequireFromString("19.90"), ValidityDays: &days},
		Usage:   UsageInput{TrafficBytes: int64Ptr(5 << 30)},
		Sales:   SalesInput{Visibility: VisibilityPublic, AllowNewPurchase: true, AllowRenewal: false, AllowUpgrade: true},
		Publish: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != PlanActive || detail.AllowRenewal {
		t.Fatalf("status=%s allow_renewal=%v", detail.Status, detail.AllowRenewal)
	}
	if detail.CurrentVersionID == nil || detail.Version == nil || *detail.Version != 1 {
		t.Fatalf("current_version=%v version=%v", detail.CurrentVersionID, detail.Version)
	}
	if len(detail.Versions[0].Quotas) != 1 || detail.Versions[0].Quotas[0].Period != PeriodTotal {
		t.Fatalf("配额 period=%v", detail.Versions[0].Quotas)
	}
	var ot Price
	found := false
	for _, p := range detail.Prices {
		if p.Status == PriceActive {
			ot = p
			found = true
		}
	}
	if !found || ot.BillingInterval != IntervalOneTime || ot.IntervalCount != 1 {
		t.Fatalf("价格: %+v", ot)
	}
	if ot.ValidityDays == nil || *ot.ValidityDays != 30 {
		t.Fatalf("validity_days=%v", ot.ValidityDays)
	}
}

func TestCreateValidation(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	_, err := e.svc.CreatePlan(ctx, PlanCreate{
		Kind:    KindOneTime,
		Name:    "一次性",
		OneTime: &OneTimeInput{UnitAmount: decimal.RequireFromString("9.90")},
		Usage:   UsageInput{TrafficBytes: int64Ptr(1 << 30)},
		Sales:   SalesInput{Visibility: VisibilityPublic, AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true},
	})
	if errCode(err) != errs.ValidationFailed || errFields(err)["allow_renewal"] == "" {
		t.Fatalf("一次性续费: %v fields=%v", err, errFields(err))
	}

	_, err = e.svc.CreatePlan(ctx, PlanCreate{
		Kind:  KindSubscription,
		Name:  "坏周期",
		Tiers: []TierInput{{BillingInterval: IntervalMonth, IntervalCount: 2, UnitAmount: decimal.RequireFromString("9.90")}},
		Usage: UsageInput{TrafficBytes: int64Ptr(1 << 30)},
		Sales: SalesInput{Visibility: VisibilityPublic, AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true},
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("订阅非法周期: %v", err)
	}
}

func TestPublishRequiresActivePrice(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	detail := publishedPlan(t, ctx, e, publishedPlanOpts{Draft: true})
	if detail.DraftVersionID == nil {
		t.Fatal("无草稿版本")
	}
	verID := *detail.DraftVersionID

	// 归档唯一在售价
	for _, p := range detail.Prices {
		if p.Status != PriceActive {
			continue
		}
		if err := e.exec(ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE id = ?`, p.ID); err != nil {
			t.Fatal(err)
		}
	}

	_, err := e.svc.PublishVersion(ctx, detail.ID, verID, detail.RowVersion)
	if errCode(err) != errs.ValidationFailed || errFields(err)["prices"] == "" {
		t.Fatalf("无在售价发布: %v fields=%v", err, errFields(err))
	}
}

func TestPublishVersionFlow(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	detail := publishedPlan(t, ctx, e, publishedPlanOpts{Draft: true})
	verID := *detail.DraftVersionID

	pub, err := e.svc.PublishVersion(ctx, detail.ID, verID, detail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Status != PlanActive || pub.RowVersion != detail.RowVersion+1 {
		t.Fatalf("发布后 status=%s row_version=%d", pub.Status, pub.RowVersion)
	}
	var frozen Version
	for _, v := range pub.Versions {
		if v.ID == verID {
			frozen = v
		}
	}
	if frozen.Status != VersionPublished || frozen.FrozenAt == nil {
		t.Fatalf("版本未冻结: status=%s frozen=%v", frozen.Status, frozen.FrozenAt)
	}
}

func TestOptimisticLockConflicts(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	base := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, err := e.svc.GetPlan(ctx, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := detail.RowVersion + 1

	_, err = e.svc.UpdateBasic(ctx, base.ID, BasicUpdate{ExpectedRowVersion: stale, Name: "新名"})
	assertRowVersionConflict(t, err)

	_, err = e.svc.ArchivePlan(ctx, base.ID, stale)
	assertRowVersionConflict(t, err)

	_, err = e.svc.UpdateSales(ctx, base.ID, SalesUpdate{
		ExpectedRowVersion: stale,
		Sales:              SalesInput{Visibility: VisibilityPublic, AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true},
	})
	assertRowVersionConflict(t, err)

	amt := decimal.RequireFromString("12.00")
	_, err = e.svc.UpdatePrices(ctx, base.ID, PricesUpdate{
		ExpectedRowVersion: stale,
		Tiers:              []TierChange{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: &amt}},
	})
	assertRowVersionConflict(t, err)

	traffic := int64(20 << 30)
	_, err = e.svc.UpdateUsage(ctx, base.ID, UsageUpdate{ExpectedRowVersion: stale, Usage: UsageInput{TrafficBytes: &traffic}})
	assertRowVersionConflict(t, err)

	withDraft, err := e.svc.UpdateUsage(ctx, base.ID, UsageUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Usage:              UsageInput{TrafficBytes: &traffic},
	})
	if err != nil || withDraft.DraftVersionID == nil {
		t.Fatalf("建草稿: err=%v draft=%v", err, withDraft.DraftVersionID)
	}
	_, err = e.svc.PublishVersion(ctx, base.ID, *withDraft.DraftVersionID, withDraft.RowVersion+1)
	assertRowVersionConflict(t, err)

	// 成功一次后 row_version +1
	updated, err := e.svc.UpdateBasic(ctx, base.ID, BasicUpdate{ExpectedRowVersion: withDraft.RowVersion, Name: "改名一次"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RowVersion != withDraft.RowVersion+1 {
		t.Fatalf("UpdateBasic 后 row_version=%d", updated.RowVersion)
	}
}

func assertRowVersionConflict(t *testing.T, err error) {
	t.Helper()
	if errCode(err) != errs.Conflict {
		t.Fatalf("期望 409，得到 %v", err)
	}
	if errFields(err)["row_version"] == "" {
		t.Fatal("fields 缺 row_version")
	}
}

func TestArchivePlan(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	archived, err := e.svc.ArchivePlan(ctx, plan.ID, detail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != PlanArchived {
		t.Fatalf("status=%s", archived.Status)
	}

	_, err = e.svc.ArchivePlan(ctx, plan.ID, archived.RowVersion)
	if !errors.Is(err, ErrPlanArchived) && errCode(err) != errs.Conflict {
		t.Fatalf("重复归档: %v", err)
	}

	_, err = e.svc.UpdateBasic(ctx, plan.ID, BasicUpdate{ExpectedRowVersion: archived.RowVersion, Name: "x"})
	if !errors.Is(err, ErrPlanArchived) {
		t.Fatalf("归档后改基本信息: %v", err)
	}
}

func TestDuplicateActivePrice(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	var activeID uuid.UUID
	for _, p := range detail.Prices {
		if p.Status == PriceActive {
			activeID = p.ID
			break
		}
	}

	err := inSavepoint(t, ctx, e, "sp_dup", func() error {
		return e.exec(ctx, `INSERT INTO prices (plan_id, plan_kind, unit_amount, billing_interval, interval_count, status, row_version)
			VALUES (?, ?, 12.00, 'month', 1, 'active', 1)`, plan.ID, KindSubscription)
	})
	if err == nil {
		t.Fatal("期望唯一约束冲突")
	}
	if testkit.SQLState(err) != "23505" {
		t.Fatalf("期望 23505，得到 %v", err)
	}
	_ = activeID
}

func TestListPlansFilters(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	_ = publishedPlan(t, ctx, e, publishedPlanOpts{Name: "筛选订阅A", Kind: KindSubscription})
	_ = publishedPlan(t, ctx, e, publishedPlanOpts{
		Name: "筛选一次性B", Kind: KindOneTime, Draft: true,
	})

	subs, err := e.svc.ListPlans(ctx, PlanFilter{Kind: KindSubscription})
	if err != nil {
		t.Fatal(err)
	}
	foundSub := false
	for _, s := range subs {
		if s.Name == "筛选订阅A" {
			foundSub = true
		}
		if s.Kind != KindSubscription {
			t.Fatalf("kind 筛选泄漏: %s", s.Name)
		}
	}
	if !foundSub {
		t.Fatal("未找到订阅套餐")
	}

	drafts, err := e.svc.ListPlans(ctx, PlanFilter{Status: PlanDraft, Q: "筛选一次性"})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].Name != "筛选一次性B" {
		t.Fatalf("status+q: %+v", drafts)
	}

	// 通配符应被转义，不应把「筛选%」当成 LIKE 模式
	none, err := e.svc.ListPlans(ctx, PlanFilter{Q: "筛选%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("%% 应转义，得到 %d 条", len(none))
	}
}

func scanString(t *testing.T, ctx context.Context, e *env, q string, args ...any) string {
	t.Helper()
	var s string
	if err := e.row(ctx, q, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
