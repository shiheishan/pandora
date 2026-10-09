package public

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/token"
	"github.com/aegispanel/aegis/tools/routebudget"
)

// TestPortalRouteBudgetPG18 走门户网关的真实路由（public.NewRouter，含请求 ID、访问日志、
// 鉴权、限流整条链），逐条量门户热路由的稳态库语句往返与 Valkey 往返，与
// tools/routebudget/routes.txt 的预算比对。路由器只认默认租户，夹具都建在默认租户里
// （public_api 库里别的用例都用各自的租户）。
//
// 已登录的门户请求都带 Authorization（前端登录后每个请求都带，鉴权那次库往返算在内）；
// 登录与订阅拉取不带。
func TestPortalRouteBudgetPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const tenant = middleware.DefaultTenantID
	f := seedPortalBudgetFixture(t, ctx, admin, tenant)

	masterKey := bytes.Repeat([]byte{0x42}, 32)
	envelope, err := crypto.NewEnvelope(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	access := &routebudget.AccessLog{}
	log := access.Logger()
	hub := realtime.NewHub(nil, log)
	defer hub.Close()
	issuer := token.NewIssuer("public", []byte("route-budget-secret-0123456789ab"), time.Hour)
	billingSvc := billing.NewService(app, envelope)
	tgSender := notify.NewDynamicTelegramSender(app, envelope, tenant)
	server := httptest.NewServer(NewRouter(Deps{
		Cfg: &config.Config{MasterKey: masterKey, PublicBaseURL: "https://portal.route-budget.invalid",
			RateLimitPerIPPerMinute: 600, RateLimitPerAccountPerMinute: 600, RateLimitAuthPerMinute: 60},
		Pool: app, Redis: routebudget.Valkey(t), Log: log, Issuer: issuer,
		Identity:     identity.NewService(app, issuer, time.Hour, masterKey, false),
		Billing:      billingSvc,
		Payments:     billing.NewPaymentService(billingSvc, app, envelope, masterKey, "https://portal.route-budget.invalid", false),
		Support:      support.NewService(app),
		Subscription: subscription.New(app, crypto.SubscriptionAuditSalt(masterKey), envelope),
		Realtime:     hub,
		Notify: notify.New(app, log, crypto.NotifyRecipientSalt(masterKey),
			notify.NewDynamicSMTPSender(notify.NewDBSMTPProvider(app, envelope), tenant), tgSender),
		Content:        content.New(app),
		GiftCard:       giftcard.New(app, log, billingSvc.GiftGranter()),
		TelegramSender: tgSender,
		Envelope:       envelope,
		Appearance:     appearance.New(app),
	}))
	defer server.Close()

	bearer, err := issuer.Issue(token.Claims{Subject: f.user, TenantID: tenant, SessionID: f.session, Kind: "user",
		AuthMeth: []string{"password"}})
	if err != nil {
		t.Fatal(err)
	}
	// call 发一次请求；状态码不对时把响应体打进日志，CI 上一次就能看清是哪条夹具没接上
	call := func(method, path, body string, authed bool) func(string) int {
		return func(id string) int {
			var rd io.Reader
			if body != "" {
				rd = strings.NewReader(body)
			}
			req, err := http.NewRequest(method, server.URL+path, rd)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Request-ID", id)
			req.Header.Set("User-Agent", "clash-verge/v2.0")
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if authed {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				t.Logf("%s %s → %d %s", method, path, resp.StatusCode, raw)
			}
			return resp.StatusCode
		}
	}
	ok := []int{http.StatusOK}
	budget := routebudget.NewBudget(t, "public", app)
	for _, c := range []struct {
		route, method, path, body string
		authed                    bool
	}{
		{"GET /healthz", "GET", "/healthz", "", false},
		{"POST /v1/auth/login", "POST", "/v1/auth/login", `{"email":"` + f.email + `","password":"` + f.password + `"}`, false},
		{"GET /{prefix}/{token}", "GET", "/" + f.prefix + "/" + f.subToken, "", false},
		{"GET /v1/site-config", "GET", "/v1/site-config", "", true},
		{"GET /v1/appearance", "GET", "/v1/appearance", "", true},
		{"GET /v1/plans", "GET", "/v1/plans", "", true},
		{"GET /v1/traffic-packs", "GET", "/v1/traffic-packs", "", true},
		{"GET /v1/me", "GET", "/v1/me", "", true},
		{"GET /v1/payment-methods", "GET", "/v1/payment-methods", "", true},
		{"GET /v1/me/subscriptions", "GET", "/v1/me/subscriptions", "", true},
		{"GET /v1/me/subscriptions/{id}/nodes", "GET", "/v1/me/subscriptions/" + f.subscription + "/nodes", "", true},
		{"GET /v1/me/subscriptions/{id}/usage", "GET", "/v1/me/subscriptions/" + f.subscription + "/usage", "", true},
		{"GET /v1/me/subscription-links", "GET", "/v1/me/subscription-links", "", true},
		{"GET /v1/me/invite", "GET", "/v1/me/invite", "", true},
		{"GET /v1/me/commission", "GET", "/v1/me/commission", "", true},
		{"GET /v1/me/balance", "GET", "/v1/me/balance", "", true},
		{"GET /v1/me/announcements", "GET", "/v1/me/announcements", "", true},
		{"GET /v1/content/pages", "GET", "/v1/content/pages", "", true},
		{"GET /v1/me/notifications", "GET", "/v1/me/notifications", "", true},
		{"GET /v1/me/notification-preferences", "GET", "/v1/me/notification-preferences", "", true},
		{"GET /v1/me/traffic-packs", "GET", "/v1/me/traffic-packs", "", true},
		{"GET /v1/me/gift-cards", "GET", "/v1/me/gift-cards", "", true},
		{"GET /v1/me/telegram", "GET", "/v1/me/telegram", "", true},
		{"GET /v1/me/sessions", "GET", "/v1/me/sessions", "", true},
		{"GET /v1/orders", "GET", "/v1/orders", "", true},
		{"GET /v1/orders/{id}", "GET", "/v1/orders/" + f.order, "", true},
		{"GET /v1/support/categories", "GET", "/v1/support/categories", "", true},
		{"GET /v1/support/tickets", "GET", "/v1/support/tickets", "", true},
		{"POST /v1/me/checkout/quote", "POST", "/v1/me/checkout/quote",
			`{"action":"renew","subscription_id":"` + f.subscription + `"}`, true},
	} {
		budget.Measure(access, c.route, 2, ok, call(c.method, c.path, c.body, c.authed))
	}
	budget.Verify()
}

type portalBudgetFixture struct {
	user, email, password, session string
	subscription, subToken, prefix string
	order                          string
}

// seedPortalBudgetFixture 在默认租户里建一个已登录用户：一份生效订阅（有价格可续费、
// 有配额、有订阅链接、套餐绑定的池里有一个节点）、一张待支付订单、一个支付渠道。
func seedPortalBudgetFixture(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant string) portalBudgetFixture {
	t.Helper()
	id := func() string { return uuid.NewString() }
	f := portalBudgetFixture{user: id(), email: "rb-" + uuid.NewString()[:8] + "@route-budget.invalid",
		password: "route-budget-pass-1", session: id(), subscription: id(),
		subToken: strings.Repeat("rbud", 9), order: id()}
	product, plan, version, price, pool, server, node := id(), id(), id(), id(), id(), id(), id()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed portal budget fixture: %v\nSQL: %s", err, sql)
		}
	}
	if err := admin.QueryRow(ctx, `SELECT coalesce(sub_path_prefix,'') FROM tenants WHERE id=$1`, tenant).Scan(&f.prefix); err != nil {
		t.Fatal(err)
	}
	if f.prefix == "" {
		f.prefix = "b0d6e7aa0001"
		must(`UPDATE tenants SET sub_path_prefix=$2 WHERE id=$1`, tenant, f.prefix)
	}
	phc, err := crypto.HashPassword(f.password, crypto.DefaultArgon2Params())
	if err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,$3,'Route Budget','active')`, tenant, f.user, f.email)
	must(`INSERT INTO user_passwords(user_id,tenant_id,phc) VALUES($2,$1,$3)`, tenant, f.user, phc)
	must(`INSERT INTO sessions(id,tenant_id,user_id,audience,auth_methods,expires_at)
	      VALUES($2,$1,$3,'public',ARRAY['password'],now()+interval '30 days')`, tenant, f.session, f.user)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'rb-portal','Route Budget','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'rb-portal','Route Budget','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'rb-portal','Route Budget','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, version, f.user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, version, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, version)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, version, plan)
	must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status)
	      VALUES($2,$1,$3,'CNY',3000,'month',1,'active')`, tenant, price, product)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'rb-portal','ready')`, tenant, server)
	must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,sort_order)
	      VALUES($2,$1,'RB Line','RB Line',$3,'active','vless','rb.route-budget.invalid',443,$4,'active',1,now(),now(),0)`,
		tenant, node, pool, server)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,price_id,status,snapshot_currency,
			snapshot_amount,current_period_start,current_period_end)
	      VALUES($1,$2,$3,$4,$5,$6,'active','CNY',3000,now()-interval '10 days',now()+interval '20 days')`,
		f.subscription, tenant, f.user, plan, version, price)
	must(`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope)
	      VALUES($1,$2,$3,$4,$5,'subscription')`, tenant, f.subscription, f.user, crypto.HashToken(f.subToken), f.subToken[:8])
	must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
	      VALUES($1,$2,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',1000000,1000000,400)`,
		tenant, f.subscription)

	// 订单与支付渠道照 portal_step5 的做法关掉触发器直接插（它们的正常来路是下单与后台配置）
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO orders(id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,tax_amount,
			total_amount,balance_applied,payable_amount,expires_at,business_request_id)
		  VALUES($2,$1,'RB-ORDER-1',$3,'new','pending_payment','CNY',3000,0,0,3000,0,3000,now()+interval '30 minutes',gen_random_uuid())`,
			[]any{tenant, f.order, f.user}},
		{`INSERT INTO payment_providers(tenant_id,code,adapter,display_name,supported_currencies,config,enabled,accepting_new)
		  VALUES($1,'epay','epay','易支付','{CNY}','{"methods":["alipay","wxpay"]}',true,true)`, []any{tenant}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed portal budget fixture: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}
