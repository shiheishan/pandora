// [INPUT]: 依赖 handlers.go 的 listPlans / previewCoupon / myCommission、my_orders.go 的 listMyOrders / myOrderDetail，依赖 domain/billing 的 NewService，依赖 platform/pg18test 打开 public_api 域的一次性库
// [OUTPUT]: 对外提供 TestPortalCommerceReadsPG18
// [POS]: api/public 门户-03/04/06 字段扩展的 PG18 集成门禁：目录的重置策略与续费变更开关、优惠码试算的流量包形态与券面、订单筛选段计数与详情扩展、佣金概况与转出记录
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestPortalCommerceReadsPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)

	const (
		tenant   = "7b100000-0000-4000-8000-000000000001"
		user     = "7b100000-0000-4000-8000-000000000011"
		other    = "7b100000-0000-4000-8000-000000000012"
		refereeA = "7b100000-0000-4000-8000-000000000013"
		refereeB = "7b100000-0000-4000-8000-000000000014"
		product  = "7b100000-0000-4000-8000-000000000021"
		plan     = "7b100000-0000-4000-8000-000000000022"
		version  = "7b100000-0000-4000-8000-000000000023"
		price    = "7b100000-0000-4000-8000-000000000024"
		pack     = "7b100000-0000-4000-8000-000000000025"
		coupon   = "7b100000-0000-4000-8000-000000000026"
		provider = "7b100000-0000-4000-8000-000000000027"
		sub      = "7b100000-0000-4000-8000-000000000028"
		openO    = "7b100000-0000-4000-8000-000000000031"
		doneO    = "7b100000-0000-4000-8000-000000000032"
		txnMine  = "7b100000-0000-4000-8000-000000000041"
		txnOther = "7b100000-0000-4000-8000-000000000042"
	)
	// 读路径的素材：replica 模式跳过触发器与外键（下单主路径的整套预留与
	// 幂等与这里无关），CHECK 约束照常生效。
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
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'portal-commerce-pg18', 'Portal Commerce', 'CNY')`, []any{tenant}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES
			($2, $1, 'buyer@commerce.invalid', 'Buyer', 'active'),
			($3, $1, 'other@commerce.invalid', 'Other', 'active'),
			($4, $1, 'friend-a@commerce.invalid', 'Friend A', 'active'),
			($5, $1, 'friend-b@commerce.invalid', 'Friend B', 'active')`, []any{tenant, user, other, refereeA, refereeB}},
		// 目录：一个已发布、每月 5 日重置、允许续费但不允许变更的套餐。
		{`INSERT INTO products (id, tenant_id, code, name, status) VALUES ($2, $1, 'pc-pro', '专业版', 'active')`, []any{tenant, product}},
		{`INSERT INTO plans (id, tenant_id, product_id, code, name, visibility, status, allow_renewal, allow_upgrade, current_version_id)
			VALUES ($2, $1, $3, 'pc-pro', '专业版', 'public', 'active', true, false, $4)`, []any{tenant, plan, product, version}},
		{`INSERT INTO plan_versions (id, tenant_id, plan_id, version, status, frozen_at, quota_reset_strategy, quota_reset_day)
			VALUES ($2, $1, $3, 1, 'published', now(), 'fixed_day', 5)`, []any{tenant, version, plan}},
		{`INSERT INTO prices (id, tenant_id, product_id, currency, unit_amount, billing_interval, interval_count, status)
			VALUES ($2, $1, $3, 'CNY', 3000, 'month', 1, 'active')`, []any{tenant, price, product}},
		{`INSERT INTO traffic_packs (id, tenant_id, name, traffic_bytes, currency, unit_amount)
			VALUES ($2, $1, '100 GB', 107374182400, 'CNY', 2000)`, []any{tenant, pack}},
		{`INSERT INTO coupons (id, tenant_id, code, name, discount_type, discount_value, currency, max_redemptions_per_user)
			VALUES ($2, $1, 'PC10', '九折', 'percent', 1000, 'CNY', 5)`, []any{tenant, coupon}},
		{`INSERT INTO payment_providers (id, tenant_id, code, adapter, display_name, enabled)
			VALUES ($2, $1, 'pc-epay', 'epay', '易支付', true)`, []any{tenant, provider}},
		{`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id, status,
			snapshot_currency, snapshot_amount, current_period_end)
			VALUES ($2, $1, $3, $4, $5, 'active', 'CNY', 2700, '2030-01-31T00:00:00Z')`, []any{tenant, sub, user, plan, version}},
		// 订单：本人 5 张分落四个筛选段，另有别人的 1 张。
		{`INSERT INTO orders (id, tenant_id, order_no, user_id, kind, status, currency,
			subtotal_amount, discount_amount, tax_amount, total_amount, balance_applied,
			payable_amount, paid_amount, refunded_amount, coupon_id, subscription_id, business_request_id, created_at)
			VALUES
			($2, $1, 'PC-OPEN', $4, 'new', 'pending_payment', 'CNY', 3000, 0, 0, 3000, 0, 3000, 0, 0, NULL, NULL, gen_random_uuid(), now() - interval '5 minutes'),
			($3, $1, 'PC-DONE', $4, 'new', 'fulfilled', 'CNY', 3000, 300, 0, 2700, 0, 2700, 2700, 0, $6, $7, gen_random_uuid(), now() - interval '4 minutes'),
			(gen_random_uuid(), $1, 'PC-TOPUP', $4, 'topup', 'paid', 'CNY', 1000, 0, 0, 1000, 0, 1000, 1000, 0, NULL, NULL, gen_random_uuid(), now() - interval '3 minutes'),
			(gen_random_uuid(), $1, 'PC-GONE', $4, 'new', 'cancelled', 'CNY', 3000, 0, 0, 3000, 0, 3000, 0, 0, NULL, NULL, gen_random_uuid(), now() - interval '2 minutes'),
			(gen_random_uuid(), $1, 'PC-BACK', $4, 'new', 'refunded', 'CNY', 3000, 0, 0, 3000, 0, 3000, 3000, 3000, NULL, NULL, gen_random_uuid(), now() - interval '1 minute'),
			(gen_random_uuid(), $1, 'PC-OTHER', $5, 'new', 'pending_payment', 'CNY', 3000, 0, 0, 3000, 0, 3000, 0, 0, NULL, NULL, gen_random_uuid(), now())`,
			[]any{tenant, openO, doneO, user, other, coupon, sub}},
		{`INSERT INTO order_items (tenant_id, order_id, product_id, price_id, plan_id, plan_version_id,
			snapshot_product_name, snapshot_plan_name, snapshot_plan_version,
			snapshot_interval, snapshot_interval_count, quantity, unit_amount, line_amount, currency)
			VALUES ($1, $2, $4, $5, $6, $7, '专业版', '专业版', 1, 'month', 1, 1, 3000, 3000, 'CNY'),
			       ($1, $3, $4, $5, $6, $7, '专业版', '专业版', 1, 'month', 1, 1, 3000, 3000, 'CNY')`,
			[]any{tenant, openO, doneO, product, price, plan, version}},
		{`INSERT INTO payments (tenant_id, order_id, provider_id, provider_payment_id, currency, amount, method)
			VALUES ($1, $2, $3, 'pc-epay-1', 'CNY', 2700, 'alipay')`, []any{tenant, doneO, provider}},
		// 佣金：A 有一笔可用、一笔冻结，B 只有一笔已冲销。
		{`INSERT INTO referrals (tenant_id, referee_user_id, referrer_user_id, channel)
			VALUES ($1, $3, $2, 'pg18'), ($1, $4, $2, 'pg18')`, []any{tenant, user, refereeA, refereeB}},
		{`INSERT INTO commission_entries (tenant_id, referrer_user_id, referee_user_id, order_id, currency,
			base_amount, rate_bp, commission_amount, status)
			VALUES ($1, $2, $3, gen_random_uuid(), 'CNY', 1000, 1000, 100, 'available'),
			       ($1, $2, $3, gen_random_uuid(), 'CNY', 500, 1000, 50, 'pending'),
			       ($1, $2, $4, gen_random_uuid(), 'CNY', 700, 1000, 70, 'reversed')`,
			[]any{tenant, user, refereeA, refereeB}},
		{`INSERT INTO ledger_transactions (id, tenant_id, kind, currency, memo, actor_kind, actor_id)
			VALUES ($2, $1, 'commission_to_balance', 'CNY', '佣金转入余额', 'user', $4),
			       ($3, $1, 'commission_to_balance', 'CNY', '佣金转入余额', 'user', $5)`,
			[]any{tenant, txnMine, txnOther, user, other}},
		{`INSERT INTO ledger_entries (tenant_id, transaction_id, account_id, direction, amount, currency)
			VALUES ($1, $2, gen_random_uuid(), 'debit', 80, 'CNY'), ($1, $2, gen_random_uuid(), 'credit', 80, 'CNY'),
			       ($1, $3, gen_random_uuid(), 'debit', 99, 'CNY'), ($1, $3, gen_random_uuid(), 'credit', 99, 'CNY')`,
			[]any{tenant, txnMine, txnOther}},
	} {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed portal commerce: %v\nSQL: %s", err, seed.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit portal commerce fixture: %v", err)
	}

	h := &handlers{d: Deps{Pool: app, Billing: billing.NewService(app, nil),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	call := func(handler http.HandlerFunc, method, target, body string, params map[string]string, into any) int {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		c := httpx.WithTenantID(ctx, tenant)
		c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "user", Audience: "public", UserID: user, TenantID: tenant})
		if len(params) > 0 {
			rc := chi.NewRouteContext()
			for k, v := range params {
				rc.URLParams.Add(k, v)
			}
			c = context.WithValue(c, chi.RouteCtxKey, rc)
		}
		w := httptest.NewRecorder()
		handler(w, req.WithContext(c))
		if into != nil && w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
				t.Fatalf("decode %s %s: %v\n%s", method, target, err, w.Body.String())
			}
		}
		return w.Code
	}

	// --- 目录：重置策略与续费、变更开关 ---
	var plans struct {
		Plans []struct {
			ID                 string `json:"id"`
			QuotaResetStrategy string `json:"quota_reset_strategy"`
			QuotaResetDay      *int   `json:"quota_reset_day"`
			AllowRenewal       bool   `json:"allow_renewal"`
			AllowUpgrade       bool   `json:"allow_upgrade"`
		} `json:"plans"`
	}
	if code := call(h.listPlans, http.MethodGet, "/v1/plans", "", nil, &plans); code != http.StatusOK || len(plans.Plans) != 1 {
		t.Fatalf("list plans status=%d plans=%+v", code, plans.Plans)
	}
	if p := plans.Plans[0]; p.QuotaResetStrategy != "fixed_day" || p.QuotaResetDay == nil || *p.QuotaResetDay != 5 ||
		!p.AllowRenewal || p.AllowUpgrade {
		t.Fatalf("plan catalog fields=%+v", p)
	}
	t.Log("marker=portal_catalog_fields_ok")

	// --- 优惠码试算：套餐与流量包两种形态，都回券面 ---
	type preview struct {
		Subtotal int64 `json:"subtotal"`
		Discount int64 `json:"discount"`
		Payable  int64 `json:"payable"`
		Coupon   *struct {
			Code          string `json:"code"`
			DiscountType  string `json:"discount_type"`
			DiscountValue int64  `json:"discount_value"`
		} `json:"coupon"`
	}
	var pv preview
	if code := call(h.previewCoupon, http.MethodPost, "/v1/coupons/preview",
		`{"pack_id":"`+pack+`","coupon_code":"pc10"}`, nil, &pv); code != http.StatusOK ||
		pv.Subtotal != 2000 || pv.Discount != 200 || pv.Payable != 1800 || pv.Coupon == nil ||
		pv.Coupon.Code != "PC10" || pv.Coupon.DiscountType != "percent" || pv.Coupon.DiscountValue != 1000 {
		t.Fatalf("pack preview status=%d body=%+v coupon=%+v", code, pv, pv.Coupon)
	}
	pv = preview{}
	if code := call(h.previewCoupon, http.MethodPost, "/v1/coupons/preview",
		`{"plan_id":"`+plan+`","price_id":"`+price+`"}`, nil, &pv); code != http.StatusOK ||
		pv.Subtotal != 3000 || pv.Discount != 0 || pv.Coupon != nil {
		t.Fatalf("plan preview without code status=%d body=%+v", code, pv)
	}
	if code := call(h.previewCoupon, http.MethodPost, "/v1/coupons/preview",
		`{"pack_id":"`+pack+`","plan_id":"`+plan+`","price_id":"`+price+`"}`, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("mixed preview status=%d, want 422", code)
	}
	if code := call(h.previewCoupon, http.MethodPost, "/v1/coupons/preview",
		`{"pack_id":"7b100000-0000-4000-8000-0000000000ff"}`, nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown pack preview status=%d, want 404", code)
	}
	t.Log("marker=portal_coupon_preview_pack_ok")

	// --- 订单：多值状态、筛选段计数、首项快照 ---
	var orders struct {
		Orders []struct {
			OrderNo       string `json:"order_no"`
			Interval      string `json:"interval"`
			IntervalCount int    `json:"interval_count"`
			ItemName      string `json:"item_name"`
		} `json:"orders"`
		Total  int64                 `json:"total"`
		Counts billing.MyOrderCounts `json:"counts"`
	}
	if code := call(h.listMyOrders, http.MethodGet, "/v1/orders?status=pending_payment,processing,draft",
		"", nil, &orders); code != http.StatusOK || orders.Total != 1 || len(orders.Orders) != 1 {
		t.Fatalf("open orders status=%d total=%d rows=%+v", code, orders.Total, orders.Orders)
	}
	if o := orders.Orders[0]; o.OrderNo != "PC-OPEN" || o.Interval != "month" || o.IntervalCount != 1 || o.ItemName != "专业版" {
		t.Fatalf("open order row=%+v", o)
	}
	if orders.Counts != (billing.MyOrderCounts{Open: 1, Paid: 2, Closed: 1, Refunded: 1}) {
		t.Fatalf("order counts=%+v, want open 1 paid 2 closed 1 refunded 1 (other users excluded)", orders.Counts)
	}
	if code := call(h.listMyOrders, http.MethodGet, "/v1/orders?status=paid,bogus", "", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown status status=%d, want 400", code)
	}

	var detail struct {
		Order struct {
			CouponCode            *string `json:"coupon_code"`
			SubscriptionPeriodEnd *string `json:"subscription_period_end"`
			Payments              []struct {
				Method       *string `json:"method"`
				ProviderName string  `json:"provider_name"`
			} `json:"payments"`
		} `json:"order"`
	}
	if code := call(h.myOrderDetail, http.MethodGet, "/v1/orders/"+doneO, "", map[string]string{"id": doneO}, &detail); code != http.StatusOK {
		t.Fatalf("order detail status=%d", code)
	}
	d := detail.Order
	if d.CouponCode == nil || *d.CouponCode != "PC10" || d.SubscriptionPeriodEnd == nil ||
		!strings.HasPrefix(*d.SubscriptionPeriodEnd, "2030-01-31") || len(d.Payments) != 1 ||
		d.Payments[0].ProviderName != "易支付" || d.Payments[0].Method == nil || *d.Payments[0].Method != "alipay" {
		t.Fatalf("order detail=%+v payments=%+v", d, d.Payments)
	}
	t.Log("marker=portal_orders_counts_and_detail_ok")

	// --- 佣金：付费好友、累计佣金（不含冲销）、只有本人的转出 ---
	var commission struct {
		Summary struct {
			Invitees     int   `json:"invitees"`
			PaidInvitees int   `json:"paid_invitees"`
			TotalEarned  int64 `json:"total_earned"`
		} `json:"summary"`
		Transfers []billing.CommissionTransfer `json:"transfers"`
	}
	if code := call(h.myCommission, http.MethodGet, "/v1/me/commission", "", nil, &commission); code != http.StatusOK {
		t.Fatalf("my commission status=%d", code)
	}
	if s := commission.Summary; s.Invitees != 2 || s.PaidInvitees != 1 || s.TotalEarned != 150 {
		t.Fatalf("commission summary=%+v, want invitees 2 paid 1 earned 150", s)
	}
	if len(commission.Transfers) != 1 || commission.Transfers[0].LedgerTxnID != txnMine ||
		commission.Transfers[0].Amount != 80 || commission.Transfers[0].Currency != "CNY" {
		t.Fatalf("commission transfers=%+v, want only the caller's 80", commission.Transfers)
	}
	t.Log("marker=portal_commission_summary_ok")
}
