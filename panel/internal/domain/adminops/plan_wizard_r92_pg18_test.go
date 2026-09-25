// [INPUT]: 依赖 plan_wizard.go / plan_wizard_update.go 的向导用例、catalog.go 的 UpdatePlanVersion，依赖 catalog_sales_pg18_test.go 的 openCatalogSalesPG18 与 catalog_sales_capability_test.go 的 staticSalesCapability
// [OUTPUT]: 对外提供 TestPlanWizardKeepsSettingsPG18、TestPlanThrottleDecoupledPG18（run-pg18-gates.sh 的 catalog_sales 域）
// [POS]: adminops 第 4 阶段套餐修复的 PG18 门禁：向导编辑的三态与继承、上架时间窗保留（R92），限速与超额策略解耦（R99，迁移删掉 plan_versions_throttle_exact）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	r92Tenant  = "8c000000-0000-7000-8000-000000000001"
	r92Actor   = "8c000000-0000-7000-8000-000000000011"
	r92Pool    = "8c000000-0000-7000-8000-000000000051"
	r92Server  = "8c000000-0000-7000-8000-000000000052"
	r92Node    = "8c000000-0000-7000-8000-000000000053"
	r92Product = "8c000000-0000-7000-8000-000000000021"
	r92Plan    = "8c000000-0000-7000-8000-000000000031"
	r92V1      = "8c000000-0000-7000-8000-000000000041"
)

// seedR92 建一个已上架、当前版本带满高级设置的套餐，线路里有一个可服务节点，
// 让向导编辑能一路发布成功。v1 故意是旧口径的 throttle 策略 + 2000 kbps。
func seedR92(t *testing.T, ctx context.Context, admin *pgxpool.Pool) (from, until time.Time) {
	t.Helper()
	from = time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	until = time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'wizard-r92-pg18', 'Wizard R92', 'CNY')`, []any{r92Tenant}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, 'wizard-r92@example.test', 'Wizard R92', 'active')`, []any{r92Tenant, r92Actor}},
		{`INSERT INTO node_pools (id, tenant_id, code, name) VALUES ($2, $1, 'r92-pool', 'R92 Pool')`, []any{r92Tenant, r92Pool}},
		{`INSERT INTO servers (id, tenant_id, name, status) VALUES ($2, $1, 'r92-server', 'ready')`, []any{r92Tenant, r92Server}},
		{`INSERT INTO nodes (id, tenant_id, name, pool_id, status, node_type, server_host, server_port, server_id, serving_status, protocol_schema_version, config_validated_at)
		  VALUES ($2, $1, 'r92-node', $3, 'active', 'vless', 'n.invalid', 443, $4, 'active', 1, now())`, []any{r92Tenant, r92Node, r92Pool, r92Server}},
		{`INSERT INTO products (id, tenant_id, code, name, status) VALUES ($2, $1, 'wizard-r92', 'Wizard R92', 'active')`, []any{r92Tenant, r92Product}},
		{`INSERT INTO plans (id, tenant_id, product_id, code, name, visibility, visible_from, visible_until, status)
		  VALUES ($3, $1, $2, 'wizard-r92', 'Wizard R92', 'public', $4, $5, 'draft')`, []any{r92Tenant, r92Product, r92Plan, from, until}},
		{`INSERT INTO plan_versions (id, tenant_id, plan_id, version, quota_reset_strategy, quota_reset_day,
		    grace_period_hours, grace_keeps_service, renewal_extends_period, renewal_resets_quota, renewal_keeps_addons,
		    max_devices, max_concurrent, device_release_hours, overage_policy, throttle_kbps, notes)
		  VALUES ($3, $1, $2, 1, 'fixed_day', 5, 48, false, false, false, false, 3, 2, 12, 'throttle', 2000, 'fixture note')`, []any{r92Tenant, r92Plan, r92V1}},
		{`INSERT INTO entitlements (tenant_id, plan_version_id, code, value) VALUES ($1, $2, 'support.priority', '{"tier":2}')`, []any{r92Tenant, r92V1}},
		{`INSERT INTO quota_definitions (tenant_id, plan_version_id, metric, limit_value, unit, period) VALUES
		    ($1, $2, 'traffic.bytes', 107374182400, 'bytes', 'cycle'),
		    ($1, $2, 'devices.active', 3, 'count', 'cycle'),
		    ($1, $2, 'requests.count', 1000, 'count', 'day')`, []any{r92Tenant, r92V1}},
		{`INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id) VALUES ($1, $2, $3)`, []any{r92Tenant, r92V1, r92Pool}},
		{`UPDATE plan_versions SET status='published', frozen_at=now() WHERE id=$1`, []any{r92V1}},
		{`UPDATE plans SET current_version_id=$2, status='active' WHERE id=$1`, []any{r92Plan, r92V1}},
		{`INSERT INTO prices (tenant_id, product_id, currency, unit_amount, billing_interval, interval_count, status)
		  VALUES ($1, $2, 'CNY', 1000, 'month', 1, 'active')`, []any{r92Tenant, r92Product}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed R92 fixture: %v\nSQL: %s", err, seed.sql)
		}
	}
	return from, until
}

func TestPlanWizardKeepsSettingsPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)
	from, until := seedR92(t, ctx, admin)
	svc := NewService(app, staticSalesCapability(true))

	// 请求体走 JSON 解码，三态才是真实的三态：缺省、null、数字。
	update := func(body string) *UpdatePlanCompleteOutput {
		t.Helper()
		var rv int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM plans WHERE id=$1`, r92Plan).Scan(&rv); err != nil {
			t.Fatal(err)
		}
		var in UpdatePlanCompleteInput
		raw := `{"expected_row_version":` + jsonInt(rv) + `,"code":"wizard-r92","name":"Wizard R92","visibility":"public","sort_order":0` + body + `}`
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		in.ActorID = r92Actor
		out, err := svc.UpdatePlanComplete(ctx, r92Tenant, r92Plan, in)
		if err != nil {
			t.Fatalf("wizard update %s: %v", body, err)
		}
		return out
	}
	current := func(out *UpdatePlanCompleteOutput) *VersionRow {
		t.Helper()
		v := currentVersion(out.Plan)
		if v == nil {
			t.Fatal("no current version")
		}
		return v
	}
	quota := func(v *VersionRow, metric string) *QuotaInput {
		for i := range v.Quotas {
			if v.Quotas[i].Metric == metric {
				return &v.Quotas[i]
			}
		}
		return nil
	}
	window := func(out *UpdatePlanCompleteOutput) {
		t.Helper()
		if out.Plan.VisibleFrom == nil || out.Plan.VisibleUntil == nil ||
			!out.Plan.VisibleFrom.Equal(from) || !out.Plan.VisibleUntil.Equal(until) {
			t.Fatalf("visible window lost: from=%v until=%v want %v %v", out.Plan.VisibleFrom, out.Plan.VisibleUntil, from, until)
		}
	}

	// 1) 只改流量：新版本继承全部高级设置，策略改写成 suspend，限速照样继承。
	out := update(`,"traffic_gb":200`)
	v := current(out)
	if v.ID == r92V1 || v.Version != 2 {
		t.Fatalf("expected a rolled v2, got %+v", v)
	}
	if v.QuotaResetStrategy != "fixed_day" || v.QuotaResetDay == nil || *v.QuotaResetDay != 5 ||
		v.GracePeriodHours != 48 || v.GraceKeepsService || v.RenewalExtendsPeriod || v.RenewalResetsQuota || v.RenewalKeepsAddons ||
		v.MaxConcurrent == nil || *v.MaxConcurrent != 2 || v.DeviceReleaseHours != 12 ||
		v.Notes == nil || *v.Notes != "fixture note" {
		t.Fatalf("advanced settings not inherited: %+v", v)
	}
	if v.OveragePolicy != "suspend" || v.ThrottleKbps == nil || *v.ThrottleKbps != 2000 ||
		v.MaxDevices == nil || *v.MaxDevices != 3 {
		t.Fatalf("policy/throttle/devices: %+v", v)
	}
	if len(v.Entitlements) != 1 || v.Entitlements[0].Code != "support.priority" {
		t.Fatalf("entitlements not inherited: %+v", v.Entitlements)
	}
	var ent struct{ Tier int }
	if err := json.Unmarshal(v.Entitlements[0].Value, &ent); err != nil || ent.Tier != 2 {
		t.Fatalf("entitlement value %s: %v", v.Entitlements[0].Value, err)
	}
	if q := quota(v, "traffic.bytes"); q == nil || q.Limit == nil || *q.Limit != 200*bytesPerGB {
		t.Fatalf("traffic quota: %+v", q)
	}
	if q := quota(v, "requests.count"); q == nil || q.Limit == nil || *q.Limit != 1000 || q.Period != "day" {
		t.Fatalf("extra quota not inherited: %+v", q)
	}
	if q := quota(v, "devices.active"); q == nil || q.Limit == nil || *q.Limit != 3 {
		t.Fatalf("devices quota: %+v", q)
	}
	if len(v.PoolIDs) != 1 || v.PoolIDs[0] != r92Pool {
		t.Fatalf("pools not inherited: %v", v.PoolIDs)
	}
	window(out)

	// 2) max_devices 显式 null = 改回不限；限速改成 5000。
	out = update(`,"max_devices":null,"throttle_kbps":5000`)
	v = current(out)
	if v.Version != 3 || v.MaxDevices != nil || quota(v, "devices.active") != nil ||
		v.ThrottleKbps == nil || *v.ThrottleKbps != 5000 || v.GracePeriodHours != 48 {
		t.Fatalf("null devices / new throttle: %+v", v)
	}
	window(out)

	// 3) throttle_kbps 显式 null = 不限速。
	out = update(`,"throttle_kbps":null`)
	v = current(out)
	if v.Version != 4 || v.ThrottleKbps != nil || v.MaxDevices != nil {
		t.Fatalf("null throttle: %+v", v)
	}

	// 4) 流量给当前值、其余缺省：额度没变，不滚新版本；时间窗还在。
	out = update(`,"traffic_gb":200`)
	if v = current(out); v.Version != 4 {
		t.Fatalf("unchanged quota rolled a version: v%d", v.Version)
	}
	window(out)

	// 5) 非法三态值在事务外拦成 422。
	var rv int64
	if err := admin.QueryRow(ctx, `SELECT row_version FROM plans WHERE id=$1`, r92Plan).Scan(&rv); err != nil {
		t.Fatal(err)
	}
	zero := 0
	_, err := svc.UpdatePlanComplete(ctx, r92Tenant, r92Plan, UpdatePlanCompleteInput{
		ActorID: r92Actor, ExpectedRowVersion: rv, Code: "wizard-r92", Name: "Wizard R92", Visibility: "public",
		MaxDevices: OptionalInt{Set: true, Value: &zero}, ThrottleKbps: OptionalInt{Set: true, Value: &zero},
	})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["max_devices"] == "" || he.Fields["throttle_kbps"] == "" {
		t.Fatalf("zero devices/throttle err=%v", err)
	}
}

func TestPlanThrottleDecoupledPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)
	const (
		tenant = "8c000000-0000-7000-8000-000000000101"
		actor  = "8c000000-0000-7000-8000-000000000111"
	)
	for _, sql := range []string{
		`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ('` + tenant + `', 'throttle-r99-pg18', 'Throttle R99', 'CNY')`,
		`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ('` + actor + `', '` + tenant + `', 'throttle-r99@example.test', 'Throttle', 'active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	svc := NewService(app, staticSalesCapability(true))

	// 向导新建：限速正常生效（以前 suspend + 限速必定 422），设备 0 = 不限。
	throttle, devices := 1000, 0
	created, err := svc.CreatePlanComplete(ctx, tenant, CreatePlanCompleteInput{
		ActorID: actor, Code: "throttle-r99", Name: "Throttle R99",
		ThrottleKbps: &throttle, MaxDevices: &devices,
	})
	if err != nil {
		t.Fatalf("create with throttle: %v", err)
	}
	var policy string
	var kbps, maxDevices *int
	if err := admin.QueryRow(ctx, `SELECT overage_policy, throttle_kbps, max_devices FROM plan_versions WHERE id=$1`,
		created.VersionID).Scan(&policy, &kbps, &maxDevices); err != nil {
		t.Fatal(err)
	}
	if policy != "suspend" || kbps == nil || *kbps != 1000 || maxDevices != nil {
		t.Fatalf("created version policy=%s throttle=%v devices=%v", policy, kbps, maxDevices)
	}

	// 版本编辑：throttle / metered_billing 策略拒绝，省略策略按 suspend 落库。
	planID := created.Plan.ID
	rowVersion := func() int64 {
		var rv int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM plan_versions WHERE id=$1`, created.VersionID).Scan(&rv); err != nil {
			t.Fatal(err)
		}
		return rv
	}
	for _, bad := range []string{"throttle", "metered_billing"} {
		_, err := svc.UpdatePlanVersion(ctx, tenant, planID, created.VersionID, VersionSemanticsInput{
			ActorID: actor, ExpectedRowVersion: rowVersion(), QuotaResetStrategy: "billing_cycle",
			OveragePolicy: bad, ThrottleKbps: &throttle,
		})
		var he *httpx.Error
		if !errors.As(err, &he) || he.Fields["overage_policy"] == "" {
			t.Fatalf("policy %s err=%v", bad, err)
		}
	}
	faster := 3000
	if _, err := svc.UpdatePlanVersion(ctx, tenant, planID, created.VersionID, VersionSemanticsInput{
		ActorID: actor, ExpectedRowVersion: rowVersion(), QuotaResetStrategy: "billing_cycle",
		ThrottleKbps: &faster,
	}); err != nil {
		t.Fatalf("omitted policy with throttle: %v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT overage_policy, throttle_kbps FROM plan_versions WHERE id=$1`,
		created.VersionID).Scan(&policy, &kbps); err != nil {
		t.Fatal(err)
	}
	if policy != "suspend" || kbps == nil || *kbps != 3000 {
		t.Fatalf("edited version policy=%s throttle=%v", policy, kbps)
	}

	// 约束已删：数据库本身也接受 suspend + 限速。
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='plan_versions_throttle_exact')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("plan_versions_throttle_exact still present")
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
