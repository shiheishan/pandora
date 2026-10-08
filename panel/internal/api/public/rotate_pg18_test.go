package public

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// TestSubscriptionRotatePG18 钉住「重置订阅链接时节点密码一起换」（用户 2026-10-07）：
//
//   - 门户重置：旧链接 404、proxy_uuid 换了、下发纪元前进、节点名单里换成新 UUID、
//     新链接拉到的配置里是新 UUID（旧 UUID 不再出现）；
//   - 后台替用户换发（AdminRotate）同样：旧链接 404、proxy_uuid 又换了一次、纪元前进、
//     审计与换发同一事务落库；
//   - 别人的订阅不受影响。
func TestSubscriptionRotatePG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const (
		tenant   = "7d5e0000-0000-4000-8000-000000000001"
		user     = "7d5e0000-0000-4000-8000-000000000021"
		other    = "7d5e0000-0000-4000-8000-000000000022"
		operator = "7d5e0000-0000-4000-8000-000000000023"
		product  = "7d5e0000-0000-4000-8000-000000000031"
		plan     = "7d5e0000-0000-4000-8000-000000000032"
		planVer  = "7d5e0000-0000-4000-8000-000000000033"
		pool     = "7d5e0000-0000-4000-8000-000000000041"
		server   = "7d5e0000-0000-4000-8000-000000000051"
		node     = "7d5e0000-0000-4000-8000-000000000061"
		sub      = "7d5e0000-0000-4000-8000-000000000071"
		otherSub = "7d5e0000-0000-4000-8000-000000000072"
		prefix   = "r07a7e5ub123"
	)
	// 虚构令牌：低熵、互不相同、长度过 16 的门槛
	tok, otherTok := strings.Repeat("rota", 9), strings.Repeat("othr", 9)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed rotate fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency,sub_path_prefix) VALUES($1,'rotate-pg18','Rotate PG18','USD',$2)`, tenant, prefix)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
	      ($2,$1,'rotate@rotate.invalid','Rotate','active'),($3,$1,'other@rotate.invalid','Other','active'),
	      ($4,$1,'operator@rotate.invalid','Operator','active')`, tenant, user, other, operator)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'rotate-product','Rotate Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'rotate','Rotate','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'rotate-plan','Rotate Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'rotate-server','ready')`, tenant, server)
	must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,sort_order)
		  VALUES($2,$1,'Rotate Line','Rotate Line',$3,'active','vless','rotate.pull.invalid',443,$4,'active',1,now(),now(),0)`,
		tenant, node, pool, server)
	for _, s := range []struct{ id, user, token string }{{sub, user, tok}, {otherSub, other, otherTok}} {
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
				current_period_start,current_period_end)
			  VALUES($1,$2,$3,$4,$5,'active','USD',100,now()-interval '10 days',now()+interval '20 days')`,
			s.id, tenant, s.user, plan, planVer)
		must(`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,expires_at)
			  VALUES($1,$2,$3,$4,$5,'subscription',now()+interval '20 days')`, tenant, s.id, s.user, crypto.HashToken(s.token), s.token[:8])
		must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
			  VALUES($1,$2,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',1000,1000,10)`, tenant, s.id)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := realtime.NewHub(nil, log)
	defer hub.Close()
	svc := subscription.New(app, []byte("rotate-pg18-salt"), nil)
	svc.AttachRealtime(hub)
	h := &handlers{d: Deps{Log: log, Subscription: svc, Realtime: hub,
		Cfg: &config.Config{PublicBaseURL: "https://portal.rotate.invalid"}}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithRequestID(c, "req-rotate-pg18")
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "user", Audience: "public", UserID: user, TenantID: tenant})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	r.Get("/{prefix}/{token}", h.subscribe)
	r.Post("/v1/me/subscriptions/{id}/rotate", h.rotateSubscriptionLink)
	pull := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/"+token+".yaml", nil).WithContext(ctx)
		req.Header.Set("User-Agent", "clash-verge/v2.0")
		req.Header.Set("X-Real-IP", "198.51.100.40")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	proxyUUID := func(id string) string {
		t.Helper()
		var v string
		if err := admin.QueryRow(ctx, `SELECT proxy_uuid::text FROM subscriptions WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	epoch := func() int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT last_value + is_called::int FROM node_delivery_epoch`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	nodes := nodefabric.NewService(app, nil)
	nodeUUIDs := func() map[string]bool {
		t.Helper()
		poolID := pool
		users, err := nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: node, PoolID: &poolID})
		if err != nil {
			t.Fatalf("list node users: %v", err)
		}
		out := map[string]bool{}
		for _, u := range users {
			out[u.UUID] = true
		}
		return out
	}

	oldUUID, otherUUID := proxyUUID(sub), proxyUUID(otherSub)
	if w := pull(tok); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), oldUUID) {
		t.Fatalf("pull before rotation: status=%d body=%s", w.Code, w.Body)
	}
	if got := nodeUUIDs(); !got[oldUUID] || !got[otherUUID] {
		t.Fatalf("node users before rotation=%v", got)
	}

	// --- 门户重置 ---
	before := epoch()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/subscriptions/"+sub+"/rotate", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var rotated struct {
		URL string `json:"url"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rotated) != nil ||
		!strings.HasPrefix(rotated.URL, "https://portal.rotate.invalid/"+prefix+"/") {
		t.Fatalf("portal rotation: status=%d body=%s", w.Code, w.Body)
	}
	newTok := rotated.URL[strings.LastIndex(rotated.URL, "/")+1:]
	newUUID := proxyUUID(sub)
	if newUUID == oldUUID || newTok == tok {
		t.Fatalf("portal rotation kept the node password or token: uuid %s -> %s", oldUUID, newUUID)
	}
	if after := epoch(); after <= before {
		t.Fatalf("delivery epoch did not advance on rotation: %d -> %d", before, after)
	}
	if w := pull(tok); w.Code != http.StatusNotFound {
		t.Fatalf("old link after rotation: status=%d", w.Code)
	}
	if w := pull(newTok); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), newUUID) ||
		strings.Contains(w.Body.String(), oldUUID) {
		t.Fatalf("new link must serve the new node password only: status=%d body=%s", w.Code, w.Body)
	}
	if got := nodeUUIDs(); got[oldUUID] || !got[newUUID] || !got[otherUUID] {
		t.Fatalf("node users after rotation=%v want new %s, without old %s", got, newUUID, oldUUID)
	}
	if proxyUUID(otherSub) != otherUUID {
		t.Fatal("rotation changed another user's node password")
	}
	// 门户换新链接与审计同一事务落库：用户本人、这一份、不记令牌（w8walk 第 5 节第 2 条）
	var userAudits int
	var digest string
	if err := admin.QueryRow(ctx, `
		SELECT count(*), coalesce(max(after_digest::text), '') FROM audit_events
		 WHERE tenant_id=$1 AND action='subscription.link_rotated' AND actor_kind='user'
		   AND actor_id=$2::uuid AND resource_type='subscription' AND resource_id=$3::uuid
		   AND api_domain='public' AND outcome='success'`,
		tenant, user, sub).Scan(&userAudits, &digest); err != nil || userAudits != 1 {
		t.Fatalf("portal rotation audits=%d err=%v", userAudits, err)
	}
	if strings.Contains(digest, newTok) || strings.Contains(digest, newTok[:8]) || !strings.Contains(digest, "old_revoked") {
		t.Fatalf("portal rotation audit digest=%s", digest)
	}
	t.Log("marker=rotate_pg18_portal_rotation_audited_ok")
	t.Log("marker=rotate_pg18_portal_rotates_proxy_uuid_ok")

	// --- 后台替用户换发：口径一致 ---
	before = epoch()
	out, err := svc.AdminRotate(ctx, tenant, subscription.AdminRotateInput{
		SubscriptionID: sub, ActorID: operator, Reason: "订阅链接泄露，替用户换发", APIDomain: "admin",
	})
	if err != nil || out.UserEmail != "rotate@rotate.invalid" {
		t.Fatalf("admin rotation out=%+v err=%v", out, err)
	}
	adminUUID := proxyUUID(sub)
	if adminUUID == newUUID || adminUUID == oldUUID {
		t.Fatalf("admin rotation kept the node password: %s", adminUUID)
	}
	if after := epoch(); after <= before {
		t.Fatalf("delivery epoch did not advance on admin rotation: %d -> %d", before, after)
	}
	if w := pull(newTok); w.Code != http.StatusNotFound {
		t.Fatalf("portal link after admin rotation: status=%d", w.Code)
	}
	if got := nodeUUIDs(); got[newUUID] || !got[adminUUID] {
		t.Fatalf("node users after admin rotation=%v want %s", got, adminUUID)
	}
	var audits, active int
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='subscription.link_rotated_by_admin'
		          AND resource_id=$2::uuid AND actor_id=$3::uuid),
		       (SELECT count(*) FROM subscription_credentials WHERE subscription_id=$2::uuid AND status='active')`,
		tenant, sub, operator).Scan(&audits, &active); err != nil || audits != 1 || active != 1 {
		t.Fatalf("admin rotation audits=%d active credentials=%d err=%v", audits, active, err)
	}
	t.Log("marker=rotate_pg18_admin_rotates_proxy_uuid_ok")

	// --- 按份限频（购买模型统一）：重置 A 后马上重置 B 都成功，10 分钟内再重置 A 回 429，
	// 被拒的那次不换链接 ---
	const subB = "7d5e0000-0000-4000-8000-000000000073"
	tokB := strings.Repeat("rotb", 9)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
			current_period_start,current_period_end)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100,now()-interval '10 days',now()+interval '20 days')`,
		subB, tenant, user, plan, planVer)
	must(`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,expires_at)
		  VALUES($1,$2,$3,$4,$5,'subscription',now()+interval '20 days')`, tenant, subB, user, crypto.HashToken(tokB), tokB[:8])
	rdb, _ := newLuaLimiterRedis(t)
	limited := chi.NewRouter()
	limited.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "user", Audience: "public", UserID: user, TenantID: tenant})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	limited.With(middleware.RateLimit(rdb, log, subscriptionRotateLimits()...)).
		Post("/v1/me/subscriptions/{id}/rotate", h.rotateSubscriptionLink)
	rotateLimited := func(id string) int {
		t.Helper()
		w := httptest.NewRecorder()
		limited.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/me/subscriptions/"+id+"/rotate", nil).WithContext(ctx))
		return w.Code
	}
	activeCredential := func(id string) string {
		t.Helper()
		var v string
		if err := admin.QueryRow(ctx, `SELECT id::text FROM subscription_credentials
			WHERE subscription_id=$1 AND status='active'`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if code := rotateLimited(sub); code != http.StatusOK {
		t.Fatalf("limited rotation of A: status=%d", code)
	}
	if code := rotateLimited(subB); code != http.StatusOK {
		t.Fatalf("rotating B right after A: status=%d", code)
	}
	credA := activeCredential(sub)
	if code := rotateLimited(sub); code != http.StatusTooManyRequests {
		t.Fatalf("rotating A again within 10 minutes: status=%d, want 429", code)
	}
	if activeCredential(sub) != credA {
		t.Fatal("a rate-limited rotation still replaced the link")
	}
	t.Log("marker=rotate_pg18_rate_limit_per_subscription_ok")
}
