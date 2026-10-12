package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// publishedFixture 返回已发布套餐的版本 id、价格 id 与管理员用户 id。
func publishedFixture(t *testing.T, e *env, ctx context.Context) (planID, versionID, priceID, adminID uuid.UUID) {
	t.Helper()
	ctx, adminID = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var verID uuid.UUID
	for _, v := range detail.Versions {
		if v.Status == VersionPublished {
			verID = v.ID
			break
		}
	}
	if verID == uuid.Nil {
		t.Fatal("无已发布版本")
	}
	return plan.ID, verID, detail.Prices[0].ID, adminID
}

// publishedWithEntitlementFixture 发布带一条 entitlement 的版本，返回版本 id 与 entitlement 行 id。
func publishedWithEntitlementFixture(t *testing.T, e *env, ctx context.Context) (versionID, entitlementID uuid.UUID) {
	t.Helper()
	ctx, _ = seedAdmin(t, ctx, e)
	plan, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "entf"), Name: "带权益", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ver, err := e.svc.CreateVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	lim := int64(1 << 30)
	verRV, err := e.svc.UpdateVersion(ctx, plan.ID, ver.ID, ver.RowVersion, VersionInput{
		QuotaResetStrategy: ResetBillingCycle, GraceKeepsService: true,
		RenewalExtendsPeriod: true, RenewalResetsQuota: true, RenewalKeepsAddons: true,
		DeviceReleaseHours: 24, OveragePolicy: OveragePolicySuspend,
		Entitlements: []Entitlement{{Code: "feat.a", Value: json.RawMessage(`true`)}},
		Quotas:       []Quota{{Metric: MetricTraffic, Limit: &lim, Unit: "bytes", Period: PeriodCycle}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreatePrice(ctx, plan.ID, PriceInput{
		Currency: "CNY", UnitAmount: decimal.RequireFromString("9.90"),
		BillingInterval: "month", IntervalCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	_, _, err = e.svc.PublishVersion(ctx, plan.ID, ver.ID, detail.RowVersion, verRV)
	if err != nil {
		t.Fatal(err)
	}
	var eid uuid.UUID
	if err := e.row(ctx, `SELECT id FROM entitlements WHERE plan_version_id = ? LIMIT 1`, ver.ID).Scan(&eid); err != nil {
		t.Fatal(err)
	}
	return ver.ID, eid
}

func draftFixture(t *testing.T, e *env, ctx context.Context) (planID, versionID uuid.UUID) {
	t.Helper()
	ctx, _ = seedAdmin(t, ctx, e)
	plan, err := e.svc.CreatePlan(ctx, PlanInput{
		Code: uniqCode(t, "dr"), Name: "草稿", Visibility: VisibilityPublic,
		AllowNewPurchase: true, AllowRenewal: true, AllowUpgrade: true, Highlights: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ver, err := e.svc.CreateVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return plan.ID, ver.ID
}

func TestFreezeTriggerPlanVersionUpdateGrace(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID, _, _ := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1",
		`UPDATE plan_versions SET grace_period_hours = grace_period_hours + 1 WHERE id = ?`, verID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerPlanVersionDelete(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID, _, _ := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1", `DELETE FROM plan_versions WHERE id = ?`, verID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerEntitlementInsert(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID, _, _ := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1",
		`INSERT INTO entitlements (plan_version_id, code, value) VALUES (?, 'x', 'true'::jsonb)`, verID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerEntitlementUpdate(t *testing.T) {
	e, ctx := newEnv(t)
	_, eid := publishedWithEntitlementFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1", `UPDATE entitlements SET value = '"off"'::jsonb WHERE id = ?`, eid)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerEntitlementDelete(t *testing.T) {
	e, ctx := newEnv(t)
	_, eid := publishedWithEntitlementFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1", `DELETE FROM entitlements WHERE id = ?`, eid)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerQuotaInsertUpdateDelete(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID, _, _ := publishedFixture(t, e, ctx)
	var qid uuid.UUID
	if err := e.row(ctx, `SELECT id FROM quota_definitions WHERE plan_version_id = ? LIMIT 1`, verID).Scan(&qid); err != nil {
		t.Fatal(err)
	}
	err := execSavepoint(t, ctx, e, "sp1",
		`INSERT INTO quota_definitions (plan_version_id, metric, limit_value, unit, period) VALUES (?, 'other', 1, 'bytes', 'cycle')`, verID)
	assertRestrictViolation(t, err)
	err = execSavepoint(t, ctx, e, "sp2", `UPDATE quota_definitions SET limit_value = limit_value WHERE id = ?`, qid)
	assertRestrictViolation(t, err)
	err = execSavepoint(t, ctx, e, "sp3", `DELETE FROM quota_definitions WHERE id = ?`, qid)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerPriceUpdateAmount(t *testing.T) {
	e, ctx := newEnv(t)
	_, _, priceID, _ := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1", `UPDATE prices SET unit_amount = unit_amount + 1 WHERE id = ?`, priceID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerPriceUnarchive(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, _ := e.svc.GetPlan(ctx, plan.ID)
	p := detail.Prices[0]
	_, err := e.svc.ArchivePrice(ctx, plan.ID, p.ID, p.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	err = execSavepoint(t, ctx, e, "sp1",
		`UPDATE prices SET status = 'active', row_version = row_version + 1 WHERE id = ?`, p.ID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerPriceDelete(t *testing.T) {
	e, ctx := newEnv(t)
	_, _, priceID, _ := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1", `DELETE FROM prices WHERE id = ?`, priceID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerInsertPublishedVersionRow(t *testing.T) {
	e, ctx := newEnv(t)
	planID, _, _, _ := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1",
		`INSERT INTO plan_versions (plan_id, version, status, frozen_at, quota_reset_strategy, grace_period_hours, grace_keeps_service, renewal_extends_period, renewal_resets_quota, renewal_keeps_addons, device_release_hours, row_version)
		 VALUES (?, 99, 'published', now(), 'billing_cycle', 0, true, true, true, true, 24, 1)`, planID)
	assertRestrictViolation(t, err)
}

func TestFreezeTriggerDraftVersionUpdateOK(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID := draftFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1",
		`UPDATE plan_versions SET grace_period_hours = 1 WHERE id = ?`, verID)
	if err != nil {
		t.Fatalf("草稿版本应可改: %v", err)
	}
}

func TestFreezeTriggerDraftChildInsertOK(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID := draftFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1",
		`INSERT INTO quota_definitions (plan_version_id, metric, limit_value, unit, period) VALUES (?, 'traffic.bytes', 100, 'bytes', 'cycle')`, verID)
	if err != nil {
		t.Fatalf("草稿子表应可插: %v", err)
	}
}

func TestFreezeTriggerPublishedVersionCreatedByOnly(t *testing.T) {
	e, ctx := newEnv(t)
	_, verID, _, adminID := publishedFixture(t, e, ctx)
	err := execSavepoint(t, ctx, e, "sp1",
		`UPDATE plan_versions SET created_by = ? WHERE id = ?`, adminID, verID)
	if err != nil {
		t.Fatalf("仅改 created_by 应成功: %v", err)
	}
}

func TestFreezeTriggerPriceArchiveOK(t *testing.T) {
	e, ctx := newEnv(t)
	_, _, priceID, _ := publishedFixture(t, e, ctx)
	var rv int64
	if err := e.row(ctx, `SELECT row_version FROM prices WHERE id = ?`, priceID).Scan(&rv); err != nil {
		t.Fatal(err)
	}
	err := execSavepoint(t, ctx, e, "sp1",
		`UPDATE prices SET status = 'archived', row_version = ? WHERE id = ? AND status = 'active'`, rv+1, priceID)
	if err != nil {
		t.Fatalf("active→archived 且 row_version+1 应成功: %v", err)
	}
}
