package adminops

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 缺陷 12 的两半：
//   - 向导只回传人民币价时，不能顺手归档美元价与用户组专属价；
//   - 向导编辑是一个事务：最后一步（发布新版本）失败时，资料、价格、
//     新建的 draft 都不能留下。
func TestUpdatePlanCompleteAtomicPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)

	const (
		tenantID  = "73000000-0000-7000-8000-000000000001"
		actorID   = "73000000-0000-7000-8000-000000000011"
		groupID   = "73000000-0000-7000-8000-000000000012"
		productID = "73000000-0000-7000-8000-000000000021"
		planID    = "73000000-0000-7000-8000-000000000031"
		versionID = "73000000-0000-7000-8000-000000000041"
		poolID    = "73000000-0000-7000-8000-000000000051"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'wizard-atomic-pg18', 'Wizard Atomic PG18', 'CNY')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, 'wizard-atomic-admin@example.test', 'Wizard Admin', 'active')`, []any{tenantID, actorID}},
		{`INSERT INTO user_groups (id, tenant_id, code, name) VALUES ($2, $1, 'wizard-vip', 'Wizard VIP')`, []any{tenantID, groupID}},
		{`INSERT INTO node_pools (id, tenant_id, code, name) VALUES ($2, $1, 'wizard-pool', 'Wizard Pool')`, []any{tenantID, poolID}},
		{`INSERT INTO products (id, tenant_id, code, name, status) VALUES ($2, $1, 'wizard-atomic', 'Wizard Atomic', 'active')`, []any{tenantID, productID}},
		{`INSERT INTO plans (id, tenant_id, product_id, code, name, visibility, allow_new_purchase, allow_renewal, allow_upgrade, status)
		  VALUES ($3, $1, $2, 'wizard-atomic', 'Wizard Atomic', 'public', true, true, true, 'draft')`, []any{tenantID, productID, planID}},
		{`INSERT INTO plan_versions (id, tenant_id, plan_id, version) VALUES ($3, $1, $2, 1)`, []any{tenantID, planID, versionID}},
		// 冻结之前挂好额度与线路：冻结后快照子对象不可改。
		{`INSERT INTO quota_definitions (tenant_id, plan_version_id, metric, limit_value, unit, period)
		  VALUES ($1, $2, 'traffic.bytes', 107374182400, 'bytes', 'cycle')`, []any{tenantID, versionID}},
		{`INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id) VALUES ($1, $2, $3)`, []any{tenantID, versionID, poolID}},
		{`UPDATE plan_versions SET frozen_at=now() WHERE id=$1`, []any{versionID}},
		{`UPDATE plans SET current_version_id=$2, status='active' WHERE id=$1`, []any{planID, versionID}},
		{`INSERT INTO prices (tenant_id, product_id, currency, unit_amount, billing_interval, interval_count, status)
		  VALUES ($1, $2, 'CNY', 1000, 'month', 1, 'active'),
		         ($1, $2, 'USD', 150, 'month', 1, 'active')`, []any{tenantID, productID}},
		{`INSERT INTO prices (tenant_id, product_id, currency, unit_amount, billing_interval, interval_count, status, user_group_id)
		  VALUES ($1, $2, 'CNY', 800, 'month', 1, 'active', $3)`, []any{tenantID, productID, groupID}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed wizard atomic fixture: %v\nSQL: %s", err, seed.sql)
		}
	}

	activePrices := func() []string {
		t.Helper()
		rows, err := admin.Query(ctx, `
			SELECT currency::text, unit_amount, user_group_id IS NOT NULL
			  FROM prices WHERE tenant_id=$1 AND product_id=$2 AND status='active'`,
			tenantID, productID)
		if err != nil {
			t.Fatalf("read active prices: %v", err)
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var currency string
			var amount int64
			var grouped bool
			if err := rows.Scan(&currency, &amount, &grouped); err != nil {
				t.Fatalf("scan active price: %v", err)
			}
			out = append(out, fmt.Sprintf("%s/%d/group=%t", currency, amount, grouped))
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate active prices: %v", err)
		}
		slices.Sort(out)
		return out
	}
	snapshot := func() string {
		t.Helper()
		var name string
		var rowVersion int64
		var versions, pools, quotas, audits int
		if err := admin.QueryRow(ctx, `
			SELECT p.name, p.row_version,
			       (SELECT count(*)::int FROM plan_versions v WHERE v.plan_id=p.id),
			       (SELECT count(*)::int FROM plan_node_pools n WHERE n.tenant_id=p.tenant_id),
			       (SELECT count(*)::int FROM quota_definitions q WHERE q.tenant_id=p.tenant_id),
			       (SELECT count(*)::int FROM audit_events a WHERE a.tenant_id=p.tenant_id)
			  FROM plans p WHERE p.tenant_id=$1 AND p.id=$2`, tenantID, planID).Scan(
			&name, &rowVersion, &versions, &pools, &quotas, &audits); err != nil {
			t.Fatalf("read wizard snapshot: %v", err)
		}
		return fmt.Sprintf("name=%s row=%d versions=%d pools=%d quotas=%d audits=%d prices=%v",
			name, rowVersion, versions, pools, quotas, audits, activePrices())
	}
	rowVersion := func() int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM plans WHERE id=$1`, planID).Scan(&v); err != nil {
			t.Fatalf("read plan row_version: %v", err)
		}
		return v
	}

	svc := NewService(app, staticSalesCapability(true))

	// 1) 只回传人民币公开价：同币种旧价归档换新，美元价与用户组价不动。
	cnyOnly := []PlanPriceInput{{BillingInterval: "month", IntervalCount: 1, UnitAmount: 1200, Currency: "CNY"}}
	before := rowVersion()
	out, err := svc.UpdatePlanComplete(ctx, tenantID, planID, UpdatePlanCompleteInput{
		ActorID: actorID, ExpectedRowVersion: before,
		Code: "wizard-atomic", Name: "Wizard Atomic", Visibility: "public",
		Prices: &cnyOnly,
	})
	if err != nil {
		t.Fatalf("CNY-only wizard update: %v", err)
	}
	wantPrices := []string{"CNY/1200/group=false", "CNY/800/group=true", "USD/150/group=false"}
	if got := activePrices(); !slices.Equal(got, wantPrices) {
		t.Fatalf("active prices after CNY-only update=%v want %v", got, wantPrices)
	}
	if after := rowVersion(); after != before+1 {
		t.Fatalf("plan row_version=%d want %d", after, before+1)
	}
	if !slices.ContainsFunc(out.Changed, func(msg string) bool { return strings.HasPrefix(msg, "价格已更新") }) {
		t.Fatalf("changed=%v does not report the price change", out.Changed)
	}
	t.Log("marker=wizard_update_price_scope_ok")

	// 2) 改名 + 改价 + 改流量，最后一步发布因为线路里没有可服务节点失败：
	// 整个向导调用回滚，一样都不留。
	baseline := snapshot()
	traffic := int64(200)
	raise := []PlanPriceInput{{BillingInterval: "month", IntervalCount: 1, UnitAmount: 1500, Currency: "CNY"}}
	_, err = svc.UpdatePlanComplete(ctx, tenantID, planID, UpdatePlanCompleteInput{
		ActorID: actorID, ExpectedRowVersion: rowVersion(),
		Code: "wizard-atomic", Name: "Wizard Atomic Renamed", Visibility: "public",
		TrafficGB: &traffic, Prices: &raise,
	})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed ||
		he.Fields["pool_ids"] != "绑定池中至少需要一个可服务节点" {
		t.Fatalf("publish-step failure err=%v, want 422 pool_ids from the final publish step", err)
	}
	if got := snapshot(); got != baseline {
		t.Fatalf("failed wizard update left partial state\nbefore=%s\nafter =%s", baseline, got)
	}
	t.Log("marker=wizard_update_atomic_rollback_ok")
}
