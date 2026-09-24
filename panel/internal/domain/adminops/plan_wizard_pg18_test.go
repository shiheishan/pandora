// [INPUT]: 依赖 plan_wizard.go 的 CreatePlanComplete，依赖 catalog_sales_pg18_test.go 的 openCatalogSalesPG18 夹具与 catalog_sales_capability_test.go 的 staticSalesCapability
// [OUTPUT]: 对外提供 TestCreatePlanCompleteAtomicPG18（run-pg18-gates.sh 的 catalog_sales 域）
// [POS]: adminops 向导新建的 PG18 集成门禁：发布失败时一行不留（旧实现留下已归档空壳），成功时返回建成后的详情与建版本人
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 缺陷 12 的同类：向导新建以前逐步提交、失败时归档已建套餐补救，留下占着
// code 的已归档空壳。现在是一个事务：最后一步发布失败，产品、套餐、版本、
// 额度、线路、价格、审计一样都不留，同一个 code 可以立刻再用。
func TestCreatePlanCompleteAtomicPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)

	const (
		tenantID = "73100000-0000-7000-8000-000000000001"
		actorID  = "73100000-0000-7000-8000-000000000011"
		poolID   = "73100000-0000-7000-8000-000000000051"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'wizard-create-pg18', 'Wizard Create PG18', 'CNY')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, 'wizard-create-admin@example.test', 'Wizard Admin', 'active')`, []any{tenantID, actorID}},
		// 线路里没有任何节点：发布的「池里至少一个可服务节点」前置条件必然失败。
		{`INSERT INTO node_pools (id, tenant_id, code, name) VALUES ($2, $1, 'wizard-create-pool', 'Wizard Create Pool')`, []any{tenantID, poolID}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed wizard create fixture: %v\nSQL: %s", err, seed.sql)
		}
	}
	footprint := func() string {
		t.Helper()
		var products, plans, versions, pools, quotas, prices, audits int
		if err := admin.QueryRow(ctx, `
			SELECT (SELECT count(*)::int FROM products WHERE tenant_id=$1),
			       (SELECT count(*)::int FROM plans WHERE tenant_id=$1),
			       (SELECT count(*)::int FROM plan_versions WHERE tenant_id=$1),
			       (SELECT count(*)::int FROM plan_node_pools WHERE tenant_id=$1),
			       (SELECT count(*)::int FROM quota_definitions WHERE tenant_id=$1),
			       (SELECT count(*)::int FROM prices WHERE tenant_id=$1),
			       (SELECT count(*)::int FROM audit_events WHERE tenant_id=$1)`,
			tenantID).Scan(&products, &plans, &versions, &pools, &quotas, &prices, &audits); err != nil {
			t.Fatalf("read wizard footprint: %v", err)
		}
		return fmt.Sprintf("products=%d plans=%d versions=%d pools=%d quotas=%d prices=%d audits=%d",
			products, plans, versions, pools, quotas, prices, audits)
	}

	svc := NewService(app, staticSalesCapability(true))
	traffic := int64(100)
	devices := 3
	in := CreatePlanCompleteInput{
		ActorID: actorID, Code: "wizard-create", Name: "Wizard Create",
		TrafficGB: &traffic, MaxDevices: &devices, PoolIDs: []string{poolID},
		Prices:  []PlanPriceInput{{BillingInterval: "month", IntervalCount: 1, UnitAmount: 1500, Currency: "CNY"}},
		Publish: true,
	}

	// 1) 发布这最后一步失败：整个向导回滚。
	empty := footprint()
	_, err := svc.CreatePlanComplete(ctx, tenantID, in)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed ||
		he.Fields["pool_ids"] != "绑定池中至少需要一个可服务节点" {
		t.Fatalf("publish-step failure err=%v, want 422 pool_ids from the final publish step", err)
	}
	if got := footprint(); got != empty {
		t.Fatalf("failed wizard create left rows behind\nbefore=%s\nafter =%s", empty, got)
	}
	t.Log("marker=wizard_create_atomic_rollback_ok")

	// 2) 同一个 code 立刻可用；不发布时建成草稿，响应是建成后的详情。
	in.Publish = false
	out, err := svc.CreatePlanComplete(ctx, tenantID, in)
	if err != nil {
		t.Fatalf("draft wizard create: %v", err)
	}
	if out.Published || out.Plan == nil || out.Plan.Code != "wizard-create" || out.Plan.Status != "draft" {
		t.Fatalf("draft create output=%+v", out)
	}
	if len(out.Plan.Versions) != 1 || out.Plan.Versions[0].ID != out.VersionID ||
		len(out.Plan.Prices) != 1 || len(out.PriceIDs) != 1 || out.Plan.Prices[0].ID != out.PriceIDs[0] {
		t.Fatalf("draft create detail versions=%+v prices=%+v ids=%v", out.Plan.Versions, out.Plan.Prices, out.PriceIDs)
	}
	v := out.Plan.Versions[0]
	if v.CreatedByEmail == nil || *v.CreatedByEmail != "wizard-create-admin@example.test" {
		t.Fatalf("version created_by_email=%v, want the wizard actor", v.CreatedByEmail)
	}
	if len(v.PoolIDs) != 1 || v.PoolIDs[0] != poolID || v.MaxDevices == nil || *v.MaxDevices != 3 {
		t.Fatalf("draft version pools=%v max_devices=%v", v.PoolIDs, v.MaxDevices)
	}
	var trafficLimit *int64
	for _, q := range v.Quotas {
		if q.Metric == "traffic.bytes" {
			trafficLimit = q.Limit
		}
	}
	if trafficLimit == nil || *trafficLimit != 100*bytesPerGB {
		t.Fatalf("draft version traffic quota=%v, want 100 GiB", trafficLimit)
	}
	t.Log("marker=wizard_create_returns_built_detail_ok")
}
