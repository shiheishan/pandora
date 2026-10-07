package nodefabric

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 这些是 TestEffectiveReleasePG18 的子测试（effective 域的 -run 过滤只列了顶层函数名），
// 跟它共用一次性库与夹具。

// 同一 nonce 第二次认领必须被拒；后台清理只删已过期的行，删完以后仍在窗口内的
// nonce 照样不能重放；清理按租户隔离。
func testNonceReplayAndPurgePG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, svc *Service,
	tenantA, nodeA, tenantB, nodeB uuid.UUID) {
	live := []byte("live-nonce-00001")
	fingerprint := sha256.Sum256([]byte("live-request"))
	now := time.Now().UTC()
	if err := svc.ClaimSignedRequest(ctx, tenantA.String(), nodeA.String(), live, fingerprint[:], now); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := svc.ClaimSignedRequest(ctx, tenantA.String(), nodeA.String(), live, fingerprint[:], now); err == nil {
		t.Fatal("same nonce accepted twice")
	}

	expiredA, expiredB := []byte("old-nonce-a00001"), []byte("old-nonce-b00001")
	for _, row := range []struct {
		tenant, node uuid.UUID
		nonce        []byte
	}{{tenantA, nodeA, expiredA}, {tenantB, nodeB, expiredB}} {
		if _, err := admin.Exec(ctx, `INSERT INTO node_request_nonces
			(tenant_id,node_id,nonce,request_fingerprint,request_ts,expires_at)
			VALUES ($1,$2,$3,$4,now()-interval '30 minutes',now()-interval '1 minute')`,
			row.tenant, row.node, row.nonce, fingerprint[:]); err != nil {
			t.Fatal(err)
		}
	}
	purged, err := svc.PurgeExpiredNonces(ctx, tenantA.String())
	if err != nil || purged < 1 {
		t.Fatalf("purge removed %d rows, err=%v", purged, err)
	}
	count := func(tenant uuid.UUID, nonce []byte) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_request_nonces WHERE tenant_id=$1 AND nonce=$2`,
			tenant, nonce).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(tenantA, expiredA) != 0 {
		t.Fatal("expired nonce survived the purge")
	}
	if count(tenantA, live) != 1 {
		t.Fatal("purge removed a nonce that is still inside its replay window")
	}
	if count(tenantB, expiredB) != 1 {
		t.Fatal("tenant A purge touched tenant B rows")
	}
	if err := svc.ClaimSignedRequest(ctx, tenantA.String(), nodeA.String(), live, fingerprint[:], now); err == nil {
		t.Fatal("live nonce became replayable after purge")
	}
}

// 节点带着当前版来问：只读确认、回「没变化」；全量路径重复拉取同一代际不再写节点行。
func testEffectiveUnchangedPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, svc *Service) {
	tenant, node, release := uuid.New(), uuid.New(), uuid.New()
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,$3)`,
		tenant, "effective-fast-"+uuid.NewString(), "Effective fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO nodes
		(id,tenant_id,name,status,serving_status,node_type,server_host,server_port,kernel,protocol_config)
		VALUES($1,$2,'fast','active','active','vless','127.0.0.1',443,'auto','{}')`, node, tenant); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"protocol":"vless","server_port":443}`)
	manifest := []byte(`{"layers":[]}`)
	contentHash, manifestHash := sha256.Sum256(payload), sha256.Sum256(manifest)
	if _, err := admin.Exec(ctx, `INSERT INTO node_effective_config_releases
		(id,tenant_id,node_id,generation,payload,content_hash,source_manifest,source_manifest_hash,key_id)
		VALUES($1,$2,$3,1,$4,$5,$6,$7,$8)`, release, tenant, node, payload, contentHash[:],
		manifest, manifestHash[:], svc.signer.KeyID()); err != nil {
		t.Fatal(err)
	}
	unchanged := func(id string, generation uint64) bool {
		t.Helper()
		ok, err := svc.EffectiveConfigUnchanged(ctx, tenant.String(), node.String(), id, generation)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if unchanged(release.String(), 1) {
		t.Fatal("release was reported unchanged before it was ever delivered (desired_* unset)")
	}
	xmin := func() (v string) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT xmin::text FROM nodes WHERE id=$1`, node).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first, err := svc.FetchEffectiveConfig(ctx, tenant.String(), node.String())
	if err != nil || first.ReleaseID != release.String() {
		t.Fatalf("first delivery = %+v, %v", first, err)
	}
	afterFirst := xmin()
	second, err := svc.FetchEffectiveConfig(ctx, tenant.String(), node.String())
	if err != nil || second.ReleaseID != release.String() || string(second.Payload) != string(first.Payload) {
		t.Fatalf("refetch = %+v, %v", second, err)
	}
	if xmin() != afterFirst {
		t.Fatal("refetching an unchanged release rewrote the node row")
	}
	if !unchanged(release.String(), 1) {
		t.Fatal("current release not recognised as unchanged")
	}
	if unchanged(uuid.NewString(), 1) || unchanged(release.String(), 2) {
		t.Fatal("a different release identity was reported unchanged")
	}
	if _, err := admin.Exec(ctx, `UPDATE nodes SET config_source_generation=2 WHERE id=$1`, node); err != nil {
		t.Fatal(err)
	}
	if unchanged(release.String(), 1) {
		t.Fatal("pending generation bump was short-circuited")
	}
}

// 在线 IP 上报一条语句落库：重复的（uid, IP）与写法不同的同一 uid 合并，查不到订阅的
// uid 跳过；再报一次只刷新时间。同时让两种用户集查询（带节点门槛的直查、缓存用的
// 池级查询）与批量取在线节点的查询在 PG18 上各跑一遍。
func testReportAliveBatchPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool, svc *Service) {
	tenant, node, user, sub := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,$3)`,
		tenant, "alive-batch-"+uuid.NewString(), "Alive batch"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO nodes(id,tenant_id,name,status) VALUES($1,$2,'alive','active')`, node, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, $3, 'Alive Batch', 'active')`, user, tenant, "alive-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	// 订阅只是 node_uid 的挂载点：关掉触发器（含外键）插一条，套餐与版本与本测试无关。
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end)
		VALUES ($1, $2, $3, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days')`,
		sub, tenant, user); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err := admin.QueryRow(ctx, `SELECT node_uid FROM subscriptions WHERE id=$1`, sub).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	raw := []byte(fmt.Sprintf(`{"%d":["198.51.100.7","198.51.100.7","198.51.100.8"],"0%d":["198.51.100.7"],"-5":["198.51.100.9"],"x":["198.51.100.10"]}`, uid, uid))
	n := &ServingNode{ID: node.String()}
	for round := 0; round < 2; round++ {
		got, err := svc.ReportAlive(ctx, tenant.String(), n, raw)
		if err != nil || got != 2 {
			t.Fatalf("round %d: ReportAlive = %d, %v; want 2 rows", round, got, err)
		}
	}
	var rows int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_alive_ips WHERE tenant_id=$1 AND node_id=$2 AND subscription_id=$3`,
		tenant, node, sub).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("node_alive_ips rows = %d, %v; want 2", rows, err)
	}

	pool := uuid.NewString()
	direct, err := svc.ListNodeUsers(ctx, tenant.String(), &ServingNode{ID: node.String(), PoolID: &pool})
	if err != nil || len(direct) != 0 {
		t.Fatalf("gated node user query = %v, %v", direct, err)
	}
	cached := NewService(app, svc.signer)
	cached.EnableNodeCaches()
	epoch := deliveryEpochPG18(t, ctx, admin)
	pooled, version, err := cached.NodeUserSet(ctx, tenant.String(),
		&ServingNode{ID: node.String(), PoolID: &pool, deliveryEpoch: epoch, epochKnown: true})
	if err != nil || len(pooled) != 0 || version != UserSetVersion(nil) {
		t.Fatalf("pool-level node user query = %v %q, %v", pooled, version, err)
	}
	if set, ok := cached.caches.users.peek(usersCacheKey(tenant.String(), pool)); !ok || set.epoch < epoch {
		t.Fatalf("cached user set epoch = %+v (ok=%v), want >= %d", set, ok, epoch)
	}
	serving, err := svc.loadServingNodesForPush(ctx, tenant.String(), []string{node.String(), uuid.NewString()})
	if err != nil || len(serving) != 0 {
		t.Fatalf("batched serving-node query = %v, %v (fixture node has no server, so none qualify)", serving, err)
	}

	testDeliveryEpochTriggersPG18(t, ctx, admin, tenant, node, sub)
}

func deliveryEpochPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool) (epoch int64) {
	t.Helper()
	if err := admin.QueryRow(ctx, `SELECT last_value FROM node_delivery_epoch`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// 下发纪元（00101）只在下发输入「可能变了」时推进：配额只在用尽与否翻转时、节点只在
// 状态列被写时；扣量与心跳那种高频写不碰它。推进发生在提交时。
func testDeliveryEpochTriggersPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant, node, sub uuid.UUID) {
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
	}
	expect := func(what string, bumped bool, change func()) {
		t.Helper()
		before := deliveryEpochPG18(t, ctx, admin)
		change()
		after := deliveryEpochPG18(t, ctx, admin)
		if (after > before) != bumped {
			t.Fatalf("%s: delivery epoch %d -> %d, want advanced=%v", what, before, after, bumped)
		}
	}
	expect("new quota balance", true, func() {
		exec(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period, period_start, period_end, granted, limit_value)
			VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 100, 100)`, tenant, sub)
	})
	expect("consumption that leaves quota", false, func() {
		exec(`UPDATE quota_balances SET consumed = 40 WHERE subscription_id=$1 AND metric='traffic.bytes'`, sub)
	})
	expect("consumption that exhausts quota", true, func() {
		exec(`UPDATE quota_balances SET consumed = 100 WHERE subscription_id=$1 AND metric='traffic.bytes'`, sub)
	})
	expect("consumption past an exhausted quota", false, func() {
		exec(`UPDATE quota_balances SET consumed = 150 WHERE subscription_id=$1 AND metric='traffic.bytes'`, sub)
	})
	expect("quota reset", true, func() {
		exec(`UPDATE quota_balances SET consumed = 0 WHERE subscription_id=$1 AND metric='traffic.bytes'`, sub)
	})
	expect("subscription change", true, func() {
		exec(`UPDATE subscriptions SET current_period_end = now() + interval '31 days' WHERE id=$1`, sub)
	})
	expect("heartbeat-style node write", false, func() {
		exec(`UPDATE nodes SET last_heartbeat_at = now(), health_score = 90 WHERE id=$1`, node)
	})
	expect("node serving status change", true, func() {
		exec(`UPDATE nodes SET serving_status = 'disabled' WHERE id=$1`, node)
	})
	expect("rolled back change", false, func() {
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET current_period_end = now() + interval '32 days' WHERE id=$1`, sub); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	})
}
