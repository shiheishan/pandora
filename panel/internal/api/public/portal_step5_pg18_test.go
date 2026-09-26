// [INPUT]: 依赖 platform/pg18test 打开 public_api 域的一次性库（publicAPIFixture），依赖本包 my_subscriptions.go / payment_methods.go / content.go / handlers.go 的处理器，依赖 domain 的 support / content / identity 服务
// [OUTPUT]: 对外提供 TestPortalStep5PG18
// [POS]: api/public 第 ⑤ 步的 PG18 测试：我的订阅扩展字段与续费价可用性、工单 closed_reason 与关联订单、帮助 q 与 platform=any、改密保留当前会话、支付方式展开
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestPortalStep5PG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const (
		tenant   = "88000000-0000-4000-8000-000000000001"
		user     = "88000000-0000-4000-8000-000000000011"
		agent    = "88000000-0000-4000-8000-000000000012"
		group    = "88000000-0000-4000-8000-000000000021"
		other    = "88000000-0000-4000-8000-000000000022"
		product  = "88000000-0000-4000-8000-000000000031"
		plan     = "88000000-0000-4000-8000-000000000032"
		version  = "88000000-0000-4000-8000-000000000033"
		price    = "88000000-0000-4000-8000-000000000034"
		vipPrice = "88000000-0000-4000-8000-000000000035"
		subLive  = "88000000-0000-4000-8000-000000000041"
		subVip   = "88000000-0000-4000-8000-000000000042"
		order    = "88000000-0000-4000-8000-000000000051"
		keep     = "88000000-0000-4000-8000-000000000061"
		drop     = "88000000-0000-4000-8000-000000000062"
	)
	phc, err := crypto.HashPassword("old-pass-123", crypto.DefaultArgon2Params())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'portal5','Portal5','CNY')`, []any{tenant}},
		{`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'std','标准'),($3,$1,'vip','贵宾')`, []any{tenant, group, other}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id) VALUES
		   ($2,$1,'u@portal5.invalid','U','active',$4),($3,$1,'a@portal5.invalid','A','active',NULL)`, []any{tenant, user, agent, group}},
		{`INSERT INTO user_passwords(user_id,tenant_id,phc) VALUES($2,$1,$3)`, []any{tenant, user, phc}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'p5','P5','active')`, []any{tenant, product}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'pro','Pro','active')`, []any{tenant, product, plan}},
		{`INSERT INTO plan_versions(id,tenant_id,plan_id,version,max_devices,quota_reset_strategy) VALUES($3,$1,$2,1,3,'billing_cycle')`, []any{tenant, plan, version}},
		// 普通价格可续费；贵宾组价格对这个（标准组）用户不可用
		{`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status,user_group_id) VALUES
		   ($2,$1,$3,'CNY',3000,'month',1,'active',NULL),($4,$1,$3,'CNY',2000,'month',1,'active',$5)`, []any{tenant, price, product, vipPrice, other}},
		{`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,price_id,status,snapshot_currency,snapshot_amount,
			current_period_start,current_period_end,created_at) VALUES
		   ($2,$1,$3,$4,$5,$6,'active','CNY',3000,now()-interval '10 days',now()+interval '20 days',now()),
		   ($7,$1,$3,$4,$5,$8,'expired','CNY',2000,now()-interval '90 days',now()-interval '60 days',now()-interval '90 days')`,
			[]any{tenant, subLive, user, plan, version, price, subVip, vipPrice}},
		{`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed,adjusted)
		  VALUES($1,$2,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',1000,1000,400,50)`, []any{tenant, subLive}},
		{`INSERT INTO traffic_pack_grants(tenant_id,user_id,source,source_id,granted_bytes,consumed_bytes)
		  VALUES($1,$2,'migration',gen_random_uuid(),100,40)`, []any{tenant, user}},
		{`INSERT INTO orders(id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,tax_amount,
			total_amount,balance_applied,payable_amount,expires_at,business_request_id)
		  VALUES($2,$1,'P5-ORDER-1',$3,'new','pending_payment','CNY',100,0,0,100,0,100,now()+interval '30 minutes',gen_random_uuid())`,
			[]any{tenant, order, user}},
		{`INSERT INTO payment_providers(tenant_id,code,adapter,display_name,supported_currencies,config,enabled,accepting_new) VALUES
		   ($1,'epay','epay','易支付','{CNY}','{"methods":["alipay","wxpay"]}',true,true),
		   ($1,'stripe','stripe','Stripe','{USD}','{}',true,true)`, []any{tenant}},
		{`INSERT INTO sessions(id,tenant_id,user_id,audience,auth_methods,expires_at) VALUES
		   ($2,$1,$3,'public',ARRAY['password'],now()+interval '30 days'),($4,$1,$3,'public',ARRAY['password'],now()+interval '30 days')`,
			[]any{tenant, keep, user, drop}},
		{`INSERT INTO refresh_tokens(tenant_id,session_id,user_id,token_hash,expires_at) VALUES
		   ($1,$2,$3,'\x01',now()+interval '30 days'),($1,$4,$3,'\x02',now()+interval '30 days')`, []any{tenant, keep, user, drop}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handlers{d: Deps{Pool: app, Log: log, Support: support.NewService(app), Content: content.New(app),
		Identity: identity.NewService(app, nil, time.Hour, []byte("portal5-salt"), false)}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "user", Audience: "public", UserID: user,
				TenantID: tenant, SessionID: keep})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	r.Get("/v1/me/subscriptions", h.listSubscriptions)
	r.Get("/v1/payment-methods", h.listPaymentMethods)
	r.Get("/v1/support/tickets", h.listTickets)
	r.Get("/v1/support/tickets/{id}", h.getTicket)
	r.Get("/v1/content/pages", h.listContentPages)
	r.Post("/v1/me/password", h.changePassword)
	do := func(method, path, body string, code int, dst any) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != code {
			t.Fatalf("%s %s: status=%d body=%s, want %d", method, path, w.Code, w.Body.String(), code)
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
		return w.Body.String()
	}

	// --- 我的订阅 ---
	var subs struct {
		Subscriptions []struct {
			ID                 string `json:"id"`
			DeviceLimit        *int   `json:"device_limit"`
			OnlineDevices      int    `json:"online_devices"`
			QuotaResetStrategy string `json:"quota_reset_strategy"`
			Renewable          bool   `json:"renewable"`
			RenewalPrice       *struct {
				ID        string `json:"id"`
				Available bool   `json:"available"`
			} `json:"renewal_price"`
			PackRemainingBytes int64 `json:"pack_remaining_bytes"`
			Quotas             []struct {
				Period   string `json:"period"`
				Adjusted int64  `json:"adjusted"`
			} `json:"quotas"`
		} `json:"subscriptions"`
	}
	body := do(http.MethodGet, "/v1/me/subscriptions", "", http.StatusOK, &subs)
	if len(subs.Subscriptions) != 2 {
		t.Fatalf("subscriptions=%s", body)
	}
	live, vip := subs.Subscriptions[0], subs.Subscriptions[1]
	if live.ID != subLive || live.DeviceLimit == nil || *live.DeviceLimit != 3 || live.QuotaResetStrategy != "billing_cycle" ||
		!live.Renewable || live.RenewalPrice == nil || live.RenewalPrice.ID != price || !live.RenewalPrice.Available ||
		live.PackRemainingBytes != 60 || len(live.Quotas) != 1 || live.Quotas[0].Period != "cycle" || live.Quotas[0].Adjusted != 50 ||
		!strings.Contains(body, `"next_reset_at":"`) {
		t.Fatalf("live subscription=%+v body=%s", live, body)
	}
	if vip.Renewable || vip.RenewalPrice == nil || vip.RenewalPrice.Available || vip.PackRemainingBytes != 60 {
		t.Fatalf("expired vip subscription=%+v", vip)
	}

	// --- 支付方式：渠道按 methods 展开，不接新支付的不出现 ---
	var pm struct {
		Methods []struct{ Provider, Method, Label string } `json:"methods"`
	}
	do(http.MethodGet, "/v1/payment-methods", "", http.StatusOK, &pm)
	got := []string{}
	for _, m := range pm.Methods {
		got = append(got, m.Provider+"/"+m.Method+"/"+m.Label)
	}
	if strings.Join(got, ",") != "epay/alipay/支付宝,epay/wxpay/微信支付,stripe//Stripe" {
		t.Fatalf("payment methods=%v", got)
	}

	// --- 工单：用户关闭写 user_closed；详情带关联订单；非 uuid 404 ---
	svc := h.d.Support
	tk, err := svc.Create(ctx, tenant, support.CreateInput{UserID: user, Body: "订单付款后没有开通，麻烦看一下。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE tickets SET related_order_id=$1 WHERE id=$2`, order, tk.ID); err != nil {
		t.Fatal(err)
	}
	if b := do(http.MethodGet, "/v1/support/tickets/"+tk.ID, "", http.StatusOK, nil); !strings.Contains(b, `"related_order":{"id":"`+order+`","order_no":"P5-ORDER-1"}`) ||
		!strings.Contains(b, `"closed_reason":null`) {
		t.Fatalf("ticket detail=%s", b)
	}
	if err := svc.CloseByUser(ctx, tenant, user, tk.ID); err != nil {
		t.Fatal(err)
	}
	if b := do(http.MethodGet, "/v1/support/tickets", "", http.StatusOK, nil); !strings.Contains(b, `"closed_reason":"user_closed"`) {
		t.Fatalf("ticket list=%s", b)
	}
	do(http.MethodGet, "/v1/support/tickets/not-a-uuid", "", http.StatusNotFound, nil)

	// --- 帮助：定向 iOS 的教程默认看不到，platform=any 看得到；q 匹配正文 ---
	for i, in := range []content.PublishInput{
		{Slug: "ios-setup", Kind: "tutorial", Title: "iPhone 教程", Body: "在 Shadowrocket 里导入订阅", TargetPlatforms: []string{"ios"}, Status: "published"},
		{Slug: "win-setup", Kind: "tutorial", Title: "Windows 教程", Body: "在 Clash Verge 里导入订阅", Status: "published"},
	} {
		if _, err := h.d.Content.PublishVersion(ctx, tenant, agent, "portal5-"+in.Slug, in); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	slugs := func(query string) string {
		t.Helper()
		var out struct {
			Pages []struct {
				Slug string `json:"slug"`
			} `json:"pages"`
		}
		do(http.MethodGet, "/v1/content/pages?"+query, "", http.StatusOK, &out)
		s := []string{}
		for _, p := range out.Pages {
			s = append(s, p.Slug)
		}
		return strings.Join(s, ",")
	}
	if got := slugs(""); got != "win-setup" {
		t.Fatalf("default platform pages=%s", got)
	}
	if got := slugs("platform=any&q=SHADOWROCKET"); got != "ios-setup" {
		t.Fatalf("platform=any q pages=%s", got)
	}
	if got := slugs("platform=any&q=导入订阅"); got != "ios-setup,win-setup" && got != "win-setup,ios-setup" {
		t.Fatalf("platform=any shared q pages=%s", got)
	}
	do(http.MethodGet, "/v1/content/pages?q="+strings.Repeat("长", 101), "", http.StatusUnprocessableEntity, nil)

	// --- 改密：保留当前会话与它的 refresh 令牌，其余吊销 ---
	do(http.MethodPost, "/v1/me/password", `{"old_password":"old-pass-123","new_password":"new-pass-456"}`, http.StatusOK, nil)
	var keptRevoked, droppedRevoked bool
	var keptToken, droppedToken string
	if err := admin.QueryRow(ctx, `SELECT
		(SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$1), (SELECT revoked_at IS NOT NULL FROM sessions WHERE id=$2),
		(SELECT status FROM refresh_tokens WHERE session_id=$1), (SELECT status FROM refresh_tokens WHERE session_id=$2)`, keep, drop).
		Scan(&keptRevoked, &droppedRevoked, &keptToken, &droppedToken); err != nil {
		t.Fatal(err)
	}
	if keptRevoked || !droppedRevoked || keptToken != "active" || droppedToken != "revoked" {
		t.Fatalf("after password change kept=%v/%s dropped=%v/%s", keptRevoked, keptToken, droppedRevoked, droppedToken)
	}
}
