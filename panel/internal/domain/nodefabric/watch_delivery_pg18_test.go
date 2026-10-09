package nodefabric

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// watchedDeliveryScenario 证明纪元监听健康时（名单 pinned、不按 TTL 重算），下发输入的
// 变化照样及时生效（审查 #7）：
//
//   - 封禁账号：'d' 通知送达后下一次取名单就没有这个人；
//   - 把订阅的 current_period_end 拨到过去：同上（订阅行的写同样发 'd'）；
//   - 名单里最早的订阅到期：没有任何写，到点（hardAt）同步重算，不回旧名单（审查 #2）。
//
// 由 TestTrafficChargePG18 调用（traffic_charge 域的库，已应用 configure-app-role.sql）。
func watchedDeliveryScenario(t *testing.T, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const (
		tenantID = "75cc0000-0000-7000-8000-000000000001"
		userA    = "75cc0000-0000-7000-8000-000000000011"
		userB    = "75cc0000-0000-7000-8000-000000000012"
		userC    = "75cc0000-0000-7000-8000-000000000013"
		subA     = "75cc0000-0000-7000-8000-000000000021"
		subB     = "75cc0000-0000-7000-8000-000000000022"
		subC     = "75cc0000-0000-7000-8000-000000000023"
		planVer  = "75cc0000-0000-7000-8000-000000000031"
		poolID   = "75cc0000-0000-7000-8000-000000000041"
		uidA     = 7591001
		uidB     = 7591002
		uidC     = 7591003
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'node-watch-pg18', 'Node Watch PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES
		($1, $4, 'node-watch-a@example.test', 'A', 'active'),
		($2, $4, 'node-watch-b@example.test', 'B', 'active'),
		($3, $4, 'node-watch-c@example.test', 'C', 'active')`, userA, userB, userC, tenantID)
	must(`INSERT INTO node_pools (id, tenant_id, code, name, status) VALUES ($1, $2, 'node-watch', 'Node Watch', 'active')`,
		poolID, tenantID)
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO plan_versions (id, tenant_id, plan_id, version) VALUES ($1, $2, gen_random_uuid(), 1)`,
		planVer, tenantID)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $4, $5, gen_random_uuid(), $8, 'active', 'CNY', 0, now() + interval '30 days', $9),
		       ($2, $4, $6, gen_random_uuid(), $8, 'active', 'CNY', 0, now() + interval '30 days', $10),
		       ($3, $4, $7, gen_random_uuid(), $8, 'active', 'CNY', 0, now() + interval '30 days', $11)`,
		subA, subB, subC, tenantID, userA, userB, userC, planVer, int64(uidA), int64(uidB), int64(uidC))
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id) VALUES ($1, $2, $3)`,
		tenantID, planVer, poolID)

	svc := NewService(app, nil)
	svc.EnableNodeCaches()
	watchCtx, stopWatch := context.WithCancel(ctx)
	wait := svc.StartEpochWatch(watchCtx, slog.Default())
	defer func() {
		stopWatch()
		wait()
	}()
	for deadline := time.Now().Add(10 * time.Second); !svc.EpochWatchHealthy(); {
		if time.Now().After(deadline) {
			t.Fatal("epoch watch never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	pool := poolID
	served := func() map[int64]bool {
		t.Helper()
		// 与 AuthenticateNode 给出的节点视图一样：带请求开始时的监听戳
		n := &ServingNode{ID: "75cc0000-0000-7000-8000-000000000061", PoolID: &pool, epochKnown: true,
			watch: svc.watchStamp()}
		users, _, err := svc.NodeUserSet(ctx, tenantID, n)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]bool{}
		for _, u := range users {
			out[u.ID] = true
		}
		return out
	}
	within := func(step string, cond func(map[int64]bool) bool) time.Duration {
		t.Helper()
		start := time.Now()
		for {
			if got := served(); cond(got) {
				return time.Since(start)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("%s: not reflected within 2s while the watch is healthy; served %v", step, served())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := served(); !got[uidA] || !got[uidB] || !got[uidC] {
		t.Fatalf("initial list: %v", got)
	}
	if !svc.EpochWatchHealthy() {
		t.Fatal("watch unhealthy: the scenario would only prove the database path")
	}

	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE users SET status = 'banned' WHERE tenant_id = $1 AND id = $2`, tenantID, userA)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	took := within("ban", func(m map[int64]bool) bool { return !m[uidA] && m[uidB] })
	t.Logf("marker=node_watch_pg18_ban_dropped_ok latency=%v", took.Round(time.Millisecond))

	must(`UPDATE subscriptions SET current_period_end = now() - interval '1 second' WHERE id = $1`, subB)
	took = within("period end moved to the past", func(m map[int64]bool) bool { return !m[uidB] && m[uidC] })
	t.Logf("marker=node_watch_pg18_expired_dropped_ok latency=%v", took.Round(time.Millisecond))

	// 到期没有写：名单里最早的到期一到就同步重算（pinned 不越过 hardAt，也不走 staleGrace）
	must(`UPDATE subscriptions SET current_period_end = now() + interval '2 seconds' WHERE id = $1`, subC)
	within("short expiry loaded", func(m map[int64]bool) bool { return m[uidC] })
	var end time.Time
	if err := admin.QueryRow(ctx, `SELECT current_period_end FROM subscriptions WHERE id = $1`, subC).Scan(&end); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(end) + nextExpiryFloor + 200*time.Millisecond)
	if got := served(); got[uidC] {
		t.Fatalf("subscription served %v after its period end with no write in between", time.Since(end))
	}
	if !svc.EpochWatchHealthy() {
		t.Fatal("watch went unhealthy during the scenario")
	}
	t.Log("marker=node_watch_pg18_hard_expiry_ok")
}
