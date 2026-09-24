// [INPUT]: 依赖 announcement_pg18_test.go 的 openAnnouncementPG18、step3_pg18_test.go 的 step3Seed / step3Do / step3Nodes，依赖 risk.go、audit_log.go、node_admin.go、appearance.go 的处理器，依赖 platform/audit 与 crypto 写出带密文来源 IP 的审计样本
// [OUTPUT]: 对外提供 TestIPClusterPG18、TestAuditLogPG18、TestNodeCountryAndCredentialsPG18、TestPluginDeliveryDurationPG18
// [POS]: api/admin 的第 ④ 步 PG18 测试：风控聚类的复核与批量停用（M1）、审计认证强度与导出（M6）、节点国家与令牌签发记录（M8）、webhook 投递耗时（M7）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// step4Handlers 在 step3 的依赖之上补齐本步用到的服务，并配一个真信封：
// 来源 IP 的加解密要走生产同一条路径，测试才证明得了明文能回到界面上。
func step4Handlers(t *testing.T, app *platformdb.Pool) *handlers {
	t.Helper()
	env, err := crypto.NewEnvelope([]byte("step4-pg18-envelope-key-32bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	audit.Configure(func(ip string) []byte {
		sum := sha256.Sum256([]byte(ip))
		return sum[:]
	}, func(plain []byte) ([]byte, error) { return env.Seal(plain, []byte("audit")) })
	t.Cleanup(func() { audit.Configure(nil, nil) })
	return &handlers{d: Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Node: nodefabric.NewService(app, nil), Ops: adminops.NewService(app),
		Plugin: plugin.New(app, nil, true), Envelope: env}}
}

// step4Router 挂上被测处理器；主体带会话且刚重认证过，审计的认证强度由此而来。
func step4Router(tenant, actor string, h *handlers) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, SessionID: "0190a000-0000-7000-8000-00000000c0de",
				ReauthedRecently: true})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	r.Get("/v1/ip-clusters", h.ipClusters)
	r.Post("/v1/ip-clusters/{key}/review", h.reviewIPCluster)
	r.Post("/v1/ip-clusters/{key}/disable-accounts", h.disableIPClusterAccounts)
	r.Get("/v1/audit", h.listAudit)
	r.Get("/v1/audit/export", h.exportAudit)
	r.Get("/v1/nodes", h.nodeList)
	r.Patch("/v1/nodes/{id}", h.patchAdminNode)
	r.Get("/v1/nodes/{id}/identity", h.nodeIdentity)
	r.Get("/v1/plugin-hooks/{code}/deliveries", h.hookDeliveries)
	r.Post("/v1/plugin-hooks/{code}/test", h.testHook)
	return r
}

// userAuditIP 以 user 身份、从给定 IP 写一条审计，喂给 audit_ip_clusters 视图。
func userAuditIP(t *testing.T, ctx context.Context, app *platformdb.Pool, tenant, user, ip string) {
	t.Helper()
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, tenant, audit.Entry{ActorKind: "user", ActorID: &user,
			Action: "user.login", APIDomain: "public", SourceIP: ip})
	}); err != nil {
		t.Fatalf("write user audit: %v", err)
	}
}

func ipKey(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}

func TestIPClusterPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant    = "82000000-0000-4000-8000-000000000001"
		actor     = "82000000-0000-4000-8000-000000000011"
		plain1    = "82000000-0000-4000-8000-000000000012"
		plain2    = "82000000-0000-4000-8000-000000000013"
		plain3    = "82000000-0000-4000-8000-000000000014"
		staff     = "82000000-0000-4000-8000-000000000015"
		suspended = "82000000-0000-4000-8000-000000000016"
		outsider  = "82000000-0000-4000-8000-000000000017"
		role      = "82000000-0000-4000-8000-000000000021"
		hotIP     = "203.0.113.7"
		quietIP   = "198.51.100.9"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','ip-cluster-pg18','IP Cluster','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
			('`+actor+`','`+tenant+`','ops@cluster.invalid','Ops','active'),
			('`+plain1+`','`+tenant+`','a@cluster.invalid','A','active'),
			('`+plain2+`','`+tenant+`','b@cluster.invalid','B','active'),
			('`+plain3+`','`+tenant+`','c@cluster.invalid','C','active'),
			('`+staff+`','`+tenant+`','staff@cluster.invalid','Staff','active'),
			('`+suspended+`','`+tenant+`','s@cluster.invalid','S','suspended'),
			('`+outsider+`','`+tenant+`','o@cluster.invalid','O','active')`,
		`INSERT INTO roles(id,tenant_id,code,name) VALUES('`+role+`','`+tenant+`','cluster_admin','Cluster Admin')`,
		`INSERT INTO role_permissions(role_id,permission_code) VALUES('`+role+`','iam.user.write'),('`+role+`','iam.role.write')`,
		`INSERT INTO role_bindings(tenant_id,user_id,role_id,scope_type) VALUES
			('`+tenant+`','`+actor+`','`+role+`','tenant'),('`+tenant+`','`+staff+`','`+role+`','tenant')`)
	h := step4Handlers(t, app)
	for _, u := range []string{actor, plain1, plain2, plain3, staff, suspended} {
		userAuditIP(t, ctx, app, tenant, u, hotIP)
	}
	userAuditIP(t, ctx, app, tenant, plain1, quietIP)
	userAuditIP(t, ctx, app, tenant, outsider, quietIP)
	r := step4Router(tenant, actor, h)

	type cluster struct {
		Key, IP, Risk string
		Accounts      int
		Users         []struct{ ID, Email, Status string }
		Review        *struct {
			Decision  string
			ExpiresAt *time.Time `json:"expires_at"`
		}
	}
	list := func(query string) []cluster {
		t.Helper()
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/ip-clusters"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("ip-clusters%s: status=%d body=%s", query, w.Code, w.Body.String())
		}
		var body struct{ Clusters []cluster }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Clusters
	}
	got := list("")
	if len(got) != 2 || got[0].Key != ipKey(hotIP) || got[0].IP != hotIP || got[0].Accounts != 6 ||
		got[0].Risk != "high" || len(got[0].Users) != 6 || got[0].Review != nil ||
		got[1].Key != ipKey(quietIP) || got[1].Risk != "low" {
		t.Fatalf("initial clusters: %+v", got)
	}

	// 标记为正常：默认列表里消失，include_reviewed=1 时带着结论回来
	if w := step3Do(t, ctx, r, http.MethodPost, "/v1/ip-clusters/"+ipKey(quietIP)+"/review", `{"note":"同一家庭"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"decision":"normal"`) {
		t.Fatalf("review: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := list(""); len(got) != 1 || got[0].Key != ipKey(hotIP) {
		t.Fatalf("reviewed cluster still listed: %+v", got)
	}
	if got := list("?include_reviewed=1"); len(got) != 2 || got[1].Review == nil || got[1].Review.Decision != "normal" ||
		got[1].Review.ExpiresAt == nil || got[1].Review.ExpiresAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("include_reviewed: %+v", got)
	}
	for _, key := range []string{"zz", ipKey("192.0.2.1")} {
		if w := step3Do(t, ctx, r, http.MethodPost, "/v1/ip-clusters/"+key+"/review", `{}`); w.Code != http.StatusNotFound {
			t.Fatalf("review unknown key %s: status=%d", key, w.Code)
		}
	}

	// 批量停用：原因太短 422，什么都不动
	disable := "/v1/ip-clusters/" + ipKey(hotIP) + "/disable-accounts"
	if w := step3Do(t, ctx, r, http.MethodPost, disable, `{"user_ids":["`+plain1+`"],"reason":"刷单"}`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"reason"`) {
		t.Fatalf("short reason: status=%d body=%s", w.Code, w.Body.String())
	}
	w := step3Do(t, ctx, r, http.MethodPost, disable, `{"user_ids":["`+actor+`","`+plain1+`","`+plain2+`","`+staff+`","`+suspended+`","`+outsider+`"],"reason":"同一机房批量注册"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: status=%d body=%s", w.Code, w.Body.String())
	}
	var res struct {
		Disabled int
		Skipped  []struct {
			UserID string `json:"user_id"`
			Reason string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	wantSkip := map[string]string{actor: "self", staff: "administrator", suspended: "already_disabled", outsider: "not_member"}
	if res.Disabled != 2 || len(res.Skipped) != len(wantSkip) {
		t.Fatalf("disable result: %s", w.Body.String())
	}
	for _, s := range res.Skipped {
		if wantSkip[s.UserID] != s.Reason {
			t.Fatalf("skip %s reason %s, want %s", s.UserID, s.Reason, wantSkip[s.UserID])
		}
	}
	statusOf := func(id string) (s string) {
		if err := admin.QueryRow(ctx, `SELECT status::text FROM users WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return
	}
	for id, want := range map[string]string{plain1: "suspended", plain2: "suspended", plain3: "active", staff: "active", actor: "active", outsider: "active"} {
		if got := statusOf(id); got != want {
			t.Fatalf("user %s status %s, want %s", id, got, want)
		}
	}
	var decision string
	var changes, clusterAudits int
	if err := admin.QueryRow(ctx, `SELECT decision FROM ip_cluster_reviews WHERE tenant_id=$1 AND source_ip_hash=decode($2,'hex')`,
		tenant, ipKey(hotIP)).Scan(&decision); err != nil || decision != "disabled" {
		t.Fatalf("review row decision=%q err=%v", decision, err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='user.status_change'),
		count(*) FILTER (WHERE action='risk.ip_cluster.disable' AND outcome='partial' AND auth_context='reauth')
		FROM audit_events WHERE tenant_id=$1`, tenant).Scan(&changes, &clusterAudits); err != nil || changes != 2 || clusterAudits != 1 {
		t.Fatalf("audit status_change=%d disable=%d err=%v", changes, clusterAudits, err)
	}
}

func TestAuditLogPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "83000000-0000-4000-8000-000000000001"
		actor  = "83000000-0000-4000-8000-000000000011"
		target = "83000000-0000-4000-8000-000000000012"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','audit-log-pg18','Audit Log','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
			('`+actor+`','`+tenant+`','auditor@audit-log.invalid','Auditor','active'),
			('`+target+`','`+tenant+`','=cmd@audit-log.invalid','Target','active')`)
	h := step4Handlers(t, app)
	node := step3Nodes(t, ctx, admin, tenant, "83000000-0000-4000-8000-", 1)[0]

	session := &httpx.Principal{Kind: "admin", UserID: actor, TenantID: tenant, SessionID: "s-1"}
	reauthed := &httpx.Principal{Kind: "admin", UserID: actor, TenantID: tenant, SessionID: "s-1", ReauthedRecently: true}
	write := func(p *httpx.Principal, e audit.Entry) {
		t.Helper()
		c := httpx.WithClientInfo(httpx.WithPrincipal(ctx, p), "192.0.2.44", "pg18")
		if err := app.InTx(c, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			return audit.Write(c, tx, tenant, e)
		}); err != nil {
			t.Fatalf("write audit %s: %v", e.Action, err)
		}
	}
	a, tg, n := actor, target, node
	write(session, audit.Entry{ActorKind: "admin", ActorID: &a, Action: "user.status_change",
		ResourceType: "user", ResourceID: &tg, APIDomain: "admin", AfterDigest: map[string]any{"reason": "测试"}})
	write(reauthed, audit.Entry{ActorKind: "admin", ActorID: &a, Action: "node.server_token.issue",
		ResourceType: "node", ResourceID: &n, APIDomain: "admin"})
	write(nil, audit.Entry{ActorKind: "system", Action: "order.expired"})

	r := step4Router(tenant, actor, h)
	type event struct {
		Action        string
		ResourceLabel *string `json:"resource_label"`
		AuthContext   *string `json:"auth_context"`
		SourceIP      *string `json:"source_ip"`
		Reason        *string
	}
	list := func(query string) ([]event, int) {
		t.Helper()
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/audit"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("audit%s: status=%d body=%s", query, w.Code, w.Body.String())
		}
		var body struct {
			Events []event
			Total  int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Events, body.Total
	}
	events, total := list("")
	if total != 3 || len(events) != 3 {
		t.Fatalf("audit total=%d events=%+v", total, events)
	}
	byAction := map[string]event{}
	for _, e := range events {
		byAction[e.Action] = e
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	if e := byAction["user.status_change"]; str(e.ResourceLabel) != "=cmd@audit-log.invalid" || str(e.AuthContext) != "session" || str(e.SourceIP) != "192.0.2.44" || str(e.Reason) != "测试" {
		t.Fatalf("user event: %+v label=%s auth=%s ip=%s", e, str(e.ResourceLabel), str(e.AuthContext), str(e.SourceIP))
	}
	if e := byAction["node.server_token.issue"]; str(e.ResourceLabel) != "step3-node-1" || str(e.AuthContext) != "reauth" {
		t.Fatalf("node event label=%s auth=%s", str(e.ResourceLabel), str(e.AuthContext))
	}
	if e := byAction["order.expired"]; e.AuthContext != nil || e.ResourceLabel != nil {
		t.Fatalf("system event carries auth/label: %+v", e)
	}
	// q：动作片段、操作者邮箱片段、对象 id 精确
	for query, want := range map[string]int{"?q=server_token": 1, "?q=AUDITOR@": 2, "?q=" + node: 1, "?q=nothing-matches": 0} {
		if _, total := list(query); total != want {
			t.Fatalf("audit%s total=%d, want %d", query, total, want)
		}
	}

	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/audit/export?from=2026-13-01", ""); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"from"`) {
		t.Fatalf("bad from: status=%d body=%s", w.Code, w.Body.String())
	}
	today := time.Now().UTC().Format("2006-01-02")
	w := step3Do(t, ctx, r, http.MethodGet, "/v1/audit/export?actor_kind=admin&from="+today+"&to="+today, "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="audit-`) {
		t.Fatalf("export: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(strings.TrimPrefix(w.Body.String(), "\xef\xbb\xbf")), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "occurred_at,actor_kind,actor_email,action") ||
		!strings.Contains(w.Body.String(), "'=cmd@audit-log.invalid") || !strings.Contains(w.Body.String(), "192.0.2.44") {
		t.Fatalf("export body:\n%s", w.Body.String())
	}
	var exports int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='audit.export'
		AND (after_digest->>'rows')::int = 2 AND auth_context='reauth'`, tenant).Scan(&exports); err != nil || exports != 1 {
		t.Fatalf("audit.export rows=%d err=%v", exports, err)
	}
	// 不在这里跑 audit.VerifyChain：它从 jsonb 读回摘要再算哈希，而 jsonb 的输出
	// 字节与写入时 json.Marshal 的不同（键序、冒号后空格），任何带摘要的记录都
	// 复算不出——既有缺陷，已报告协调会话。auth_context 入链由 audit 包单测证明。
}

func TestNodeCountryAndCredentialsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "84000000-0000-4000-8000-000000000001"
		actor  = "84000000-0000-4000-8000-000000000011"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','node-cc-pg18','Node CC','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@node-cc.invalid','Ops','active')`)
	node := step3Nodes(t, ctx, admin, tenant, "84000000-0000-4000-8000-", 1)[0]
	h := step4Handlers(t, app)
	r := step4Router(tenant, actor, h)

	countryOf := func() *string {
		var cc *string
		if err := admin.QueryRow(ctx, `SELECT country_code FROM nodes WHERE id=$1`, node).Scan(&cc); err != nil {
			t.Fatal(err)
		}
		return cc
	}
	if w := step3Do(t, ctx, r, http.MethodPatch, "/v1/nodes/"+node, `{"row_version":1,"country_code":"JPN"}`); w.Code != http.StatusUnprocessableEntity || countryOf() != nil {
		t.Fatalf("bad country: status=%d body=%s", w.Code, w.Body.String())
	}
	w := step3Do(t, ctx, r, http.MethodPatch, "/v1/nodes/"+node, `{"row_version":1,"country_code":"jp"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"country_code":"JP"`) {
		t.Fatalf("set country: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/nodes", ""); !strings.Contains(w.Body.String(), `"country_code":"JP"`) {
		t.Fatalf("node list lacks country: %s", w.Body.String())
	}
	// 省略=不改，null=清空
	if w := step3Do(t, ctx, r, http.MethodPatch, "/v1/nodes/"+node, `{"row_version":2,"name":"renamed"}`); w.Code != http.StatusOK || countryOf() == nil {
		t.Fatalf("omitted country cleared it: status=%d", w.Code)
	}
	if w := step3Do(t, ctx, r, http.MethodPatch, "/v1/nodes/"+node, `{"row_version":3,"country_code":null}`); w.Code != http.StatusOK || countryOf() != nil {
		t.Fatalf("null did not clear country: status=%d", w.Code)
	}

	identity := func() map[string]any {
		t.Helper()
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/nodes/"+node+"/identity", "")
		if w.Code != http.StatusOK {
			t.Fatalf("identity: status=%d body=%s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if got := identity(); got["identity"] != nil || got["server_token"].(map[string]any)["present"] != false || got["bootstrap_tokens_pending"] != float64(0) {
		t.Fatalf("fresh node identity: %+v", got)
	}
	issueCtx := httpx.WithPrincipal(ctx, &httpx.Principal{Kind: "admin", UserID: actor, TenantID: tenant})
	if _, _, err := h.d.Node.IssueServerToken(issueCtx, tenant, actor, node); err != nil {
		t.Fatalf("issue server token: %v", err)
	}
	step3Seed(t, ctx, admin,
		`INSERT INTO node_identities(tenant_id,node_id,serial,public_key,spiffe_id,fingerprint,expires_at)
		 VALUES('`+tenant+`','`+node+`',1,'\x01','spiffe://aegis/tenant/`+tenant+`/node/`+node+`','\xabcd',now()+interval '90 days')`,
		`INSERT INTO bootstrap_tokens(tenant_id,token_hash,node_id,expires_at) VALUES
		   ('`+tenant+`','\x01','`+node+`',now()+interval '10 minutes'),
		   ('`+tenant+`','\x02','`+node+`',now()-interval '1 minute')`)
	got := identity()
	token := got["server_token"].(map[string]any)
	id, _ := got["identity"].(map[string]any)
	if token["present"] != true || token["issued_by"] != actor || token["issued_by_name"] != "Ops" || token["issued_at"] == nil ||
		id == nil || id["serial"] != float64(1) || id["fingerprint_sha256"] != "abcd" || id["status"] != "active" ||
		got["bootstrap_tokens_pending"] != float64(1) {
		t.Fatalf("identity after issue: %+v", got)
	}
	for _, bad := range []string{"not-a-uuid", "84000000-0000-4000-8000-00000000ffff"} {
		if w := step3Do(t, ctx, r, http.MethodGet, "/v1/nodes/"+bad+"/identity", ""); w.Code != http.StatusNotFound {
			t.Fatalf("identity %s: status=%d", bad, w.Code)
		}
	}
}

func TestPluginDeliveryDurationPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const tenant = "85000000-0000-4000-8000-000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','hook-pg18','Hook','CNY')`,
		`INSERT INTO plugin_hooks(tenant_id,code,name,enabled,events,endpoint_url) VALUES('`+tenant+`','timing','Timing',true,'{order.paid}','`+srv.URL+`')`)
	h := step4Handlers(t, app)
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		return plugin.Emit(ctx, tx, tenant, "order.paid", "order-1", map[string]any{"order_id": "1"})
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if n, err := h.d.Plugin.Dispatch(ctx, tenant, 10); err != nil || n != 1 {
		t.Fatalf("dispatch n=%d err=%v", n, err)
	}
	r := step4Router(tenant, "", h)
	w := step3Do(t, ctx, r, http.MethodGet, "/v1/plugin-hooks/timing/deliveries", "")
	var body struct {
		Deliveries []struct {
			Status     string
			DurationMS *int `json:"duration_ms"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Deliveries) != 1 {
		t.Fatalf("deliveries: %s err=%v", w.Body.String(), err)
	}
	if d := body.Deliveries[0]; d.Status != "sent" || d.DurationMS == nil || *d.DurationMS < 20 {
		t.Fatalf("delivery %+v, want sent with duration >= 20ms", d)
	}
	w = step3Do(t, ctx, r, http.MethodPost, "/v1/plugin-hooks/timing/test", "")
	var test struct {
		Sent       bool
		DurationMS *int `json:"duration_ms"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &test); err != nil || !test.Sent || test.DurationMS == nil || *test.DurationMS < 20 {
		t.Fatalf("test hook: status=%d body=%s", w.Code, w.Body.String())
	}
}
