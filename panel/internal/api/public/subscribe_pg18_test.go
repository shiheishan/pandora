package public

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// statementCounter 记下一条连接池发出的每条语句（含 begin / commit）。
type statementCounter struct {
	mu  sync.Mutex
	sql []string
}

func (c *statementCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.sql = append(c.sql, strings.Join(strings.Fields(data.SQL), " "))
	c.mu.Unlock()
	return ctx
}

func (c *statementCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *statementCounter) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.sql
	c.sql = nil
	return out
}

// TestSubscribePullPG18 端到端钉住订阅拉取的改造：
//   - 输出不变：正文与「不经缓存现查节点再渲染」逐字节相同，Subscription-Userinfo 同口径；
//   - 一次热拉取 2 个事务、9 条语句（原先 6 个事务、27 条，另加 6 次归还连接时的会话清理）；
//   - 节点按（套餐版本, 用户组）缓存：不同组互不串，节点变更信号到达即失效；
//   - 认证从不缓存：吊销立即生效；限流、日志与拉取次数同一事务，超限整笔回滚；
//   - 未认证失败按来源采样落库；认证通过后的数据缺失照旧记 error 并打 ERROR。
func TestSubscribePullPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const (
		tenant    = "7d5b0000-0000-4000-8000-000000000001"
		group     = "7d5b0000-0000-4000-8000-000000000011"
		vipUser   = "7d5b0000-0000-4000-8000-000000000021"
		plainUser = "7d5b0000-0000-4000-8000-000000000022"
		lostUser  = "7d5b0000-0000-4000-8000-000000000023"
		product   = "7d5b0000-0000-4000-8000-000000000031"
		plan      = "7d5b0000-0000-4000-8000-000000000032"
		planVer   = "7d5b0000-0000-4000-8000-000000000033"
		openPool  = "7d5b0000-0000-4000-8000-000000000041"
		vipPool   = "7d5b0000-0000-4000-8000-000000000042"
		server    = "7d5b0000-0000-4000-8000-000000000051"
		openNode  = "7d5b0000-0000-4000-8000-000000000061"
		vipNode   = "7d5b0000-0000-4000-8000-000000000062"
		vipSub    = "7d5b0000-0000-4000-8000-000000000071"
		plainSub  = "7d5b0000-0000-4000-8000-000000000072"
		lostSub   = "7d5b0000-0000-4000-8000-000000000073"
		prefix    = "c0ffee123456"
	)
	// 虚构令牌：低熵、互不相同、长度过 16 的门槛
	vipTok, plainTok, lostTok := strings.Repeat("vip0", 9), strings.Repeat("plain", 8), strings.Repeat("lost", 9)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed pull fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency,sub_path_prefix) VALUES($1,'pull-pg18','Pull PG18','USD',$2)`, tenant, prefix)
	must(`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'vip','VIP')`, tenant, group)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id) VALUES
	      ($2,$1,'vip@pull.invalid','VIP','active',$5),($3,$1,'plain@pull.invalid','Plain','active',NULL),
	      ($4,$1,'lost@pull.invalid','Lost','active',NULL)`, tenant, vipUser, plainUser, lostUser, group)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'pull-product','Pull Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'open','Open','active'),($3,$1,'vip','VIP','active')`, tenant, openPool, vipPool)
	must(`INSERT INTO node_pool_user_groups(tenant_id,pool_id,user_group_id) VALUES($1,$2,$3)`, tenant, vipPool, group)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'pull-plan','Pull Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, vipUser)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3),($1,$2,$4)`, tenant, planVer, openPool, vipPool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'pull-server','ready')`, tenant, server)
	// 同一台服务器上的节点端口各不相同（同机端口门禁的唯一索引，00122）
	for _, n := range []struct {
		id, name, host, pool string
		port                 int
	}{
		{openNode, "Open Line", "open.pull.invalid", openPool, 443},
		{vipNode, "VIP Line", "vip.pull.invalid", vipPool, 8443},
	} {
		must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,sort_order)
			  VALUES($2,$1,$3,$3,$4,'active','vless',$5,$7,$6,'active',1,now(),now(),0)`,
			tenant, n.id, n.name, n.pool, n.host, server, n.port)
	}
	for _, s := range []struct{ id, user, token string }{
		{vipSub, vipUser, vipTok}, {plainSub, plainUser, plainTok}, {lostSub, lostUser, lostTok},
	} {
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
				current_period_start,current_period_end)
			  VALUES($1,$2,$3,$4,$5,'active','USD',100,now()-interval '10 days',now()+interval '20 days')`,
			s.id, tenant, s.user, plan, planVer)
		must(`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope)
			  VALUES($1,$2,$3,$4,$5,'subscription')`, tenant, s.id, s.user, crypto.HashToken(s.token), s.token[:8])
	}
	// lostSub 故意没有流量配额行
	must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed,adjusted)
	      VALUES($1,$2,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',1000,1000,400,50),
	            ($1,$3,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',1000,1000,10,0)`, tenant, vipSub, plainSub)
	// 流量包按份挂（购买模型统一）：挂在 vipSub 上
	must(`INSERT INTO traffic_pack_grants(tenant_id,user_id,subscription_id,source,source_id,granted_bytes,consumed_bytes)
	      VALUES($1,$2,$3,'migration',gen_random_uuid(),100,40)`, tenant, vipUser, vipSub)

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	hub := realtime.NewHub(nil, log)
	defer hub.Close()
	newRouter := func(svc *subscription.Service) http.Handler {
		svc.AttachRealtime(hub)
		h := &handlers{d: Deps{Log: log, Subscription: svc}}
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				c := httpx.WithTenantID(req.Context(), tenant)
				c = httpx.WithRequestID(c, "req-pull-pg18")
				next.ServeHTTP(w, req.WithContext(c))
			})
		})
		r.Get("/{prefix}/{token}", h.subscribe)
		return r
	}
	svc := subscription.New(app, []byte("pull-pg18-salt"), nil)
	router := newRouter(svc)
	pull := func(h http.Handler, path, ip string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		req.Header.Set("User-Agent", "clash-verge/v2.0")
		req.Header.Set("X-Real-IP", ip)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	render := func(user string) string {
		t.Helper()
		nodes, err := svc.ListNodes(ctx, tenant, &subscription.Credential{UserID: user, PlanVersionID: planVer})
		if err != nil {
			t.Fatal(err)
		}
		var proxy string
		if err := admin.QueryRow(ctx, `SELECT proxy_uuid::text FROM subscriptions s JOIN subscription_credentials c ON c.subscription_id=s.id
			WHERE s.user_id=$1`, user).Scan(&proxy); err != nil {
			t.Fatal(err)
		}
		body, _, _ := subscription.Render(subscription.FormatClash, nodes, proxy)
		return string(body)
	}

	// --- 成功拉取：输出与现查渲染逐字节相同，用量头同口径，拉取次数与日志同一事务 ---
	w := pull(router, "/"+prefix+"/"+vipTok+".yaml", "198.51.100.1")
	var expire int64
	if err := admin.QueryRow(ctx, `SELECT floor(extract(epoch FROM current_period_end))::bigint FROM subscriptions WHERE id=$1`, vipSub).Scan(&expire); err != nil {
		t.Fatal(err)
	}
	wantInfo := fmt.Sprintf("upload=0; download=400; total=1110; expire=%d", expire)
	if w.Code != http.StatusOK || w.Header().Get("Subscription-Userinfo") != wantInfo ||
		w.Body.String() != render(vipUser) || !strings.Contains(w.Body.String(), "VIP Line") {
		t.Fatalf("vip pull: status=%d userinfo=%q body=%s", w.Code, w.Header().Get("Subscription-Userinfo"), w.Body)
	}
	// 没起备注名：配置名是「站点名 · 套餐名」（租户没有主题，站点名是默认的 Pandora）
	if got, want := w.Header().Get("Content-Disposition"),
		`attachment; filename="Pandora Pull Plan"; filename*=UTF-8''Pandora%20%C2%B7%20Pull%20Plan`; got != want {
		t.Fatalf("vip pull Content-Disposition=%q want %q", got, want)
	}
	if n := count(`SELECT fetch_count::int FROM subscription_credentials WHERE subscription_id=$1`, vipSub); n != 1 {
		t.Fatalf("fetch_count=%d after one pull, want 1", n)
	}
	if n := count(`SELECT count(*)::int FROM subscription_fetch_log WHERE subscription_id=$1 AND result='ok' AND node_count=2`, vipSub); n != 1 {
		t.Fatalf("ok fetch logs=%d, want 1", n)
	}
	// 同套餐版本、默认组的用户：拿不到 VIP 池的节点，缓存不能把 VIP 组的答案给他
	w = pull(router, "/"+prefix+"/"+plainTok, "198.51.100.2")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "VIP Line") || w.Body.String() != render(plainUser) {
		t.Fatalf("plain pull leaked another group's nodes: status=%d body=%s", w.Code, w.Body)
	}

	// --- 语句数：同一份服务热起来之后，一次拉取 2 个事务、9 条语句 ---
	counter := &statementCounter{}
	cfg, err := pgxpool.ParseConfig(os.Getenv("AEGIS_PUBLIC_API_PG18_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = counter
	counted, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer counted.Close()
	countedRouter := newRouter(subscription.New(&db.Pool{Pool: counted}, []byte("pull-pg18-salt"), nil))
	if w := pull(countedRouter, "/"+prefix+"/"+vipTok, "198.51.100.1"); w.Code != http.StatusOK {
		t.Fatalf("cold counted pull status=%d", w.Code)
	}
	cold := counter.take()
	if w := pull(countedRouter, "/"+prefix+"/"+vipTok, "198.51.100.1"); w.Code != http.StatusOK {
		t.Fatalf("warm counted pull status=%d", w.Code)
	}
	warm := counter.take()
	begins := 0
	for _, s := range warm {
		if strings.HasPrefix(strings.ToLower(s), "begin") {
			begins++
		}
	}
	t.Logf("pull statements cold=%d warm=%d warm_tx=%d\nwarm:\n  %s", len(cold), len(warm), begins, strings.Join(warm, "\n  "))
	if begins != 2 || len(warm) > 9 {
		t.Fatalf("warm pull ran %d transactions / %d statements, want 2 / <=9:\n%s", begins, len(warm), strings.Join(warm, "\n"))
	}

	// --- 节点缓存：节点改名后仍是缓存里的旧答案，节点变更信号到达即失效 ---
	must(`UPDATE nodes SET display_name='Renamed Line' WHERE id=$1`, openNode)
	if w := pull(router, "/"+prefix+"/"+vipTok, "198.51.100.1"); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Renamed Line") {
		t.Fatalf("node cache did not hold within TTL: status=%d", w.Code)
	}
	hub.Publish(ctx, realtime.ChannelNodeAll(tenant), realtime.TopicNodeConfigChanged, map[string]any{"node_id": openNode})
	deadline := time.Now().Add(3 * time.Second)
	for {
		w := pull(router, "/"+prefix+"/"+vipTok, "198.51.100.1")
		if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "Renamed Line") && w.Body.String() == render(vipUser) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node change signal did not invalidate the cache: status=%d body=%s", w.Code, w.Body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// --- 未认证失败：按来源采样，同一来源一个窗口只落一行 ---
	notFound := func() int {
		return count(`SELECT count(*)::int FROM subscription_fetch_log WHERE tenant_id=$1 AND result='not_found'`, tenant)
	}
	base := notFound()
	for i, c := range []struct{ path, ip string }{
		{"/" + prefix + "/no-such-token-0123456789abcdef", "198.51.100.7"},
		{"/" + prefix + "/another-wrong-token-0123456789ab", "198.51.100.7"},
		{"/ffffffffffff/" + vipTok, "198.51.100.7"},
		{"/" + prefix + "/no-such-token-0123456789abcdef", "198.51.100.8"},
	} {
		if w := pull(router, c.path, c.ip); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Not Found") {
			t.Fatalf("unauthenticated pull %d: status=%d", i, w.Code)
		}
	}
	if got := notFound() - base; got != 2 {
		t.Fatalf("not_found rows written=%d for 4 failures from 2 sources, want 2", got)
	}

	// --- 限流：超限回 429，日志记 rate_limited，拉取次数不加（整笔回滚） ---
	ok := count(`SELECT count(*)::int FROM subscription_fetch_log WHERE subscription_id=$1 AND result='ok' AND fetched_at > now() - interval '1 hour'`, vipSub)
	must(`UPDATE subscription_credentials SET rate_limit_per_hour=$2 WHERE subscription_id=$1`, vipSub, ok)
	fetches := count(`SELECT fetch_count::int FROM subscription_credentials WHERE subscription_id=$1`, vipSub)
	w = pull(router, "/"+prefix+"/"+vipTok, "198.51.100.1")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "3600" {
		t.Fatalf("over the hourly limit: status=%d", w.Code)
	}
	if count(`SELECT fetch_count::int FROM subscription_credentials WHERE subscription_id=$1`, vipSub) != fetches ||
		count(`SELECT count(*)::int FROM subscription_fetch_log WHERE subscription_id=$1 AND result='rate_limited'`, vipSub) != 1 {
		t.Fatal("rate-limited pull must roll back the fetch count and log rate_limited once")
	}

	// --- 认证从不缓存：吊销后下一次拉取就是伪装页 ---
	must(`UPDATE subscription_credentials SET status='revoked', revoked_at=now() WHERE subscription_id=$1`, plainSub)
	if w := pull(router, "/"+prefix+"/"+plainTok, "198.51.100.2"); w.Code != http.StatusNotFound {
		t.Fatalf("revoked credential still served: status=%d", w.Code)
	}

	// --- 认证通过但没有流量配额行：照旧回伪装页、记一条带凭据的 error，并打 ERROR ---
	logs.Reset()
	if w := pull(router, "/"+prefix+"/"+lostTok, "198.51.100.3"); w.Code != http.StatusNotFound {
		t.Fatalf("subscription without quota row: status=%d", w.Code)
	}
	if count(`SELECT count(*)::int FROM subscription_fetch_log WHERE subscription_id=$1 AND result='error' AND credential_id IS NOT NULL`, lostSub) != 1 ||
		!strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), `"request_id":"req-pull-pg18"`) ||
		strings.Contains(logs.String(), lostTok) {
		t.Fatalf("missing quota row must be logged as an error without the token: %s", logs.String())
	}
	t.Logf("subscribe_pull_pg18 output=unchanged warm_tx=%d warm_statements=%d cold_statements=%d group_cache=isolated invalidation=signal not_found_sampled=2/4", begins, len(warm), len(cold))
}
