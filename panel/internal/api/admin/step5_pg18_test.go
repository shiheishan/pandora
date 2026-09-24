// [INPUT]: 依赖 announcement 域的一次性库（openAnnouncementPG18）、step3/step4 的造数与请求辅助，依赖第 ⑤ 步的处理器
// [OUTPUT]: 对外提供 step5Router 与 TestSiteSettingsPG18、TestDashboardTasksPG18、TestFeatureSwitchGatesPG18
// [POS]: api/admin 第 ⑤ 步的 PG18 集成测试：站点时区的迁移默认值、读写、校验与审计，「需要处理」各项计数与按权限过滤，降级开关的种子、网关门与切换广播；由 run-pg18-gates.sh 的 announcement 域按精确名单跑
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// step5Router 把被测处理器挂在一个带租户与主体的路由上；主体带会话且刚重认证过。
// 权限与重认证中间件不在这里验，由 TestStep5RouteProtections 钉住路由声明。
func step5Router(tenant, actor string, perms []string, mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, SessionID: "0190a000-0000-7000-8000-00000000c0de",
				ReauthedRecently: true, Permissions: perms})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	mount(r)
	return r
}

func TestSiteSettingsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000001"
		actor  = "87000000-0000-4000-8000-000000000011"
	)
	// 迁移 00083：新租户默认 Asia/Shanghai；种子里的默认租户原是 'UTC'，被改掉
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','site-tz-pg18','Site TZ','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@site-tz.invalid','Ops','active')`)
	var seeded, defaultTenant string
	if err := admin.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenant).Scan(&seeded); err != nil || seeded != "Asia/Shanghai" {
		t.Fatalf("new tenant timezone=%q err=%v, want the Asia/Shanghai default", seeded, err)
	}
	if err := admin.QueryRow(ctx, `SELECT timezone FROM tenants WHERE slug='default'`).Scan(&defaultTenant); err != nil || defaultTenant != "Asia/Shanghai" {
		t.Fatalf("seeded default tenant timezone=%q err=%v, want UTC migrated to Asia/Shanghai", defaultTenant, err)
	}

	h := step4Handlers(t, app)
	r := step5Router(tenant, actor, nil, func(r chi.Router) {
		r.Get("/v1/settings/site", h.getSiteSettings)
		r.Post("/v1/settings/site", h.setSiteSettings)
	})
	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/settings/site", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"timezone":"Asia/Shanghai"`) {
		t.Fatalf("get: status=%d body=%s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`{"timezone":""}`, `{"timezone":"Local"}`, `{"timezone":"Mars/Olympus"}`, `{}`} {
		w := step3Do(t, ctx, r, http.MethodPost, "/v1/settings/site", bad)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"timezone":"不是有效的时区"`) {
			t.Fatalf("bad %s: status=%d body=%s", bad, w.Code, w.Body.String())
		}
	}
	w := step3Do(t, ctx, r, http.MethodPost, "/v1/settings/site", `{"timezone":"America/New_York"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"timezone":"America/New_York"`) {
		t.Fatalf("set: status=%d body=%s", w.Code, w.Body.String())
	}
	var stored, before, after, auth string
	if err := admin.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenant).Scan(&stored); err != nil || stored != "America/New_York" {
		t.Fatalf("stored timezone=%q err=%v", stored, err)
	}
	if err := admin.QueryRow(ctx, `SELECT before_digest->>'timezone', after_digest->>'timezone', auth_context
		FROM audit_events WHERE tenant_id=$1 AND action='site.timezone_changed' AND actor_id=$2`, tenant, actor).
		Scan(&before, &after, &auth); err != nil || before != "Asia/Shanghai" || after != "America/New_York" || auth != "reauth" {
		t.Fatalf("audit before=%q after=%q auth=%q err=%v", before, after, auth, err)
	}
	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/settings/site", ""); !strings.Contains(w.Body.String(), `"timezone":"America/New_York"`) {
		t.Fatalf("get after set: %s", w.Body.String())
	}
}

func TestDashboardTasksPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000101"
		actor  = "87000000-0000-4000-8000-000000000111"
		pre    = "87000000-0000-4000-8000-"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','tasks-pg18','Tasks','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@tasks.invalid','Ops','active')`)
	nodes := step3Nodes(t, ctx, admin, tenant, pre, 4)
	step3Seed(t, ctx, admin,
		// 在线、离线 10 分钟、从没心跳（也算离线）、已退役（不算）
		`UPDATE nodes SET last_heartbeat_at = now() WHERE id='`+nodes[0]+`'`,
		`UPDATE nodes SET last_heartbeat_at = now() - interval '10 minutes' WHERE id='`+nodes[1]+`'`,
		`UPDATE nodes SET serving_status='retired', last_heartbeat_at = now() - interval '1 day' WHERE id='`+nodes[3]+`'`)

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		// 工单：两张待处理（一张高优先级、用户 2 小时前追问过），一张已解决不算
		`INSERT INTO tickets(id,tenant_id,ticket_no,user_id,subject,priority,status,created_at) VALUES
		   ('` + pre + `0000000000c1','` + tenant + `','T-1','` + actor + `','a','urgent','pending_agent',now()-interval '5 hours'),
		   ('` + pre + `0000000000c2','` + tenant + `','T-2','` + actor + `','b','normal','open',now()-interval '1 hour'),
		   ('` + pre + `0000000000c3','` + tenant + `','T-3','` + actor + `','c','high','resolved',now()-interval '9 hours')`,
		`INSERT INTO ticket_messages(tenant_id,ticket_id,author_kind,body,created_at) VALUES
		   ('` + tenant + `','` + pre + `0000000000c1','user','again',now()-interval '2 hours')`,
		// 提现：两笔待审（两个币种），一笔已打款不算
		`INSERT INTO withdrawals(tenant_id,user_id,currency,amount,status) VALUES
		   ('` + tenant + `','` + actor + `','CNY',12800,'requested'),
		   ('` + tenant + `','` + actor + `','USD',500,'reviewing'),
		   ('` + tenant + `','` + actor + `','CNY',999,'paid')`,
		// 订单：一张超过 30 分钟仍待支付，一张刚下
		`INSERT INTO orders(tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,
		    tax_amount,total_amount,balance_applied,payable_amount,expires_at,business_request_id,created_at) VALUES
		   ('` + tenant + `','TASKS-1','` + actor + `','new','pending_payment','CNY',100,0,0,100,0,100,now(),gen_random_uuid(),now()-interval '1 hour'),
		   ('` + tenant + `','TASKS-2','` + actor + `','new','pending_payment','CNY',100,0,0,100,0,100,now()+interval '30 minutes',gen_random_uuid(),now())`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	h := step4Handlers(t, app)
	get := func(perms []string) map[string]map[string]any {
		t.Helper()
		r := step5Router(tenant, actor, perms, func(r chi.Router) { r.Get("/v1/dashboard/tasks", h.dashboardTasks) })
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/dashboard/tasks", "")
		if w.Code != http.StatusOK {
			t.Fatalf("tasks: status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			AsOf  string           `json:"as_of"`
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.AsOf == "" {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
		out := map[string]map[string]any{}
		for _, it := range body.Items {
			out[it["kind"].(string)] = it
		}
		return out
	}

	all := get([]string{"ops.dashboard.read", "ops.ticket.read", "marketing.commission.read", "node.read",
		"billing.order.read", "ops.notification.read", "billing.ledger.read"})
	if len(all) != 6 {
		t.Fatalf("items=%v, want all six kinds", all)
	}
	num := func(kind, field string) float64 { return all[kind][field].(float64) }
	if num("tickets_open", "count") != 2 || num("tickets_open", "high_priority") != 1 {
		t.Fatalf("tickets_open=%v", all["tickets_open"])
	}
	// 最久等待按用户最后一次发言算：5 小时前建的单、2 小时前追问 → 约 2 小时，1 小时前的新单更短
	if wait := num("tickets_open", "oldest_wait_seconds"); wait < 7100 || wait > 7400 {
		t.Fatalf("oldest_wait_seconds=%v, want about 7200", wait)
	}
	w := all["withdrawals_pending"]
	amounts, _ := json.Marshal(w["amounts"])
	if w["count"].(float64) != 2 || string(amounts) != `[{"amount":12800,"currency":"CNY"},{"amount":500,"currency":"USD"}]` {
		t.Fatalf("withdrawals_pending=%v amounts=%s", w, amounts)
	}
	n := all["nodes_offline"]
	sample, _ := n["sample"].([]any)
	if n["count"].(float64) != 2 || len(sample) != 2 || sample[0].(map[string]any)["id"] != nodes[1] {
		t.Fatalf("nodes_offline=%v", n)
	}
	if off := n["longest_offline_seconds"].(float64); off < 590 || off > 700 {
		t.Fatalf("longest_offline_seconds=%v, want about 600", off)
	}
	if num("orders_pending_stale", "count") != 1 || num("orders_pending_stale", "threshold_seconds") != 1800 {
		t.Fatalf("orders_pending_stale=%v", all["orders_pending_stale"])
	}
	if nb := all["notifications_backlog"]; nb["queued"].(float64) != 0 || nb["backlog_state"] != "clear" {
		t.Fatalf("notifications_backlog=%v", nb)
	}
	if num("ledger_drift", "count") != 0 {
		t.Fatalf("ledger_drift=%v", all["ledger_drift"])
	}

	// 只有工单读权限：只出现工单一项，其余不查也不出现
	only := get([]string{"ops.dashboard.read", "ops.ticket.read"})
	if len(only) != 1 || only["tickets_open"] == nil {
		t.Fatalf("filtered items=%v, want only tickets_open", only)
	}
	if none := get([]string{"ops.dashboard.read"}); len(none) != 0 {
		t.Fatalf("no item permissions, items=%v", none)
	}
}

func TestFeatureSwitchGatesPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000201"
		actor  = "87000000-0000-4000-8000-000000000211"
	)
	// 迁移 00085：迁移时已有的租户（种子里的默认租户）有四个新开关，默认开启、非 essential
	var seeded int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM feature_switches f JOIN tenants t ON t.id=f.tenant_id
		WHERE t.slug='default' AND f.enabled AND NOT f.essential
		  AND f.code IN ('billing.checkout','marketing.giftcard.redeem','notify.email','admin.writes')`).Scan(&seeded); err != nil || seeded != 4 {
		t.Fatalf("seeded switches=%d err=%v, want 4", seeded, err)
	}
	// 迁移之后才建的租户没有这几行：billing.checkout 与 admin.writes 显式关掉，
	// 礼品卡兑换没有行（缺行视为开启）
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','switch-pg18','Switch','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@switch.invalid','Ops','active')`,
		`INSERT INTO feature_switches(tenant_id,code,enabled,essential,reason) VALUES
		   ('`+tenant+`','billing.checkout',false,false,'支付渠道故障'),
		   ('`+tenant+`','admin.writes',false,false,'迁移维护')`)

	h := step4Handlers(t, app)
	hub := realtime.NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(hub.Close)
	h.d.Realtime = hub
	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelAdmin(tenant)})
	t.Cleanup(unsubscribe)

	ok := func(w http.ResponseWriter, _ *http.Request) { httpx.OK(w, map[string]any{"ok": true}) }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := step5Router(tenant, actor, nil, func(r chi.Router) {
		r.Route("/v1", func(r chi.Router) {
			r.Use(middleware.AdminWritesGate(app, log))
			r.Get("/users", ok)
			r.Post("/users/{id}/status", ok)
			r.Post("/auth/reauth", ok)
			r.Post("/me/password", ok)
			r.Post("/switches/{code}", h.setSwitch)
		})
		r.With(middleware.FeatureSwitch(app, "billing.checkout", "下单与支付暂停中，请稍后再试", log)).Post("/p/orders", ok)
		r.With(middleware.FeatureSwitch(app, "marketing.giftcard.redeem", "礼品卡兑换暂停中，请稍后再试", log)).Post("/p/redeem", ok)
	})
	expect := func(method, path, body string, code int, contains string) {
		t.Helper()
		w := step3Do(t, ctx, r, method, path, body)
		if w.Code != code || !strings.Contains(w.Body.String(), contains) {
			t.Fatalf("%s %s: status=%d body=%s, want %d containing %q", method, path, w.Code, w.Body.String(), code, contains)
		}
	}

	// 只读模式：写被拒，读、重认证、改密码照常
	expect(http.MethodPost, "/v1/users/x/status", `{}`, http.StatusServiceUnavailable, "管理端只读模式")
	expect(http.MethodGet, "/v1/users", "", http.StatusOK, `"ok":true`)
	expect(http.MethodPost, "/v1/auth/reauth", `{}`, http.StatusOK, `"ok":true`)
	expect(http.MethodPost, "/v1/me/password", `{}`, http.StatusOK, `"ok":true`)
	// 下单关闭；礼品卡兑换没有开关行，放行
	expect(http.MethodPost, "/p/orders", `{}`, http.StatusServiceUnavailable, "下单与支付暂停中")
	expect(http.MethodPost, "/p/redeem", `{}`, http.StatusOK, `"ok":true`)

	// 切开关本身在只读模式下也放行，成功后只向管理端频道广播 switches.changed
	expect(http.MethodPost, "/v1/switches/admin.writes", `{"enabled":true}`, http.StatusOK, `"enabled":true`)
	select {
	case ev := <-events:
		if ev.Topic != "switches.changed" || ev.Payload["code"] != "admin.writes" || ev.Payload["enabled"] != true {
			t.Fatalf("event=%+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("switches.changed was not published")
	}
	expect(http.MethodPost, "/v1/users/x/status", `{}`, http.StatusOK, `"ok":true`)
}
