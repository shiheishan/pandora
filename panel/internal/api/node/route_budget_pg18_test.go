package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/middleware"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/tools/routebudget"
)

// pushWALBudget 是一份 push（3 个有效用户、带上报编号）写进 WAL 的字节上限，钉成棘轮。
// 口径是 5 次里的最小值：排除检查点后的整页镜像与旁路写入，只剩这份 push 自己的行。
// 现状实测 2488 字节（10-09，往返 8 次，见 routes.txt 的 push 行），留约 8% 余量。
// 生产均值 7.9KB 含整页镜像，与这里不是一个口径：N2（push 合到 ≤2 次往返）的「WAL ≤6KB」
// 按同比例折到这里约 1.9KB，落地后把这里与 push 行一起改小。
const pushWALBudget = 2688

// TestNodeRouteBudgetPG18 走节点网关的真实路由（与 aegis-node 同样装配：Valkey nonce、
// 节点缓存、纪元监听、心跳合并），逐条量稳态的库语句往返与 Valkey 往返，与
// tools/routebudget/routes.txt 的预算比对；另量一份 push 的 WAL 字节。
// 用 effective 域的一次性库（run-pg18-gates.sh），默认租户里自己接入一个节点。
func TestNodeRouteBudgetPG18(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_FIXTURE")) != "disposable-v1" {
		t.Skip("AEGIS_EFFECTIVE_PG18_FIXTURE is not disposable-v1")
	}
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_ADMIN_DSN"))
	expectedDB := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_DATABASE"))
	if appDSN == "" || adminDSN == "" || expectedDB == "" {
		t.Fatal("effective PG18 DSNs and expected database are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var dbName string
	var serverVersion int
	if err := admin.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int`).
		Scan(&dbName, &serverVersion); err != nil {
		t.Fatal(err)
	}
	if dbName != expectedDB || serverVersion < 180000 || serverVersion >= 190000 {
		t.Fatalf("refusing unexpected PG target: database=%q version=%d", dbName, serverVersion)
	}
	app, err := db.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	signer, err := platformcrypto.NewSigner(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}

	// 与 cmd/aegis-node 相同的装配（不调 PrimeNonceFallback：稳态下 PG 里没有回落留下的 nonce）
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := nodefabric.NewService(app, signer)
	svc.SetReleaseBinding(nodefabric.ReleaseBinding{})
	svc.SetNonceStore(valkeyNonces{rdb: routebudget.Valkey(t)}, quiet)
	hub := nodefabric.NewStreamHub()
	svc.AttachStream(hub)
	svc.EnableNodeCaches()
	watchCtx, stopWatch := context.WithCancel(ctx)
	waitWatch := svc.StartEpochWatch(watchCtx, quiet)
	waitBeats := svc.StartHeartbeatCoalescer(watchCtx, quiet)
	defer func() {
		stopWatch()
		waitWatch()
		waitBeats()
	}()
	for deadline := time.Now().Add(10 * time.Second); !svc.EpochWatchHealthy(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("epoch watch never became healthy against PG18")
		}
	}
	access := &routebudget.AccessLog{}
	server := httptest.NewServer(NewRouter(Deps{Pool: app, Log: access.Logger(), Node: svc, NodeStream: hub}))
	defer server.Close()

	const tenantID = middleware.DefaultTenantID
	node, privateKey, runtimeToken := enrollActiveNodePG18(t, ctx, admin, server.URL, signer, "route-budget", 14444)
	uids := seedNodeDeliveryPG18(t, ctx, admin, tenantID, node.NodeID)

	// 夹具改了节点池，生效配置随之换代：取当前版并回报已应用，之后的拉取都是「未变」
	var cfg nodefabric.SignedConfig
	doSignedJSON(t, privateKey, node.NodeID, http.MethodGet, server.URL+"/v1/nodes/effective-config", nil, http.StatusOK, &cfg)
	for _, phase := range []string{"switched", "health_passed"} {
		doSignedJSON(t, privateKey, node.NodeID, http.MethodPost, server.URL+"/v1/nodes/config/report",
			mustJSON(t, map[string]any{
				"report_id":  uuid.NewSHA1(uuid.MustParse(cfg.ReleaseID), []byte(phase)).String(),
				"release_id": cfg.ReleaseID, "generation": cfg.Generation,
				"content_sha256": cfg.ContentSHA256, "phase": phase, "detail": "",
			}), http.StatusOK, nil)
	}
	applied := cfg.ReleaseID + "/" + strconv.FormatUint(cfg.Generation, 10)
	beat := mustJSON(t, map[string]any{"agent_version": "rb-test", "runtime_version": "native-rb",
		"config_signing_key_id":        signer.KeyID(),
		"applied_effective_release_id": cfg.ReleaseID, "applied_effective_generation": cfg.Generation,
		"applied_effective_content_sha256": cfg.ContentSHA256})

	send := func(req *http.Request) int {
		t.Helper()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	signed := func(method, path string, body []byte, header map[string]string) func(string) int {
		return func(id string) int {
			req := newSignedRequest(t, privateKey, node.NodeID, method, server.URL+path, body,
				freshNonce(t), time.Now().UTC().Format(time.RFC3339))
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			for k, v := range header {
				req.Header.Set(k, v)
			}
			req.Header.Set("X-Request-ID", id)
			return send(req)
		}
	}
	var etag string
	uni := func(method, path string, body func() string, header func() map[string]string) func(string) int {
		return func(id string) int {
			var rd io.Reader
			if body != nil {
				rd = strings.NewReader(body())
			}
			req, err := http.NewRequest(method, fmt.Sprintf("%s/api/v1/server/UniProxy/%s?node_id=%s&node_type=shadowsocks",
				server.URL, path, node.NodeID), rd)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+runtimeToken)
			req.Header.Set("X-Request-ID", id)
			if header != nil {
				for k, v := range header() {
					req.Header.Set(k, v)
				}
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if path == "user" && resp.Header.Get("ETag") != "" {
				etag = resp.Header.Get("ETag")
			}
			return resp.StatusCode
		}
	}
	pushBody := func() string {
		// 每份用不同的流量值，免得撞上老节点的 10 秒内容去重；编号每份都新
		parts := make([]string, len(uids))
		for i, uid := range uids {
			parts[i] = fmt.Sprintf(`"%d":[%d,%d]`, uid, 1000+time.Now().Nanosecond()%1000, 2048)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	pushHeader := func() map[string]string {
		return map[string]string{nodefabric.TrafficReportIDHeader: "rb-" + uuid.NewString()}
	}
	aliveBody := func() string {
		parts := make([]string, len(uids))
		for i, uid := range uids {
			parts[i] = fmt.Sprintf(`"%d":["198.18.0.%d"]`, uid, i+1)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	ifNoneMatch := func() map[string]string { return map[string]string{"If-None-Match": etag} }
	statusBody := func() string {
		return `{"cpu":1.5,"mem":{"total":2048,"used":1024},"swap":{"total":0,"used":0},"disk":{"total":4096,"used":1024}}`
	}

	budget := routebudget.NewBudget(t, "node", app)
	ok200, ok204, ok304 := []int{http.StatusOK}, []int{http.StatusNoContent}, []int{http.StatusNotModified}
	budget.Measure(access, "GET /healthz", 1, ok200, func(id string) int {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/healthz", nil)
		req.Header.Set("X-Request-ID", id)
		return send(req)
	})
	budget.Measure(access, "POST /v1/nodes/heartbeat", 2, ok200, signed(http.MethodPost, "/v1/nodes/heartbeat", beat, nil))
	budget.Measure(access, "GET /v1/nodes/effective-config", 2, ok204, signed(http.MethodGet, "/v1/nodes/effective-config", nil,
		map[string]string{nodefabric.AppliedEffectiveReleaseHeader: applied}))
	// 例行换钥检查：带上手里的配置签名钥，没有换钥时 204
	budget.Measure(access, "GET /v1/nodes/config-signing-key", 2, ok204, signed(http.MethodGet, "/v1/nodes/config-signing-key", nil,
		map[string]string{"X-Config-Key-Id": signer.KeyID()}))
	if code := uni(http.MethodGet, "user", nil, nil)(uuid.NewString()); code != http.StatusOK || etag == "" {
		t.Fatalf("first user list = %d etag=%q", code, etag)
	}
	budget.Measure(access, "GET /api/v1/server/UniProxy/user", 1, ok304, uni(http.MethodGet, "user", nil, ifNoneMatch))
	budget.Measure(access, "GET /api/v1/server/UniProxy/config", 2, ok200, uni(http.MethodGet, "config", nil, nil))
	budget.Measure(access, "POST /api/v1/server/UniProxy/push", 2, ok200, uni(http.MethodPost, "push", pushBody, pushHeader))
	budget.Measure(access, "POST /api/v1/server/UniProxy/alive", 2, ok200, uni(http.MethodPost, "alive", aliveBody, nil))
	budget.Measure(access, "POST /api/v1/server/UniProxy/status", 2, ok200, uni(http.MethodPost, "status", statusBody, nil))
	budget.Verify()

	t.Run("push WAL", func(t *testing.T) {
		push := uni(http.MethodPost, "push", pushBody, pushHeader)
		lsn := func() string {
			t.Helper()
			var at string
			if err := admin.QueryRow(ctx, `SELECT pg_current_wal_insert_lsn()::text`).Scan(&at); err != nil {
				t.Fatal(err)
			}
			return at
		}
		// 别的写入（心跳合并、自动清理、检查点后的整页镜像）只会让单次读数变大：取 5 次最小值
		best := int64(-1)
		for i := 0; i < 5; i++ {
			before := lsn()
			if code := push(uuid.NewString()); code != http.StatusOK {
				t.Fatalf("push = %d", code)
			}
			var n int64
			if err := admin.QueryRow(ctx, `SELECT pg_wal_lsn_diff(pg_current_wal_insert_lsn(), $1::pg_lsn)::bigint`, before).
				Scan(&n); err != nil {
				t.Fatal(err)
			}
			if best < 0 || n < best {
				best = n
			}
		}
		t.Logf("一份 push（%d 个有效用户）WAL %d 字节（预算 %d）", len(uids), best, pushWALBudget)
		if best > pushWALBudget {
			t.Fatalf("one push writes %d WAL bytes, budget %d", best, pushWALBudget)
		}
	})
}

// seedNodeDeliveryPG18 让节点服务 3 份有效订阅（每份带本周期流量配额），返回它们的 node_uid：
// 节点进一个新池，套餐版本绑定这个池。push 只给名单里的用户计费，没有这份名单就量不到扣量。
func seedNodeDeliveryPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenantID, nodeID string) []int64 {
	t.Helper()
	ids := map[string]string{}
	for _, k := range []string{"pool", "product", "plan", "version", "u1", "u2", "u3", "s1", "s2", "s3"} {
		ids[k] = uuid.NewString()
	}
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed node delivery: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'rb-pool','Route Budget','active')`, tenantID, ids["pool"])
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'rb-product','Route Budget','active')`, tenantID, ids["product"])
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'rb-plan','Route Budget','draft')`,
		tenantID, ids["product"], ids["plan"])
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($3,$1,$2,1)`, tenantID, ids["plan"], ids["version"])
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenantID, ids["version"], ids["pool"])
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, ids["version"])
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenantID, ids["version"], ids["plan"])
	for i, k := range []string{"1", "2", "3"} {
		must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,$3,'RB','active')`,
			tenantID, ids["u"+k], fmt.Sprintf("rb-%d-%s@route-budget.invalid", i, ids["u"+k][:8]))
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
				current_period_start,current_period_end)
			  VALUES($1,$2,$3,$4,$5,'active','CNY',100,now()-interval '1 day',now()+interval '29 days')`,
			ids["s"+k], tenantID, ids["u"+k], ids["plan"], ids["version"])
		must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value)
			  VALUES($1,$2,'traffic.bytes','cycle',now()-interval '1 day',now()+interval '29 days',$3,$3)`,
			tenantID, ids["s"+k], int64(1)<<40)
	}
	must(`UPDATE nodes SET pool_id=$3 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID, ids["pool"])
	rows, err := admin.Query(ctx, `SELECT node_uid FROM subscriptions WHERE id = ANY($1::uuid[]) ORDER BY node_uid`,
		[]string{ids["s1"], ids["s2"], ids["s3"]})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var uids []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		uids = append(uids, uid)
	}
	if err := rows.Err(); err != nil || len(uids) != 3 {
		t.Fatalf("node uids = %v err=%v", uids, err)
	}
	return uids
}

// valkeyNonces 与 cmd/aegis-node 的 valkeyNonceStore 同样做法：SET NX PX 认领 nonce。
type valkeyNonces struct{ rdb *redis.Client }

func (v valkeyNonces) ClaimNonce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return v.rdb.SetNX(ctx, key, "1", ttl).Result()
}
