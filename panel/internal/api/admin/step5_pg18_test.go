// [INPUT]: 依赖 announcement 域的一次性库（openAnnouncementPG18）、step3/step4 的造数与请求辅助，依赖第 ⑤ 步的处理器
// [OUTPUT]: 对外提供 step5Router 与 TestSiteSettingsPG18、TestDashboardTasksPG18、TestFeatureSwitchGatesPG18、TestAdminMeProfilePG18、TestUserProfileRegisteredIPPG18、TestDashboardReadModelsPG18、TestNodesStep5PG18、TestContentNotifyStep5PG18
// [POS]: api/admin 第 ⑤ 步的 PG18 集成测试：站点时区的迁移默认值、读写、校验与审计，「需要处理」各项计数与按权限过滤，降级开关的种子、网关门与切换广播，GET v1/me 的邮箱、显示名与生效角色，风控画像的注册 IP，经营总览 / 收入上一区间 / 系统状态组件 / 日活，节点列表字段与排序、节点池成员、一步退役、全局路由，公告用户组定向、Telegram 管理员群组、模板草稿预览与测试、钩子统计、访问日志 outcome；由 run-pg18-gates.sh 的 announcement 域按精确名单跑
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
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

func TestAdminMeProfilePG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000301"
		actor  = "87000000-0000-4000-8000-000000000311"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','me-pg18','Me','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','linzhou@me.invalid','林舟','active')`)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO roles(id,tenant_id,code,name) VALUES
		   ('87000000-0000-4000-8000-000000000321','` + tenant + `','ops','运维'),
		   ('87000000-0000-4000-8000-000000000322','` + tenant + `','finance','财务'),
		   ('87000000-0000-4000-8000-000000000323','` + tenant + `','support','客服')`,
		// 生效：租户级、未过期；不算：已过期、非租户级（与 admin 登录展开权限同一过滤）
		`INSERT INTO role_bindings(tenant_id,user_id,role_id,scope_type,expires_at) VALUES
		   ('` + tenant + `','` + actor + `','87000000-0000-4000-8000-000000000321','tenant',NULL),
		   ('` + tenant + `','` + actor + `','87000000-0000-4000-8000-000000000322','tenant',now()-interval '1 day'),
		   ('` + tenant + `','` + actor + `','87000000-0000-4000-8000-000000000323','self',NULL)`,
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
	h.d.Identity = identity.NewService(app, nil, time.Hour, []byte("me-pg18-salt"), false)
	r := step5Router(tenant, actor, []string{"ops.ticket.read"}, func(r chi.Router) { r.Get("/v1/me", h.me) })
	w := step3Do(t, ctx, r, http.MethodGet, "/v1/me", "")
	var body struct {
		UserID      string              `json:"user_id"`
		Email       string              `json:"email"`
		DisplayName *string             `json:"display_name"`
		Roles       []map[string]string `json:"roles"`
		Permissions []string            `json:"permissions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("me: status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	if body.UserID != actor || body.Email != "linzhou@me.invalid" || body.DisplayName == nil || *body.DisplayName != "林舟" ||
		len(body.Roles) != 1 || body.Roles[0]["code"] != "ops" || body.Roles[0]["name"] != "运维" ||
		len(body.Permissions) != 1 {
		t.Fatalf("me body=%s", w.Body.String())
	}
}

func TestUserProfileRegisteredIPPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000401"
		actor  = "87000000-0000-4000-8000-000000000411"
		user   = "87000000-0000-4000-8000-000000000412"
		quiet  = "87000000-0000-4000-8000-000000000413"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','profile-ip-pg18','Profile','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		   ('`+actor+`','`+tenant+`','ops@profile.invalid','Ops','active'),
		   ('`+user+`','`+tenant+`','u@profile.invalid','U','active'),
		   ('`+quiet+`','`+tenant+`','q@profile.invalid','Q','active')`)
	h := step4Handlers(t, app) // 配好了审计 IP 的哈希与加密
	uid := user
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		if err := audit.Write(ctx, tx, tenant, audit.Entry{ActorKind: "user", ActorID: &uid,
			Action: "user.registered", ResourceType: "user", ResourceID: &uid, SourceIP: "198.51.100.7"}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenant, audit.Entry{ActorKind: "user", ActorID: &uid,
			Action: "user.login", SourceIP: "203.0.113.9"})
	}); err != nil {
		t.Fatal(err)
	}
	r := step5Router(tenant, actor, nil, func(r chi.Router) { r.Get("/v1/users/{id}/profile", h.userProfile) })
	for id, want := range map[string]string{user: `"registered_ip":"198.51.100.7"`, quiet: `"registered_ip":""`} {
		if w := step3Do(t, ctx, r, http.MethodGet, "/v1/users/"+id+"/profile", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("profile %s: status=%d body=%s, want %s", id, w.Code, w.Body.String(), want)
		}
	}
}

func TestDashboardReadModelsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000501"
		actor  = "87000000-0000-4000-8000-000000000511"
		other  = "87000000-0000-4000-8000-000000000512"
		pre    = "87000000-0000-4000-8000-000000000"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','dash-pg18','Dash','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		   ('`+actor+`','`+tenant+`','ops@dash.invalid','Ops','active'),
		   ('`+other+`','`+tenant+`','o@dash.invalid','O','active')`)
	// 节点 id 前缀与 TestDashboardTasksPG18 错开：同一个库，主键不能撞
	nodes := step3Nodes(t, ctx, admin, tenant, "87000005-0000-4000-8000-", 2)
	step3Seed(t, ctx, admin, `UPDATE nodes SET last_heartbeat_at = now() WHERE id='`+nodes[0]+`'`)
	subA, subB := pre+"5a1", pre+"5a2"
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		// 一条新建的 active 订阅（算 new_7_days），一条老订阅
		`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end,created_at) VALUES
		   ('` + subA + `','` + tenant + `','` + actor + `',gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',now()),
		   ('` + subB + `','` + tenant + `','` + other + `',gen_random_uuid(),gen_random_uuid(),'active','CNY',0,now()+interval '30 days',now()-interval '40 days')`,
		// 站点时区（新租户默认上海）的昨天调增 500；10 天前调增 700（落在 7 天的上一区间里）
		`INSERT INTO revenue_report_adjustments(tenant_id,currency,amount,reason,effective_on,created_by,idempotency_key) VALUES
		   ('` + tenant + `','CNY',500,'昨日补录收入',(now() AT TIME ZONE 'Asia/Shanghai')::date - 1,'` + actor + `','dash-1'),
		   ('` + tenant + `','CNY',700,'上一区间补录',(now() AT TIME ZONE 'Asia/Shanghai')::date - 10,'` + actor + `','dash-2')`,
		// 日活：actor 今天拉取成功且有流量（去重算 1），other 今天只有流量
		`INSERT INTO subscription_fetch_log(tenant_id,subscription_id,result,fetched_at) VALUES('` + tenant + `','` + subA + `','ok',now())`,
		`INSERT INTO subscription_usage_daily(tenant_id,subscription_id,day,bytes) VALUES
		   ('` + tenant + `','` + subA + `',current_date,10),('` + tenant + `','` + subB + `',current_date,20)`,
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
	r := step5Router(tenant, actor, nil, func(r chi.Router) {
		r.Get("/v1/overview", h.overview)
		r.Get("/v1/revenue/timeseries", h.revenueTimeseries)
		r.Get("/v1/system/status", h.systemStatus)
		r.Get("/v1/stats/timeseries", h.statsTimeseries)
	})
	getJSON := func(path string, dst any) {
		t.Helper()
		w := step3Do(t, ctx, r, http.MethodGet, path, "")
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), dst) != nil {
			t.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
	}

	var ov struct {
		Subscriptions struct {
			New7Days int `json:"new_7_days"`
		} `json:"subscriptions"`
		Nodes struct {
			Total, Online int
		} `json:"nodes"`
		Revenue []struct {
			Currency        string
			Yesterday       int64 `json:"yesterday"`
			ActualYesterday int64 `json:"actual_yesterday"`
		} `json:"revenue"`
	}
	getJSON("/v1/overview", &ov)
	if ov.Subscriptions.New7Days != 1 || ov.Nodes.Total != 2 || ov.Nodes.Online != 1 ||
		len(ov.Revenue) != 2 || ov.Revenue[0].Currency != "CNY" || ov.Revenue[0].Yesterday != 500 || ov.Revenue[0].ActualYesterday != 0 {
		t.Fatalf("overview=%+v", ov)
	}

	var ts struct {
		PreviousTotal int64 `json:"previous_total"`
		Points        []any `json:"points"`
	}
	getJSON("/v1/revenue/timeseries?currency=cny&days=7", &ts)
	if ts.PreviousTotal != 700 || len(ts.Points) != 7 {
		t.Fatalf("timeseries previous_total=%d points=%d", ts.PreviousTotal, len(ts.Points))
	}

	var st struct {
		State      string `json:"state"`
		Components []struct {
			Key     string         `json:"key"`
			State   string         `json:"state"`
			Metrics map[string]any `json:"metrics"`
		} `json:"components"`
	}
	getJSON("/v1/system/status", &st)
	comp := map[string]map[string]any{}
	states := map[string]string{}
	for _, c := range st.Components {
		comp[c.Key], states[c.Key] = c.Metrics, c.State
	}
	if len(st.Components) != 8 || st.State != "degraded" || states["postgres"] != "ok" || states["valkey"] != "unknown" ||
		states["node_fabric"] != "warn" || comp["node_fabric"]["online"] != float64(1) || comp["node_fabric"]["total"] != float64(2) ||
		states["payment_callbacks"] != "ok" || states["mail"] != "ok" || states["sse"] != "unknown" {
		t.Fatalf("system status state=%s components=%+v", st.State, st.Components)
	}

	var stats struct {
		Points []struct {
			ActiveUsers int `json:"active_users"`
		} `json:"points"`
	}
	getJSON("/v1/stats/timeseries?days=3", &stats)
	if len(stats.Points) != 3 || stats.Points[2].ActiveUsers != 2 || stats.Points[0].ActiveUsers != 0 {
		t.Fatalf("stats points=%+v", stats.Points)
	}
}

func TestNodesStep5PG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000601"
		actor  = "87000000-0000-4000-8000-000000000611"
		prefix = "87000006-0000-4000-8000-"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','nodes5-pg18','Nodes5','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@nodes5.invalid','Ops','active')`)
	nodes := step3Nodes(t, ctx, admin, tenant, prefix, 3)
	server := prefix + "0000000000a2"
	step3Seed(t, ctx, admin,
		// 排序：sort_order 优先、node_no 其次
		`UPDATE nodes SET sort_order = 20 WHERE id='`+nodes[0]+`'`,
		`UPDATE nodes SET sort_order = 10 WHERE id IN ('`+nodes[1]+`','`+nodes[2]+`')`,
		// 服务器的控制节点是 nodes[0]，它的最近一条探针给同服务器的全部节点
		`UPDATE servers SET control_node_id='`+nodes[0]+`' WHERE id='`+server+`'`,
		`INSERT INTO node_metrics(tenant_id,node_id,recorded_at,cpu_bp,mem_used_mb,mem_total_mb) VALUES
		   ('`+tenant+`','`+nodes[0]+`',now()-interval '1 minute',1234,512,2048),
		   ('`+tenant+`','`+nodes[0]+`',now()-interval '1 hour',9000,2000,2048)`,
		`INSERT INTO node_tasks(tenant_id,node_id,task_type,expires_at) VALUES('`+tenant+`','`+nodes[1]+`','health.check',now()+interval '1 hour')`)

	h := step4Handlers(t, app)
	r := step5Router(tenant, actor, nil, func(r chi.Router) {
		r.Get("/v1/nodes", h.nodeList)
		r.Get("/v1/node-pools", h.listNodePools)
		r.Post("/v1/nodes/{id}/retire", h.nodeRetire)
		r.Get("/v1/nodes/routing", h.nodeGetGlobalRouting)
		r.Put("/v1/nodes/routing", h.nodeSetGlobalRouting)
	})
	do := func(method, path, body string, code int, dst any) string {
		t.Helper()
		w := step3Do(t, ctx, r, method, path, body)
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

	var list struct {
		Nodes []struct {
			ID              string   `json:"id"`
			RowVersion      int64    `json:"row_version"`
			TrafficBytes24h *int64   `json:"traffic_bytes_24h"`
			CPUPercent      *float64 `json:"cpu_percent"`
			MemPercent      *float64 `json:"mem_percent"`
			MetricsAt       *string  `json:"metrics_at"`
		} `json:"nodes"`
	}
	do(http.MethodGet, "/v1/nodes", "", http.StatusOK, &list)
	if len(list.Nodes) != 3 || list.Nodes[0].ID != nodes[1] || list.Nodes[1].ID != nodes[2] || list.Nodes[2].ID != nodes[0] {
		t.Fatalf("node order=%+v, want sort_order then node_no", list.Nodes)
	}
	for _, n := range list.Nodes {
		if n.TrafficBytes24h == nil || *n.TrafficBytes24h != 0 || n.CPUPercent == nil || *n.CPUPercent != 12.34 ||
			n.MemPercent == nil || *n.MemPercent != 25 || n.MetricsAt == nil {
			t.Fatalf("node metrics=%+v", n)
		}
	}

	var pools struct {
		Pools []struct {
			Members   []map[string]any `json:"members"`
			PlanNames []string         `json:"plan_names"`
		} `json:"pools"`
	}
	do(http.MethodGet, "/v1/node-pools", "", http.StatusOK, &pools)
	if len(pools.Pools) != 1 || len(pools.Pools[0].Members) != 3 || pools.Pools[0].PlanNames == nil ||
		pools.Pools[0].Members[0]["id"] != nodes[1] {
		t.Fatalf("pools=%+v", pools)
	}

	// 一步退役：active → draining → retired，服务状态 retired，在途任务失败
	version := map[string]int64{}
	for _, n := range list.Nodes {
		version[n.ID] = n.RowVersion
	}
	do(http.MethodPost, "/v1/nodes/"+nodes[1]+"/retire", `{"row_version":999,"reason":"x"}`, http.StatusConflict, nil)
	do(http.MethodPost, "/v1/nodes/"+nodes[0]+"/retire", fmt.Sprintf(`{"row_version":%d}`, version[nodes[0]]), http.StatusConflict, nil)
	do(http.MethodPost, "/v1/nodes/not-a-uuid/retire", `{"row_version":1}`, http.StatusNotFound, nil)
	body := do(http.MethodPost, "/v1/nodes/"+nodes[1]+"/retire", fmt.Sprintf(`{"row_version":%d,"reason":"机房到期"}`, version[nodes[1]]), http.StatusOK, nil)
	if !strings.Contains(body, `"serving_status":"retired"`) {
		t.Fatalf("retire body=%s", body)
	}
	var status, serving, task string
	var desired *int
	if err := admin.QueryRow(ctx, `SELECT n.status, n.serving_status, n.desired_config_version,
		(SELECT status FROM node_tasks WHERE node_id=n.id) FROM nodes n WHERE n.id=$1`, nodes[1]).
		Scan(&status, &serving, &desired, &task); err != nil || status != "retired" || serving != "retired" || desired != nil || task != "failed" {
		t.Fatalf("retired node status=%s serving=%s desired=%v task=%s err=%v", status, serving, desired, task, err)
	}
	var retireAudits int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.retire'`, tenant).Scan(&retireAudits); err != nil || retireAudits != 1 {
		t.Fatalf("node.retire audits=%d err=%v", retireAudits, err)
	}
	do(http.MethodPost, "/v1/nodes/"+nodes[1]+"/retire", fmt.Sprintf(`{"row_version":%d}`, version[nodes[1]]+1), http.StatusConflict, nil)

	// 全局路由：空集也有 revision；发布推进全部未退役节点；旧 revision 409
	var g struct {
		Revision string `json:"revision"`
		Routes   []any  `json:"routes"`
	}
	do(http.MethodGet, "/v1/nodes/routing", "", http.StatusOK, &g)
	if len(g.Revision) != 64 {
		t.Fatalf("empty revision=%q", g.Revision)
	}
	r0 := g.Revision
	var put struct {
		Revision      string `json:"revision"`
		AffectedNodes int    `json:"affected_nodes"`
	}
	do(http.MethodPut, "/v1/nodes/routing", `{"expected_revision":"`+r0+`",
		"outbounds":[{"tag":"US-LAX","type":"trojan","settings":{"server":"lax.invalid"}}],
		"routes":[{"matcher":{"domain_suffix":"example.com"},"outbound_tag":"US-LAX","enabled":true}]}`, http.StatusOK, &put)
	if put.AffectedNodes != 2 || put.Revision == r0 {
		t.Fatalf("publish=%+v", put)
	}
	do(http.MethodGet, "/v1/nodes/routing", "", http.StatusOK, &g)
	if g.Revision != put.Revision || len(g.Routes) != 1 {
		t.Fatalf("after publish revision=%s routes=%d", g.Revision, len(g.Routes))
	}
	do(http.MethodPut, "/v1/nodes/routing", `{"expected_revision":"`+r0+`","outbounds":[],"routes":[]}`, http.StatusConflict, nil)
	do(http.MethodPut, "/v1/nodes/routing", `{"expected_revision":"`+put.Revision+`","outbounds":[],
		"routes":[{"matcher":{"domain_suffix":"a.test"},"outbound_tag":"nowhere","enabled":true}]}`, http.StatusUnprocessableEntity, nil)

	// 节点私有规则引用了全局出站：删它 409，并点名节点
	step3Seed(t, ctx, admin, `INSERT INTO node_routes(tenant_id,node_id,priority,matcher,outbound_tag,enabled)
		VALUES('`+tenant+`','`+nodes[2]+`',10,'{"domain_suffix":"private.test"}','us-lax',true)`)
	if b := do(http.MethodPut, "/v1/nodes/routing", `{"expected_revision":"`+put.Revision+`","outbounds":[],"routes":[]}`, http.StatusConflict, nil); !strings.Contains(b, "step3-node-3") {
		t.Fatalf("referenced outbound delete body=%s", b)
	}
	// 生效规则：节点私有在前、全局在后
	_, routes, err := h.d.Node.LoadRouting(ctx, tenant, nodes[2])
	if err != nil || len(routes) != 2 || !strings.Contains(string(routes[0].Matcher), "private.test") ||
		!strings.Contains(string(routes[1].Matcher), "example.com") {
		t.Fatalf("effective routes=%+v err=%v", routes, err)
	}
}

func TestContentNotifyStep5PG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000701"
		actor  = "87000000-0000-4000-8000-000000000711"
		group  = "87000000-0000-4000-8000-000000000721"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','cn5-pg18','CN5','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@cn5.invalid','Ops','active')`,
		`INSERT INTO user_groups(id,tenant_id,code,name) VALUES('`+group+`','`+tenant+`','beta','内测')`,
		`INSERT INTO notification_templates(tenant_id,code,channel,subject,body,allowed_variables,status) VALUES
		   ('`+tenant+`','quota.warning','email','流量 {{percent}}%','{{plan}} 剩余 {{remaining}}','{site,plan,percent,remaining}','active')`)
	h := step4Handlers(t, app)
	h.d.Notify = notify.New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("cn5-salt"))
	r := step5Router(tenant, actor, nil, func(r chi.Router) {
		r.Get("/v1/announcements", h.listAnnouncements)
		r.Post("/v1/announcements", h.saveAnnouncement)
		r.Get("/v1/settings/telegram", h.getTelegramSettings)
		r.Post("/v1/settings/telegram", h.setTelegramSettings)
		r.Post("/v1/settings/telegram/test", h.testTelegram)
		r.Post("/v1/mail/templates/preview", h.previewMailTemplate)
		r.Post("/v1/mail/templates/test", h.testMailTemplate)
		r.Get("/v1/plugin-hooks", h.listHooks)
		r.Get("/v1/access-log", h.accessLogList)
	})
	expect := func(method, path, body string, code int, contains ...string) string {
		t.Helper()
		w := step3Do(t, ctx, r, method, path, body)
		if w.Code != code {
			t.Fatalf("%s %s: status=%d body=%s, want %d", method, path, w.Code, w.Body.String(), code)
		}
		for _, c := range contains {
			if !strings.Contains(w.Body.String(), c) {
				t.Fatalf("%s %s: body=%s, want %q", method, path, w.Body.String(), c)
			}
		}
		return w.Body.String()
	}

	// 公告：用户组定向可写可读，组必须属于本租户
	expect(http.MethodPost, "/v1/announcements", `{"title":"维护通知","body":"今晚维护","target_user_group_ids":["`+group+`"],"expected_version":0}`, http.StatusOK)
	expect(http.MethodPost, "/v1/announcements", `{"title":"维护通知","body":"今晚维护","target_user_group_ids":["87000000-0000-4000-8000-0000000007ff"],"expected_version":0}`,
		http.StatusUnprocessableEntity, "target_user_group_ids")
	expect(http.MethodGet, "/v1/announcements", "", http.StatusOK,
		`"target_user_group_ids":["`+group+`"]`, `"user_group_targets":[{"id":"`+group+`","name":"内测"}]`, `"user_groups":[{"id":"`+group+`","name":"内测"}]`)

	// Telegram：管理员群组 chat id 存取；测试发送省略 chat_id 时用它
	expect(http.MethodPost, "/v1/settings/telegram/test", `{}`, http.StatusUnprocessableEntity, `"chat_id"`)
	expect(http.MethodPost, "/v1/settings/telegram", `{"enabled":false,"bot_username":"","bot_token":"","admin_chat_id":0}`, http.StatusUnprocessableEntity, `"admin_chat_id"`)
	expect(http.MethodPost, "/v1/settings/telegram", `{"enabled":false,"bot_username":"","bot_token":"","admin_chat_id":-1001234567890}`, http.StatusOK)
	expect(http.MethodGet, "/v1/settings/telegram", "", http.StatusOK, `"admin_chat_id":-1001234567890`)
	// 过了 chat_id 这一关，停在「没配置好」上
	expect(http.MethodPost, "/v1/settings/telegram/test", `{}`, http.StatusUnprocessableEntity, "还没配置好")
	expect(http.MethodPost, "/v1/settings/telegram", `{"enabled":false,"bot_username":"","bot_token":""}`, http.StatusOK)
	expect(http.MethodGet, "/v1/settings/telegram", "", http.StatusOK, `"admin_chat_id":-1001234567890`)
	expect(http.MethodPost, "/v1/settings/telegram", `{"enabled":false,"bot_username":"","bot_token":"","admin_chat_id":null}`, http.StatusOK)
	expect(http.MethodGet, "/v1/settings/telegram", "", http.StatusOK, `"admin_chat_id":null`)

	// 模板草稿：预览标出白名单外的变量；缺 code 400、模板不存在 404；草稿测试先校验
	expect(http.MethodPost, "/v1/mail/templates/preview", `{"code":"quota.warning","channel":"email","subject":"{{percent}}%","body":"{{plan}} 与 {{bogus}}"}`,
		http.StatusOK, `"preview_body":"旗舰套餐 与 {{bogus}}"`, `"unknown_variables":["bogus"]`, `"preview_subject":"85%"`)
	expect(http.MethodPost, "/v1/mail/templates/preview", `{"channel":"email","subject":"a","body":"b"}`, http.StatusBadRequest)
	expect(http.MethodPost, "/v1/mail/templates/preview", `{"code":"nope","channel":"email","subject":"a","body":"b"}`, http.StatusNotFound)
	expect(http.MethodPost, "/v1/mail/templates/test", `{"code":"quota.warning","channel":"email","to":"a@b.invalid","subject":"s","body":"{{bogus}}"}`,
		http.StatusUnprocessableEntity, `"body"`)
	expect(http.MethodPost, "/v1/mail/templates/test", `{"code":"quota.warning","channel":"email","to":"a@b.invalid","subject":"s","body":"{{plan}}"}`,
		http.StatusUnprocessableEntity, "SMTP 还没配置好")

	// 钩子：没有钩子时是空数组；事件目录键小写；近 7 天发送数
	expect(http.MethodGet, "/v1/plugin-hooks", "", http.StatusOK, `"hooks":[]`, `{"name":"user.registered","desc":"用户完成注册"}`)
	if _, err := h.d.Plugin.SaveHook(ctx, tenant, plugin.SaveHookInput{Code: "crm", Name: "CRM", Enabled: true,
		Events: []string{"user.registered"}, EndpointURL: "https://hooks.example.com/crm", TimeoutMS: 3000, MaxAttempts: 3, ActorID: actor}); err != nil {
		t.Fatal(err)
	}
	step3Seed(t, ctx, admin, `INSERT INTO plugin_hook_deliveries(tenant_id,hook_id,event,dedupe_key,payload,status,sent_at)
		SELECT tenant_id,id,'user.registered','d-'||g,'{}','sent',now() FROM plugin_hooks, generate_series(1,2) g WHERE tenant_id='`+tenant+`'`)
	expect(http.MethodGet, "/v1/plugin-hooks", "", http.StatusOK, `"sent_count_7d":2`)

	// 访问日志 outcome：error = 非 success（订阅拉取 ok 以外也算）；denied 不查订阅日志
	uid := actor
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		for _, oc := range []string{"success", "failure", "denied"} {
			if err := audit.Write(ctx, tx, tenant, audit.Entry{ActorKind: "admin", ActorID: &uid,
				Action: "admin.test_" + oc, Outcome: oc}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	step3Seed(t, ctx, admin, `INSERT INTO subscription_fetch_log(tenant_id,result) VALUES('`+tenant+`','ok'),('`+tenant+`','not_found')`)
	count := func(query string) int {
		t.Helper()
		var body struct {
			Items []struct {
				Action string `json:"action"`
			} `json:"items"`
		}
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/access-log?limit=200&"+query, "")
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("access-log %s: status=%d body=%s", query, w.Code, w.Body.String())
		}
		n := 0
		for _, it := range body.Items {
			if strings.HasPrefix(it.Action, "admin.test_") || it.Action == "subscription.fetch" {
				n++
			}
		}
		return n
	}
	for query, want := range map[string]int{"outcome=": 5, "outcome=error": 3, "outcome=denied": 1, "outcome=success": 2} {
		if got := count(query); got != want {
			t.Errorf("access-log %s: %d rows, want %d", query, got, want)
		}
	}
	expect(http.MethodGet, "/v1/access-log?outcome=weird", "", http.StatusUnprocessableEntity, `"outcome"`)
}
