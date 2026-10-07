package nodefabric

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// retentionScenario 证明保留期清理（00100、aegis-admin 的保留期任务）在运行角色下真能删、
// 只删该删的：
//   - PurgeMetrics 经 app.purge_node_metrics 删本租户 48 小时以前的探针点，近期的点与
//     别的租户的点不动；保留期传小了按 48 小时算，函数本身拒收小于 48 的保留期；
//   - 追加写保护不变：运行角色直接 DELETE node_metrics 仍被拒绝；
//   - PurgeStaleAlive 删 70 分钟以前的在线记录，窗口内的与别的租户的不动。
//
// 挂在 TestTrafficChargePG18 里跑：traffic_charge 是 nodefabric 唯一套了 configure-app-role
// （运行角色收窄、node_metrics 的 DELETE 已收回）的 PG18 库。
func retentionScenario(t *testing.T, ctx context.Context, admin *pgx.Conn, app *platformdb.Pool) {
	t.Helper()
	const (
		tenantA = "75300000-0000-7000-8000-000000000001"
		tenantB = "75300000-0000-7000-8000-000000000002"
		userA   = "75300000-0000-7000-8000-000000000011"
		userB   = "75300000-0000-7000-8000-000000000012"
		subA    = "75300000-0000-7000-8000-000000000021"
		subB    = "75300000-0000-7000-8000-000000000022"
		nodeA   = "75300000-0000-7000-8000-000000000031"
		nodeB   = "75300000-0000-7000-8000-000000000032"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'retention-a-pg18', 'Retention A', 'CNY'), ($2, 'retention-b-pg18', 'Retention B', 'CNY')`, tenantA, tenantB)
	must(`INSERT INTO users (id, tenant_id, email, display_name, status)
		VALUES ($1, $3, 'retention-a@example.test', 'Retention A', 'active'),
		       ($2, $4, 'retention-b@example.test', 'Retention B', 'active')`, userA, userB, tenantA, tenantB)
	must(`INSERT INTO nodes (id, tenant_id, name, status)
		VALUES ($1, $3, 'retention-a', 'active'), ($2, $4, 'retention-b', 'active')`, nodeA, nodeB, tenantA, tenantB)
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO subscriptions (id, tenant_id, user_id, plan_id, plan_version_id,
			status, snapshot_currency, snapshot_amount, current_period_end)
		VALUES ($1, $3, $5, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days'),
		       ($2, $4, $6, gen_random_uuid(), gen_random_uuid(), 'active', 'CNY', 0, now() + interval '30 days')`,
		subA, subB, tenantA, tenantB, userA, userB)
	must(`SET session_replication_role = origin`)
	must(`INSERT INTO node_metrics (tenant_id, node_id, recorded_at, cpu_bp)
		VALUES ($1, $2, now() - interval '50 hours', 100),
		       ($1, $2, now() - interval '49 hours', 100),
		       ($1, $2, now() - interval '1 hour', 100),
		       ($3, $4, now() - interval '60 hours', 100)`, tenantA, nodeA, tenantB, nodeB)
	must(`INSERT INTO node_alive_ips (tenant_id, node_id, subscription_id, ip_hash, last_seen_at)
		VALUES ($1, $2, $3, '\x01', now() - interval '71 minutes'),
		       ($1, $2, $3, '\x02', now() - interval '59 minutes'),
		       ($4, $5, $6, '\x03', now() - interval '3 hours')`, tenantA, nodeA, subA, tenantB, nodeB, subB)

	svc := NewService(app, nil)
	if n, err := svc.PurgeMetrics(ctx, tenantA, 1); err != nil || n != 2 {
		t.Fatalf("PurgeMetrics deleted=%d err=%v, want the two points older than 48 hours", n, err)
	}
	var leftA, leftB int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE tenant_id = $1), count(*) FILTER (WHERE tenant_id = $2)
		  FROM node_metrics WHERE tenant_id IN ($1, $2)`, tenantA, tenantB).Scan(&leftA, &leftB); err != nil {
		t.Fatal(err)
	}
	if leftA != 1 || leftB != 1 {
		t.Fatalf("node_metrics left tenantA=%d tenantB=%d, want the recent point and the other tenant untouched", leftA, leftB)
	}
	// 运行角色仍不能直接删追加写表，也不能借清理函数删近期数据
	for name, sql := range map[string]string{
		"direct delete":   `DELETE FROM node_metrics`,
		"short retention": `SELECT app.purge_node_metrics(1, 10)`,
	} {
		err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql)
			return err
		})
		if err == nil {
			t.Fatalf("%s on node_metrics was accepted for the app role", name)
		}
	}

	if n, err := svc.PurgeStaleAlive(ctx, tenantA); err != nil || n != 1 {
		t.Fatalf("PurgeStaleAlive deleted=%d err=%v, want the one row older than 70 minutes", n, err)
	}
	var aliveA, aliveB int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE tenant_id = $1), count(*) FILTER (WHERE tenant_id = $2)
		  FROM node_alive_ips WHERE tenant_id IN ($1, $2)`, tenantA, tenantB).Scan(&aliveA, &aliveB); err != nil {
		t.Fatal(err)
	}
	if aliveA != 1 || aliveB != 1 {
		t.Fatalf("node_alive_ips left tenantA=%d tenantB=%d, want the in-window row and the other tenant untouched", aliveA, aliveB)
	}
	t.Log("marker=retention_pg18_metrics_and_alive_purged_ok")
}
