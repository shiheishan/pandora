package service

// 目录后台写用例的 PG 测试：发布、乐观锁、归档、价格、向导、卖点。

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// --- 1. 草稿 → 版本 → 发布全流程 ---

func TestCatalogDraftPublishFlow(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	created, err := e.svc.CreatePlan(ctx, PlanInput{
		Code:             uniqCode(t, "flow"),
		Name:             "流程套餐",
		Visibility:       VisibilityPublic,
		AllowNewPurchase: true,
		AllowRenewal:     true,
		AllowUpgrade:     true,
		Highlights:       []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != PlanDraft || created.RowVersion != 1 {
		t.Fatalf("新建套餐状态/版本: status=%s row_version=%d", created.Status, created.RowVersion)
	}

	ver, err := e.svc.CreateVersion(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ver.Status != VersionDraft || ver.RowVersion != 1 {
		t.Fatalf("草稿版本: status=%s row_version=%d", ver.Status, ver.RowVersion)
	}

	traffic := int64(5)
	lim := traffic * BytesPerGB
	verRV, err := e.svc.UpdateVersion(ctx, created.ID, ver.ID, ver.RowVersion, VersionInput{
		QuotaResetStrategy:   ResetBillingCycle,
		RenewalExtendsPeriod: true,
		RenewalResetsQuota:   true,
		RenewalKeepsAddons:   true,
		DeviceReleaseHours:   24,
		OveragePolicy:        OveragePolicySuspend,
		Entitlements: []Entitlement{
			{Code: "demo.feature", Value: json.RawMessage(`"on"`)},
		},
		Quotas: []Quota{
			{Metric: MetricTraffic, Limit: &lim, Unit: "bytes", Period: PeriodCycle},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if verRV != 2 {
		t.Fatalf("版本 row_version 期望 2，得到 %d", verRV)
	}

	price, err := e.svc.CreatePrice(ctx, created.ID, PriceInput{
		Currency:        "CNY",
		UnitAmount:      decimal.RequireFromString("19.90"),
		BillingInterval: "month",
		IntervalCount:   1,
	})
	if err != nil {
		t.Fatal(err)
	}

	detailBefore, err := e.svc.GetPlan(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	planRV, pubVerRV, err := e.svc.PublishVersion(ctx, created.ID, ver.ID, detailBefore.RowVersion, verRV)
	if err != nil {
		t.Fatal(err)
	}
	if planRV != detailBefore.RowVersion+1 || pubVerRV != verRV+1 {
		t.Fatalf("发布后 row_version: plan=%d ver=%d", planRV, pubVerRV)
	}

	detail, err := e.svc.GetPlan(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != PlanActive {
		t.Fatalf("套餐 status=%s", detail.Status)
	}
	if detail.CurrentVersionID == nil || *detail.CurrentVersionID != ver.ID {
		t.Fatal("current_version_id 未指向发布版本")
	}
	if detail.RowVersion != planRV {
		t.Fatalf("套餐 row_version=%d", detail.RowVersion)
	}

	var pub *Version
	for i := range detail.Versions {
		if detail.Versions[i].ID == ver.ID {
			pub = &detail.Versions[i]
			break
		}
	}
	if pub == nil {
		t.Fatal("找不到发布版本")
	}
	if pub.Status != VersionPublished || pub.FrozenAt == nil {
		t.Fatalf("版本未冻结: status=%s frozen_at=%v", pub.Status, pub.FrozenAt)
	}
	if pub.RowVersion != pubVerRV {
		t.Fatalf("版本 row_version=%d", pub.RowVersion)
	}
	if tl := pub.TrafficLimit(); tl == nil || *tl != lim {
		t.Fatalf("流量配额 %v", tl)
	}

	foundPrice := false
	for _, p := range detail.Prices {
		if p.ID == price.ID && p.Status == PriceActive {
			foundPrice = true
		}
	}
	if !foundPrice {
		t.Fatal("价格未读回")
	}
}

// --- 2. 发布前校验 ---

func TestPublishRequiresPrice(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "noprice"), Name: "无价", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ver, err := e.svc.CreateVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.UpdateVersion(ctx, plan.ID, ver.ID, ver.RowVersion, VersionInput{
		QuotaResetStrategy: ResetBillingCycle,
		RenewalExtendsPeriod: true, RenewalResetsQuota: true, RenewalKeepsAddons: true,
		DeviceReleaseHours: 24, OveragePolicy: OveragePolicySuspend,
		Quotas: []Quota{{Metric: MetricTraffic, Limit: int64Ptr(1 << 30), Unit: "bytes", Period: PeriodCycle}},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	_, _, err = e.svc.PublishVersion(ctx, plan.ID, ver.ID, detail.RowVersion, 2)
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("期望 422，得到 %v", err)
	}
	if errFields(err)["prices"] == "" {
		t.Fatal("fields 缺 prices")
	}
}

func TestPublishRejectsInviteOnly(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)

	created, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "inv"), Name: "邀请", Visibility: VisibilityInviteOnly,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		Prices: []WizardPrice{{
			BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY",
		}},
		Publish: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	var draftID uuid.UUID
	var draftRV int64
	for _, v := range created.Plan.Versions {
		if v.Status == VersionDraft {
			draftID = v.ID
			draftRV = v.RowVersion
			break
		}
	}
	_, _, err = e.svc.PublishVersion(ctx, created.Plan.ID, draftID, created.Plan.RowVersion, draftRV)
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("期望 422，得到 %v", err)
	}
	if errFields(err)["visibility"] == "" {
		t.Fatal("fields 缺 visibility")
	}
}

func TestPublishGroupPriceCoverage(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	g1 := seedGroup(t, ctx, e, "")
	g2 := seedGroup(t, ctx, e, "")

	plan, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "grp"), Name: "分组", Visibility: VisibilityGroup,
		VisibleGroupIDs:  []uuid.UUID{g1, g2},
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
		QuotaResetStrategy: ResetBillingCycle,
		RenewalExtendsPeriod: true, RenewalResetsQuota: true, RenewalKeepsAddons: true,
		DeviceReleaseHours: 24, OveragePolicy: OveragePolicySuspend,
		Quotas: []Quota{{Metric: MetricTraffic, Limit: int64Ptr(1 << 30), Unit: "bytes", Period: PeriodCycle}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreatePrice(ctx, plan.ID, PriceInput{
		Currency: "CNY", UnitAmount: decimal.RequireFromString("8.00"),
		BillingInterval: "month", IntervalCount: 1, UserGroupID: &g1,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	_, _, err = e.svc.PublishVersion(ctx, plan.ID, ver.ID, detail.RowVersion, verRV)
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("期望 422 prices，得到 %v", err)
	}
	if errFields(err)["prices"] == "" {
		t.Fatal("fields 缺 prices")
	}
}

// --- 3. 乐观锁 ---

func TestOptimisticLockConflicts(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	base := publishedPlan(t, ctx, e, publishedPlanOpts{})
	draft, err := e.svc.CreateVersion(ctx, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 建草稿可能动了套餐的 row_version，取最新的再造「过期」版本号。
	detail, err := e.svc.GetPlan(ctx, base.ID)
	if err != nil {
		t.Fatal(err)
	}
	activePrice := detail.Prices[0]

	// 过期的版本号用「当前 + 1」：仍是合法的正整数，不会先撞「必须为正整数」的 422。
	_, err = e.svc.UpdatePlan(ctx, base.ID, detail.RowVersion+1, PlanInput{
		Code: base.Code, Name: base.Name, Visibility: base.Visibility,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	assertRowVersionConflict(t, err)

	_, err = e.svc.ArchivePlan(ctx, base.ID, detail.RowVersion+1)
	assertRowVersionConflict(t, err)

	_, err = e.svc.UpdateVersion(ctx, base.ID, draft.ID, draft.RowVersion+1, VersionInput{
		QuotaResetStrategy: ResetBillingCycle,
		RenewalExtendsPeriod: true, RenewalResetsQuota: true, RenewalKeepsAddons: true,
		DeviceReleaseHours: 24, OveragePolicy: OveragePolicySuspend,
	})
	assertRowVersionConflict(t, err)

	_, _, err = e.svc.PublishVersion(ctx, base.ID, draft.ID, detail.RowVersion, draft.RowVersion+1)
	assertRowVersionConflict(t, err)

	_, err = e.svc.ArchivePrice(ctx, base.ID, activePrice.ID, activePrice.RowVersion+1)
	assertRowVersionConflict(t, err)
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

// --- 4. Go 层冻结 ---

func TestUpdatePublishedVersionRejectedInGo(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pub Version
	for _, v := range detail.Versions {
		if v.Status == VersionPublished {
			pub = v
			break
		}
	}
	_, err = e.svc.UpdateVersion(ctx, plan.ID, pub.ID, pub.RowVersion, VersionInput{
		QuotaResetStrategy: ResetBillingCycle,
		RenewalExtendsPeriod: true, RenewalResetsQuota: true,
		RenewalKeepsAddons: true, DeviceReleaseHours: 99, OveragePolicy: OveragePolicySuspend,
	})
	if !errors.Is(err, ErrSnapshotFrozen) && errCode(err) != errs.Conflict {
		t.Fatalf("期望冻结/冲突，得到 %v", err)
	}
}

// --- 6. 归档 ---

func TestArchivePlan(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)

	next, err := e.svc.ArchivePlan(ctx, plan.ID, detail.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := e.svc.GetPlan(ctx, plan.ID)
	if after.Status != PlanArchived || after.AllowNewPurchase {
		t.Fatalf("归档后 status=%s allow_new=%v", after.Status, after.AllowNewPurchase)
	}

	_, err = e.svc.ArchivePlan(ctx, plan.ID, next)
	if errCode(err) != errs.Conflict {
		t.Fatalf("重复归档应 409，得到 %v", err)
	}

	_, err = e.svc.CreatePrice(ctx, plan.ID, PriceInput{
		Currency: "CNY", UnitAmount: decimal.RequireFromString("1.00"),
		BillingInterval: "month", IntervalCount: 1,
	})
	if errCode(err) != errs.Conflict {
		t.Fatalf("归档后建价应 409，得到 %v", err)
	}
	_, err = e.svc.CreateVersion(ctx, plan.ID)
	if errCode(err) != errs.Conflict {
		t.Fatalf("归档后建版本应 409，得到 %v", err)
	}
	_, err = e.svc.UpdatePlan(ctx, plan.ID, next, PlanInput{
		Code: after.Code, Name: after.Name, Visibility: after.Visibility,
		AllowNewPurchase: false, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if errCode(err) != errs.Conflict {
		t.Fatalf("归档后改套餐应 409，得到 %v", err)
	}
}

// --- 7. 价格唯一 ---

func TestDuplicateActivePrice(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})

	// 撞唯一索引会中止整个测试事务，包在保存点里。
	err := inSavepoint(t, ctx, e, "sp_dup", func() error {
		_, err := e.svc.CreatePrice(ctx, plan.ID, PriceInput{
			Currency: "CNY", UnitAmount: decimal.RequireFromString("12.00"),
			BillingInterval: "month", IntervalCount: 1,
		})
		return err
	})
	if errCode(err) != errs.Conflict {
		t.Fatalf("重复在售价应 409，得到 %v", err)
	}

	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Prices) == 0 {
		t.Fatal("套餐没有价格")
	}
	p := detail.Prices[0]
	_, err = e.svc.ArchivePrice(ctx, plan.ID, p.ID, p.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreatePrice(ctx, plan.ID, PriceInput{
		Currency: "CNY", UnitAmount: decimal.RequireFromString("12.00"),
		BillingInterval: "month", IntervalCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- 8. 向导 ---

func TestWizardCreatePublishTrafficQuota(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	gb := int64(10)
	out, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "wiz"), Name: "向导", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: &gb,
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: true,
	})
	if err != nil || !out.Published {
		t.Fatalf("向导发布: err=%v published=%v", err, out.Published)
	}
	var ver *Version
	for i := range out.Plan.Versions {
		if out.Plan.Versions[i].ID == out.VersionID {
			ver = &out.Plan.Versions[i]
		}
	}
	if ver == nil {
		t.Fatal("版本缺失")
	}
	lim := ver.TrafficLimit()
	if lim == nil || *lim != 10*BytesPerGB {
		t.Fatalf("traffic.bytes=%v", lim)
	}
}

func TestWizardUpdateTrafficRollsVersion(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	created, _ := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "roll"), Name: "滚版本", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		Prices: []WizardPrice{{BillingInterval: "month", IntervalCount: 1,
			UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"}},
		Publish: true,
	})
	oldPub := created.VersionID
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
	if upd.Plan.CurrentVersionID == nil || *upd.Plan.CurrentVersionID == oldPub {
		t.Fatal("应产生新当前版本")
	}
	pubCount := 0
	for _, v := range upd.Plan.Versions {
		if v.Status == VersionPublished {
			pubCount++
		}
	}
	if pubCount < 2 {
		t.Fatalf("旧版本应仍为 published，count=%d", pubCount)
	}
	found := false
	for _, c := range upd.Changed {
		if strings.Contains(c, "额度") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Changed 应含额度说明: %v", upd.Changed)
	}
}

func TestWizardUpdatePriceOnly(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	created, _ := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "pr"), Name: "双币", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(10),
		Prices: []WizardPrice{
			{BillingInterval: "month", IntervalCount: 1, UnitAmount: decimal.RequireFromString("9.90"), Currency: "CNY"},
			{BillingInterval: "month", IntervalCount: 1, UnitAmount: decimal.RequireFromString("1.99"), Currency: "USD"},
		},
		Publish: true,
	})
	upd, err := e.svc.UpdatePlanComplete(ctx, created.Plan.ID, WizardUpdate{
		ExpectedRowVersion: created.Plan.RowVersion,
		Code:               created.Plan.Code,
		Name:               created.Plan.Name,
		Visibility:         VisibilityPublic,
		Prices: &[]WizardPrice{
			{BillingInterval: "month", IntervalCount: 1, UnitAmount: decimal.RequireFromString("10.90"), Currency: "CNY"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var cnyActive, usdActive int
	for _, p := range upd.Plan.Prices {
		if string(p.Currency) == "CNY" && p.Status == PriceActive {
			cnyActive++
			if !p.UnitAmount.Equal(decimal.RequireFromString("10.90")) {
				t.Fatalf("CNY 金额 %s", p.UnitAmount)
			}
		}
		if string(p.Currency) == "USD" && p.Status == PriceActive {
			usdActive++
			if !p.UnitAmount.Equal(decimal.RequireFromString("1.99")) {
				t.Fatalf("USD 不应变 %s", p.UnitAmount)
			}
		}
	}
	if cnyActive != 1 || usdActive != 1 {
		t.Fatalf("CNY active=%d USD active=%d", cnyActive, usdActive)
	}
	archivedCNY := 0
	for _, p := range upd.Plan.Prices {
		if string(p.Currency) == "CNY" && p.Status == PriceArchived {
			archivedCNY++
		}
	}
	if archivedCNY < 1 {
		t.Fatal("旧 CNY 应归档")
	}
	priceMsg := false
	for _, c := range upd.Changed {
		if strings.Contains(c, "价格") {
			priceMsg = true
		}
	}
	if !priceMsg {
		t.Fatalf("Changed: %v", upd.Changed)
	}
}

func TestWizardDuplicatePricePeriod(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	_, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code: uniqCode(t, "dup"), Name: "重复价", Visibility: VisibilityPublic,
			AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
		},
		TrafficGB: int64Ptr(1),
		Prices: []WizardPrice{
			{BillingInterval: "month", IntervalCount: 1, UnitAmount: decimal.RequireFromString("1.00"), Currency: "CNY"},
			{BillingInterval: "month", IntervalCount: 1, UnitAmount: decimal.RequireFromString("2.00"), Currency: "CNY"},
		},
		Publish: false,
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("重复周期币种应 422，得到 %v", err)
	}
}

// --- 9. 卖点 ---

func TestPlanHighlights(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	_, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "hl"), Name: "卖点", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true,
		Highlights: []string{"一", "二", "三", "四", "五", "六"},
	})
	if errCode(err) != errs.ValidationFailed {
		t.Fatalf("超过 5 条应 422，得到 %v", err)
	}
	if errFields(err)["highlights"] == "" {
		t.Fatal("fields 缺 highlights")
	}

	want := []string{"快", "稳", "省"}
	plan, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "hlok"), Name: "卖点OK", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if plan.Highlights[i] != want[i] {
			t.Fatalf("顺序 %v vs %v", plan.Highlights, want)
		}
	}
}

// --- 10. 门户与下单快照 ---
