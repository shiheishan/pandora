// [INPUT]: 依赖 service.go 的 ListOrders / ListProviders、order_detail.go 的 GetOrder、revenue.go 的 ListRevenueAdjustments，依赖 catalog_sales_pg18_test.go 的 openCatalogSalesPG18 夹具与 catalog_sales_capability_test.go 的 staticSalesCapability
// [OUTPUT]: 对外提供 TestAdminFinanceReadsPG18（run-pg18-gates.sh 的 catalog_sales 域）
// [POS]: adminops 后台-05 读模型扩展的 PG18 集成门禁：订单状态多值白名单与按用户筛、收款渠道回退、详情复用列表行与开单人、渠道卡统计、收入调整登记人
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestAdminFinanceReadsPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)

	const (
		tenantID = "73200000-0000-7000-8000-000000000001"
		actorID  = "73200000-0000-7000-8000-000000000011"
		buyerA   = "73200000-0000-7000-8000-000000000012"
		buyerB   = "73200000-0000-7000-8000-000000000013"
		epayID   = "73200000-0000-7000-8000-000000000021"
		demoID   = "73200000-0000-7000-8000-000000000022"
		paidO    = "73200000-0000-7000-8000-000000000031" // 人工单，已入账（epay）
		openO    = "73200000-0000-7000-8000-000000000032" // 待支付，只有支付尝试
		balO     = "73200000-0000-7000-8000-000000000033" // 全额余额，已取消
	)
	// 下单主路径要带齐幂等键、预留与价格版本，与这里要验的读形状无关：
	// replica 模式跳过触发器与外键，CHECK 约束照常生效。
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO tenants (id, slug, display_name, default_currency, timezone)
		  VALUES ($1, 'finance-reads-pg18', 'Finance Reads PG18', 'CNY', 'Asia/Shanghai')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status)
		  VALUES ($2, $1, 'finance-admin@example.test', 'Admin', 'active'),
		         ($3, $1, 'buyer-a@example.test', 'Buyer A', 'active'),
		         ($4, $1, 'buyer-b@example.test', 'Buyer B', 'active')`, []any{tenantID, actorID, buyerA, buyerB}},
		{`INSERT INTO payment_providers (id, tenant_id, code, adapter, display_name, enabled)
		  VALUES ($2, $1, 'fr-epay', 'epay', '易支付', true),
		         ($3, $1, 'fr-demo', 'demo', '演示渠道', true)`, []any{tenantID, epayID, demoID}},
		{`INSERT INTO orders (id, tenant_id, order_no, user_id, kind, status, currency,
		    subtotal_amount, discount_amount, tax_amount, total_amount, balance_applied,
		    payable_amount, paid_amount, created_by, manual_reason, business_request_id, created_at)
		  VALUES ($2, $1, 'FR-PAID', $5, 'new', 'fulfilled', 'CNY', 3000, 0, 0, 3000, 0, 3000, 3000,
		          $7, '线下补单测试', gen_random_uuid(), now() - interval '3 minutes'),
		         ($3, $1, 'FR-OPEN', $5, 'new', 'pending_payment', 'CNY', 2000, 0, 0, 2000, 0, 2000, 0,
		          NULL, NULL, gen_random_uuid(), now() - interval '2 minutes'),
		         ($4, $1, 'FR-BAL', $6, 'new', 'cancelled', 'CNY', 1000, 0, 0, 1000, 1000, 0, 0,
		          NULL, NULL, gen_random_uuid(), now() - interval '1 minute')`,
			[]any{tenantID, paidO, openO, balO, buyerA, buyerB, actorID}},
		{`INSERT INTO order_items (tenant_id, order_id, product_id, price_id, plan_id, plan_version_id,
		    snapshot_product_name, snapshot_plan_name, snapshot_plan_version,
		    snapshot_interval, snapshot_interval_count, quantity, unit_amount, line_amount, currency)
		  VALUES ($1, $2, gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
		          '专业版', '专业版', 1, 'month', 3, 1, 3000, 3000, 'CNY')`, []any{tenantID, paidO}},
		// 已入账单：换过渠道（先试 demo 过期，后 epay 成功），渠道以入账为准。
		{`INSERT INTO payment_intents (tenant_id, order_id, provider_id, currency, amount, status, created_at, updated_at)
		  VALUES ($1, $2, $4, 'CNY', 3000, 'expired',   now() - interval '3 hours', now() - interval '3 hours'),
		         ($1, $2, $3, 'CNY', 3000, 'succeeded', now() - interval '2 minutes', now()),
		         ($1, $5, $3, 'CNY', 2000, 'failed',    now() - interval '2 hours', now() - interval '2 hours'),
		         ($1, $5, $4, 'CNY', 2000, 'created',   now() - interval '1 hour', now() - interval '1 hour')`,
			[]any{tenantID, paidO, epayID, demoID, openO}},
		{`INSERT INTO payments (tenant_id, order_id, provider_id, provider_payment_id, currency, amount, status, paid_at)
		  VALUES ($1, $2, $3, 'fr-epay-1', 'CNY', 3000, 'succeeded', now())`, []any{tenantID, paidO, epayID}},
		{`INSERT INTO payment_events (tenant_id, provider_id, provider_event_id, event_type, raw_payload, received_at)
		  VALUES ($1, $2, 'fr-evt-1', 'payment.succeeded', '{}', now() - interval '5 minutes')`, []any{tenantID, epayID}},
		{`INSERT INTO revenue_report_adjustments (tenant_id, currency, amount, reason, effective_on, created_by, idempotency_key)
		  VALUES ($1, 'CNY', 500, '线下收入补登测试', current_date, $2, 'fr-adj-1')`, []any{tenantID, actorID}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed finance reads: %v\nSQL: %s", err, seed.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit finance fixture: %v", err)
	}

	svc := NewService(app, staticSalesCapability(true))
	list := func(in ListOrdersInput) map[string]OrderRow {
		t.Helper()
		rows, total, err := svc.ListOrders(ctx, tenantID, in)
		if err != nil {
			t.Fatalf("list orders %+v: %v", in, err)
		}
		if int(total) != len(rows) {
			t.Fatalf("list orders %+v total=%d rows=%d", in, total, len(rows))
		}
		out := map[string]OrderRow{}
		for _, r := range rows {
			out[r.OrderNo] = r
		}
		return out
	}
	keys := func(m map[string]OrderRow) []string {
		out := []string{}
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}

	// --- 状态多值白名单、按用户筛、负 offset ---
	if got := keys(list(ListOrdersInput{Status: "paid,fulfilled"})); !slices.Equal(got, []string{"FR-PAID"}) {
		t.Fatalf("status=paid,fulfilled -> %v", got)
	}
	if got := keys(list(ListOrdersInput{Status: " pending_payment , cancelled ,cancelled"})); !slices.Equal(got, []string{"FR-BAL", "FR-OPEN"}) {
		t.Fatalf("status=pending_payment,cancelled -> %v", got)
	}
	if got := keys(list(ListOrdersInput{UserID: buyerB, Offset: -5})); !slices.Equal(got, []string{"FR-BAL"}) {
		t.Fatalf("user_id=buyerB -> %v", got)
	}
	for _, bad := range []ListOrdersInput{{Status: "pending"}, {Status: "%"}, {Status: "paid,_aid"}, {UserID: "not-a-uuid"}} {
		var he *httpx.Error
		if _, _, err := svc.ListOrders(ctx, tenantID, bad); !errors.As(err, &he) || he.Code != httpx.CodeBadRequest {
			t.Fatalf("list orders %+v err=%v, want 400", bad, err)
		}
	}
	t.Log("marker=admin_orders_status_whitelist_ok")

	// --- 收款渠道：入账优先，其次最近一次支付尝试，都没有为空 ---
	all := list(ListOrdersInput{})
	strp := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for no, want := range map[string][2]string{
		"FR-PAID": {"fr-epay", "易支付"},
		"FR-OPEN": {"fr-demo", "演示渠道"},
		"FR-BAL":  {"<nil>", "<nil>"},
	} {
		r := all[no]
		if strp(r.ProviderCode) != want[0] || strp(r.ProviderName) != want[1] {
			t.Fatalf("%s provider=%s/%s want %s/%s", no, strp(r.ProviderCode), strp(r.ProviderName), want[0], want[1])
		}
	}
	if all["FR-BAL"].BalanceApplied != 1000 || all["FR-PAID"].PlanName != "专业版" {
		t.Fatalf("balance_applied=%d plan_name=%q", all["FR-BAL"].BalanceApplied, all["FR-PAID"].PlanName)
	}
	t.Log("marker=admin_orders_provider_fallback_ok")

	// --- 详情复用列表行：首项快照不再是零值；开单人 ---
	detail, err := svc.GetOrder(ctx, tenantID, paidO)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if detail.PlanName != "专业版" || detail.Interval != "month" || detail.IntervalCount != 3 || detail.ItemCount != 1 {
		t.Fatalf("detail first-item snapshot=%q %q %d %d", detail.PlanName, detail.Interval, detail.IntervalCount, detail.ItemCount)
	}
	if strp(detail.CreatedBy) != actorID || strp(detail.CreatedByEmail) != "finance-admin@example.test" ||
		strp(detail.ProviderName) != "易支付" || detail.UserID != buyerA {
		t.Fatalf("detail created_by=%s email=%s provider=%s user=%s",
			strp(detail.CreatedBy), strp(detail.CreatedByEmail), strp(detail.ProviderName), detail.UserID)
	}
	if open, err := svc.GetOrder(ctx, tenantID, openO); err != nil || open.CreatedBy != nil || open.CreatedByEmail != nil {
		t.Fatalf("portal order detail created_by=%v email=%v err=%v, want empty", open.CreatedBy, open.CreatedByEmail, err)
	}
	t.Log("marker=admin_order_detail_shares_row_ok")

	// --- 渠道卡统计 ---
	providers, err := svc.ListProviders(ctx, tenantID)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	byCode := map[string]ProviderRow{}
	for _, p := range providers {
		byCode[p.Code] = p
	}
	epay, demo := byCode["fr-epay"], byCode["fr-demo"]
	if epay.Today["CNY"] != 3000 || len(epay.Today) != 1 || len(demo.Today) != 0 {
		t.Fatalf("today epay=%v demo=%v, want epay CNY 3000 and demo empty", epay.Today, demo.Today)
	}
	// epay：近 24 小时进入终态的两次尝试（成功、失败）→ 0.5；demo 只有 3 小时前过期的一次 → 0。
	if epay.SuccessRate24h == nil || *epay.SuccessRate24h != 0.5 || demo.SuccessRate24h == nil || *demo.SuccessRate24h != 0 {
		t.Fatalf("success rate epay=%v demo=%v, want 0.5 and 0", epay.SuccessRate24h, demo.SuccessRate24h)
	}
	if epay.LastCallbackAt == nil || demo.LastCallbackAt != nil {
		t.Fatalf("last callback epay=%v demo=%v", epay.LastCallbackAt, demo.LastCallbackAt)
	}
	t.Log("marker=admin_provider_stats_ok")

	// --- 收入调整登记人 ---
	adjustments, err := svc.ListRevenueAdjustments(ctx, tenantID, "")
	if err != nil || len(adjustments) != 1 || strp(adjustments[0].CreatedByEmail) != "finance-admin@example.test" {
		t.Fatalf("revenue adjustments=%+v err=%v, want one with the admin email", adjustments, err)
	}
	t.Log("marker=admin_revenue_adjustment_creator_ok")
}
