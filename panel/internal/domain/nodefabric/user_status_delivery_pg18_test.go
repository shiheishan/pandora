package nodefabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// nodeDeliveryStatusScenario 证明 w8node 的两件事（用户 2026-10-07 / 10-08 定）：
//
//   - 封禁即断、解封恢复：账号不是 active 时名下订阅不进节点名单；改状态推进下发纪元
//     （00141），登录时间这类写不推进；风控批量停用用的那条 UPDATE 同样生效；
//   - 下发变化 1–2 秒内推给节点：aegis-node 的信号循环读纪元，不靠 Valkey；到期没有写，
//     按名单里最早的到期时刻定时推；连串的变化按节流合并成每秒至多一轮。
//
// 由 TestTrafficChargePG18 调用（traffic_charge 域的库，已应用 configure-app-role.sql）。
func nodeDeliveryStatusScenario(t *testing.T, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const (
		tenantID = "75c80000-0000-7000-8000-000000000001"
		userU    = "75c80000-0000-7000-8000-000000000011"
		userV    = "75c80000-0000-7000-8000-000000000012"
		userX    = "75c80000-0000-7000-8000-000000000013"
		subU     = "75c80000-0000-7000-8000-000000000021"
		subV     = "75c80000-0000-7000-8000-000000000022"
		subX     = "75c80000-0000-7000-8000-000000000023"
		planVer  = "75c80000-0000-7000-8000-000000000031"
		poolID   = "75c80000-0000-7000-8000-000000000041"
		serverID = "75c80000-0000-7000-8000-000000000051"
		nodeID   = "75c80000-0000-7000-8000-000000000061"
		uidU     = 7581001
		uidV     = 7581002
		uidX     = 7581003
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'node-status-pg18', 'Node Status PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES
		($1, $4, 'node-status-u@example.test', 'U', 'active'),
		($2, $4, 'node-status-v@example.test', 'V', 'active'),
		($3, $4, 'node-status-x@example.test', 'X', 'active')`, userU, userV, userX, tenantID)
	must(`INSERT INTO node_pools (id, tenant_id, code, name, status) VALUES ($1, $2, 'node-status', 'Node Status', 'active')`,
		poolID, tenantID)
	// 套餐、服务器与节点的生命周期与本场景无关：关掉触发器（含外键）直接插，
	// 池授权与账号状态照常走约束与触发器
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO plan_versions (id, tenant_id, plan_id, version) VALUES ($1, $2, gen_random_uuid(), 1)`,
		planVer, tenantID)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $4, $5, gen_random_uuid(), $8, 'active', 'CNY', 0, now() + interval '30 days', $9),
		       ($2, $4, $6, gen_random_uuid(), $8, 'active', 'CNY', 0, now() + interval '30 days', $10),
		       ($3, $4, $7, gen_random_uuid(), $8, 'active', 'CNY', 0, now() + interval '30 days', $11)`,
		subU, subV, subX, tenantID, userU, userV, userX, planVer, int64(uidU), int64(uidV), int64(uidX))
	must(`INSERT INTO servers (id, tenant_id, name, status) VALUES ($1, $2, 'node-status-server', 'ready')`,
		serverID, tenantID)
	must(`INSERT INTO nodes (id, tenant_id, name, pool_id, status, node_type, server_host, server_port,
			server_id, serving_status, protocol_schema_version, config_validated_at)
		VALUES ($1, $2, 'node-status-node', $3, 'active', 'vless', 'node-status.invalid', 24431,
		        $4, 'active', 1, now())`, nodeID, tenantID, poolID, serverID)
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id) VALUES ($1, $2, $3)`,
		tenantID, planVer, poolID)

	epoch := func() int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT last_value + is_called::int FROM node_delivery_epoch`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// 以运行角色写，与后台、风控走同一套权限与触发器
	asApp := func(sql string, args ...any) {
		t.Helper()
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatalf("app write: %v\nSQL: %s", err, sql)
		}
	}
	setStatus := func(user, status string) {
		t.Helper()
		asApp(`UPDATE users SET status = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, user, status)
	}

	// ---- 名单：封禁即断、解封恢复（直查路径，不带纪元、不带节点门槛）----
	direct := NewService(app, nil)
	pool := poolID
	served := func(step string) map[int64]bool {
		t.Helper()
		users, err := direct.ListNodeUsers(ctx, tenantID, &ServingNode{PoolID: &pool})
		if err != nil {
			t.Fatalf("%s: list node users: %v", step, err)
		}
		out := map[int64]bool{}
		for _, u := range users {
			out[u.ID] = true
		}
		return out
	}
	if got := served("all active"); !got[uidU] || !got[uidV] || !got[uidX] || len(got) != 3 {
		t.Fatalf("active owners must all be served: %v", got)
	}
	for _, step := range []struct {
		status string
		served bool
	}{{"banned", false}, {"suspended", false}, {"active", true}} {
		before := epoch()
		setStatus(userU, step.status)
		if after := epoch(); after <= before {
			t.Fatalf("status %s did not advance the delivery epoch (%d -> %d)", step.status, before, after)
		}
		if got := served(step.status); got[uidU] != step.served || !got[uidV] || !got[uidX] {
			t.Fatalf("after status %s: served=%v, want U served=%v", step.status, got, step.served)
		}
	}
	// 风控批量停用（adminops.DisableIPClusterAccounts）的那条 UPDATE 原样执行
	before := epoch()
	asApp(`UPDATE users SET status = 'suspended' WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, userV)
	if after := epoch(); after <= before || served("risk suspend")[uidV] {
		t.Fatalf("risk suspension must advance the epoch (%d -> %d) and drop the owner", before, after)
	}
	setStatus(userV, "active")
	// 与下发无关的列不推进纪元
	before = epoch()
	asApp(`UPDATE users SET last_login_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, userV)
	setStatus(userV, "active") // 状态没变
	if after := epoch(); after != before {
		t.Fatalf("unrelated user writes advanced the delivery epoch (%d -> %d)", before, after)
	}
	t.Log("marker=node_delivery_pg18_banned_owner_dropped_and_restored_ok")

	// 名单带出最早的到期时刻
	must(`UPDATE subscriptions SET current_period_end = now() + interval '90 seconds' WHERE id = $1`, subX)
	set, err := direct.nodeUsers(ctx, tenantID, &ServingNode{PoolID: &pool})
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(set.nextExpiry); left < 80*time.Second || left > 100*time.Second {
		t.Fatalf("next expiry = %v (in %v), want about 90s", set.nextExpiry, left)
	}
	must(`UPDATE subscriptions SET current_period_end = now() + interval '30 days' WHERE id = $1`, subX)

	// ---- 推送：aegis-node 的形态（缓存开、事件流挂上、没有 Valkey）----
	svc := NewService(app, nil)
	svc.EnableNodeCaches()
	hub := NewStreamHub()
	svc.AttachStream(hub)
	conn := NewStreamConn(tenantID, nodeID, 256)
	svc.RegisterStream(conn, slog.Default())
	defer svc.UnregisterStream(conn)

	state := map[int64]ProxyUser{}
	messages := 0
	apply := func(raw []byte) {
		t.Helper()
		var msg StreamMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode stream message: %v", err)
		}
		switch msg.Event {
		case EventSyncUsers:
			var p SyncUsersPayload
			if err := json.Unmarshal(msg.Data, &p); err != nil {
				t.Fatal(err)
			}
			state = map[int64]ProxyUser{}
			for _, u := range p.Users {
				state[u.ID] = u
			}
		case EventSyncUserDelta:
			var p SyncUserDeltaPayload
			if err := json.Unmarshal(msg.Data, &p); err != nil {
				t.Fatal(err)
			}
			for _, u := range p.Delta.Added {
				state[u.ID] = u
			}
			for _, id := range p.Delta.Removed {
				delete(state, id)
			}
		default:
			return
		}
		messages++
	}
	// waitFor 读事件流直到 cond 成立，返回用时；超时失败。
	waitFor := func(step string, limit time.Duration, cond func() bool) time.Duration {
		t.Helper()
		start := time.Now()
		deadline := time.NewTimer(limit)
		defer deadline.Stop()
		for !cond() {
			select {
			case raw := <-conn.Send:
				apply(raw)
			case <-deadline.C:
				t.Fatalf("%s: not pushed within %v; node sees %v", step, limit, state)
			}
		}
		return time.Since(start)
	}
	has := func(uid int64) bool { _, ok := state[int64(uid)]; return ok }

	waitFor("initial push", 5*time.Second, func() bool { return has(uidU) && has(uidV) && has(uidX) })

	// 封禁：节点 1–2 秒内收到移出（CI 的机器忙，上限放到 3 秒；实测值记在日志里）
	setStatus(userU, "banned")
	took := waitFor("ban", 3*time.Second, func() bool { return !has(uidU) })
	t.Logf("marker=node_delivery_pg18_ban_pushed_ok latency=%v", took.Round(time.Millisecond))
	setStatus(userU, "active")
	took = waitFor("unban", 3*time.Second, func() bool { return has(uidU) })
	t.Logf("marker=node_delivery_pg18_unban_pushed_ok latency=%v", took.Round(time.Millisecond))

	// 到期：没有任何写，名单里最早的到期时刻一到就推
	must(`UPDATE subscriptions SET current_period_end = now() + interval '2 seconds' WHERE id = $1`, subX)
	var expiresAt time.Time
	if err := admin.QueryRow(ctx, `SELECT current_period_end FROM subscriptions WHERE id = $1`, subX).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	waitFor("expiry", 6*time.Second, func() bool { return !has(uidX) })
	late := time.Since(expiresAt)
	if late < -200*time.Millisecond || late > 3*time.Second {
		t.Fatalf("expiry pushed %v after the period end, want within 0–3s", late)
	}
	t.Logf("marker=node_delivery_pg18_expiry_pushed_ok after_expiry=%v", late.Round(time.Millisecond))

	// 合并与节流：1.5 秒里 30 次独立提交（每次都改了名单），节点收到的推送按轮合并，
	// 每秒至多一轮；最后的状态与库一致。
	time.Sleep(nodeFanoutMinInterval + nodeEpochPollInterval + nodeEpochSettle)
	for drained := false; !drained; {
		select {
		case raw := <-conn.Send:
			apply(raw)
		default:
			drained = true
		}
	}
	messages = 0
	burstStart := time.Now()
	const writes = 30
	for i := 1; i <= writes; i++ {
		must(`UPDATE subscriptions SET device_limit = $2 WHERE id = $1`, subV, i)
		time.Sleep(50 * time.Millisecond)
	}
	waitFor("burst settles", 4*time.Second, func() bool { return state[int64(uidV)].DeviceLimit == writes })
	burst := time.Since(burstStart)
	if maxRounds := int(burst/nodeFanoutMinInterval) + 2; messages > maxRounds {
		t.Fatalf("%d writes over %v produced %d pushes, want at most %d (one round per second)",
			writes, burst.Round(time.Millisecond), messages, maxRounds)
	}
	t.Logf("marker=node_delivery_pg18_burst_coalesced_ok writes=%d pushes=%d span=%v",
		writes, messages, burst.Round(time.Millisecond))
}
