package nodefabric

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// perSubscriptionPacksScenario 证明购买模型统一（2026-10-07）的「节点下发按订阅」：
//
//   - 同一个人的 A、B 两份套餐额度都用完，流量包挂在 B 份：B 照常下发，A 不下发；
//     他名下另有一笔还没加到任何一份的流量包，也不让 A 下发；
//   - A 份产生的流量只记在 A 的套餐上，不扣 B 份的包；
//   - 把那笔未分配的包转到 A 份（改挂加转移流水同一事务）推进下发纪元，A 随即重新下发。
//
// 由 TestTrafficChargePG18 调用（traffic_charge 域的库，已应用 configure-app-role.sql）。
func perSubscriptionPacksScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantID   = "75b00000-0000-7000-8000-000000000001"
		userID     = "75b00000-0000-7000-8000-000000000011"
		subA       = "75b00000-0000-7000-8000-000000000021"
		subB       = "75b00000-0000-7000-8000-000000000022"
		planVer    = "75b00000-0000-7000-8000-000000000031"
		poolID     = "75b00000-0000-7000-8000-000000000041"
		packB      = "75b00000-0000-7000-8000-000000000051"
		unattached = "75b00000-0000-7000-8000-000000000052"
		uidA       = 7550001
		uidB       = 7550002
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'traffic-per-sub-pg18', 'Traffic Per Subscription PG18', 'CNY')`, tenantID)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $2, 'traffic-per-sub@example.test', 'Traffic Per Subscription', 'active')`, userID, tenantID)
	must(`INSERT INTO node_pools (id, tenant_id, code, name, status) VALUES ($1, $2, 'per-sub', 'Per Sub', 'active')`,
		poolID, tenantID)
	// 套餐与产品与本场景无关：关掉触发器（含外键）直接插版本与订阅，池绑定、配额与流量包照常走约束
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO plan_versions (id, tenant_id, plan_id, version) VALUES ($1, $2, gen_random_uuid(), 1)`,
		planVer, tenantID)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end, node_uid)
		VALUES ($1, $3, $4, gen_random_uuid(), $5, 'active', 'CNY', 0, now() + interval '30 days', $6),
		       ($2, $3, $4, gen_random_uuid(), $5, 'active', 'CNY', 0, now() + interval '30 days', $7)`,
		subA, subB, tenantID, userID, planVer, int64(uidA), int64(uidB))
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id) VALUES ($1, $2, $3)`,
		tenantID, planVer, poolID)
	// 两份的套餐额度都用完了
	must(`INSERT INTO quota_balances (tenant_id, subscription_id, metric, period,
			period_start, period_end, granted, limit_value, consumed)
		VALUES ($1, $2, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 100, 100, 100),
		       ($1, $3, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', 100, 100, 100)`,
		tenantID, subA, subB)
	// B 份挂着 50；另有 40 还没加到任何一份
	must(`INSERT INTO traffic_pack_grants (id, tenant_id, user_id, subscription_id, source, source_id, granted_bytes)
		VALUES ($1, $3, $4, $5, 'migration', gen_random_uuid(), 50),
		       ($2, $3, $4, NULL, 'migration', gen_random_uuid(), 40)`,
		packB, unattached, tenantID, userID, subB)

	svc := NewService(app, nil)
	pool := poolID
	served := func(step string) map[int64]bool {
		t.Helper()
		// 不带纪元、不带节点门槛的直查：只看订阅与池的判定
		users, err := svc.ListNodeUsers(ctx, tenantID, &ServingNode{PoolID: &pool})
		if err != nil {
			t.Fatalf("%s: list node users: %v", step, err)
		}
		out := map[int64]bool{}
		for _, u := range users {
			out[u.ID] = true
		}
		return out
	}
	if got := served("before transfer"); got[uidA] || !got[uidB] || len(got) != 1 {
		t.Fatalf("exhausted A without packs must not be served, B with its own pack must: %v", got)
	}
	t.Log("marker=traffic_charge_pg18_delivery_per_subscription_ok")

	// A 份又用了 30：只记在 A 的套餐上，B 份的包与未分配的包都不动
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return chargeTraffic(ctx, tx, tenantID, subA, 30)
	}); err != nil {
		t.Fatalf("charge A: %v", err)
	}
	var planA, usedB, usedFree int64
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT consumed FROM quota_balances WHERE subscription_id = $1),
		       (SELECT consumed_bytes FROM traffic_pack_grants WHERE id = $2),
		       (SELECT consumed_bytes FROM traffic_pack_grants WHERE id = $3)`,
		subA, packB, unattached).Scan(&planA, &usedB, &usedFree); err != nil {
		t.Fatal(err)
	}
	if planA != 130 || usedB != 0 || usedFree != 0 {
		t.Fatalf("A's overage: plan A=%d, B's pack used=%d, unattached used=%d; want 130 0 0", planA, usedB, usedFree)
	}
	t.Log("marker=traffic_charge_pg18_overage_never_drains_other_subscription_ok")

	// 把未分配的那笔转到 A 份：改挂与转移流水同一事务（00137 的约束触发器在提交时核对）
	epoch := func() int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT last_value + is_called::int FROM node_delivery_epoch`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := epoch()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE traffic_pack_grants SET subscription_id = $2 WHERE id = $1`, []any{unattached, subA}},
		{`INSERT INTO traffic_pack_transfers (tenant_id, grant_id, user_id, from_subscription_id,
				to_subscription_id, remaining_bytes, actor_kind, actor_id)
			VALUES ($1, $2, $3, NULL, $4, 40, 'user', $3)`, []any{tenantID, unattached, userID, subA}},
	} {
		if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("transfer: %v\nSQL: %s", err, step.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit transfer: %v", err)
	}
	if after := epoch(); after <= before {
		t.Fatalf("delivery epoch did not advance on a pack transfer: %d -> %d", before, after)
	}
	if got := served("after transfer"); !got[uidA] || !got[uidB] || len(got) != 2 {
		t.Fatalf("after the transfer both subscriptions must be served: %v", got)
	}
	t.Log("marker=traffic_charge_pg18_transfer_advances_epoch_ok")
}
