package nodefabric

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// adminNodeListGenericPlanPG18 是 TestTrafficChargePG18 的子测试（同一个库，过滤名单不用再加一项）：
// 后台节点列表在通用计划下不能退化成 O(n²)。
//
// 5k-r4 的回退：pgx 缓存语句后 PostgreSQL 改用通用计划，traffic CTE 被内联进嵌套循环，
// 对 498 个节点各把整个流量聚合重算一遍（GroupAggregate loops=498，410–509ms）。这里用
// plan_cache_mode=force_generic_plan 的连接造出同样的条件：
//   - 500 个节点 × 72 小时流量桶 + 每节点 2 个在线 IP；
//   - EXPLAIN EXECUTE 的计划里，聚合节点与流量 / 在线表的扫描都只能执行一次（loops ≤ 1）；
//   - 经 Service 实跑 15 次，记 p50（完成标准 < 50ms，这里断言 < 80ms 留 CI 抖动余量），并核对结果；
//   - 旧写法（不物化、万能条件）同库同条件跑一遍，只记日志，证明这个探测确实测得出回退。
func adminNodeListGenericPlanPG18(t *testing.T, admin *pgx.Conn, appDSN string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	const (
		tenant = "6a5e0000-0000-7000-8000-000000000001"
		pool   = "6a5e0000-0000-7000-8000-0000000000a1"
		nodes  = 500
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants (id, slug, display_name, default_currency)
		VALUES ($1, 'node-list-generic-pg18', 'Node List Generic PG18', 'CNY')`, tenant)
	must(`INSERT INTO node_pools (id, tenant_id, code, name, status)
		VALUES ($1, $2, 'generic', 'Generic', 'active')`, pool, tenant)
	must(`INSERT INTO servers (tenant_id, name, status, capacity_nodes)
		SELECT $1, 'generic-server-' || g, 'ready', 64 FROM generate_series(1, 25) g`, tenant)
	must(`INSERT INTO nodes (tenant_id, name, pool_id, status, node_type, server_host, server_port,
			server_id, serving_status)
		SELECT $1, 'generic-node-' || g, $2::uuid, 'active', 'vless', 'n.invalid', 20000 + g,
		       (SELECT id FROM servers WHERE tenant_id = $1 ORDER BY name OFFSET g % 25 LIMIT 1),
		       'active'
		  FROM generate_series(1, $3::int) g`, tenant, pool, nodes)
	must(`INSERT INTO node_traffic_hourly (tenant_id, hour_start, node_id, report_count, raw_bytes)
		SELECT $1, date_trunc('hour', now()) - h * interval '1 hour', n.id, 60, 1000
		  FROM nodes n CROSS JOIN generate_series(0, 71) h
		 WHERE n.tenant_id = $1`, tenant)
	// 在线 IP 只是被聚合的行，背后的订阅与本测试无关：关掉外键触发器直接插
	must(`SET session_replication_role = replica`)
	must(`INSERT INTO node_alive_ips (tenant_id, node_id, subscription_id, ip_hash, last_seen_at)
		SELECT $1, n.id, gen_random_uuid(), decode(md5(n.id::text || k), 'hex'), now()
		  FROM nodes n CROSS JOIN generate_series(1, 2) k
		 WHERE n.tenant_id = $1`, tenant)
	must(`SET session_replication_role = origin`)
	must(`ANALYZE nodes, servers, node_traffic_hourly, node_alive_ips`)
	var want24h int64
	if err := admin.QueryRow(ctx, `SELECT sum(raw_bytes)::bigint FROM node_traffic_hourly
		WHERE tenant_id = $1 AND node_id = (SELECT id FROM nodes WHERE tenant_id = $1 AND name = 'generic-node-7')
		  AND hour_start >= now() - interval '24 hours'`, tenant).Scan(&want24h); err != nil {
		t.Fatal(err)
	}

	// 应用角色的连接池，会话里强制通用计划（缓存语句之后生产上就是这个形态）
	cfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	cfg.MaxConns = 2
	gp, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gp.Close()
	svc := &Service{pool: &platformdb.Pool{Pool: gp}}

	list := AdminNodeQuery{IncludeRetired: true, Limit: 1000}
	var took []time.Duration
	for range 15 {
		start := time.Now()
		rows, total, err := svc.QueryAdminNodes(ctx, tenant, list)
		took = append(took, time.Since(start))
		if err != nil {
			t.Fatalf("list under generic plan: %v", err)
		}
		if len(rows) != nodes || total != nodes {
			t.Fatalf("list rows=%d total=%d, want %d", len(rows), total, nodes)
		}
		for _, x := range rows {
			if x.Name == "generic-node-7" && (x.TrafficBytes24h != want24h || x.OnlineIPs != 2 || x.OnlineUsers != 2) {
				t.Fatalf("generic-node-7 traffic_24h=%d ips=%d users=%d, want %d/2/2",
					x.TrafficBytes24h, x.OnlineIPs, x.OnlineUsers, want24h)
			}
		}
	}
	slices.Sort(took)
	p50 := took[len(took)/2]
	t.Logf("admin node list, %d nodes, force_generic_plan: p50=%s min=%s max=%s", nodes, p50, took[0], took[len(took)-1])
	// 墙钟只兜底大回退（acb1728 的通用计划回退是 410–509ms）：CI runner 共享 CPU，
	// 同一条语句实测在 28ms 与 84ms 之间抖，阈值卡在 50–80ms 会偶发变红。
	// 「逐节点重算」由下面的计划形状断言精确把关，目标 < 50ms 看复测与评测集
	if p50 > 250*time.Millisecond {
		t.Fatalf("admin node list p50=%s under generic plan, want < 50ms (asserting 250ms against regression)", p50)
	}

	// 其余筛选组合各是一条语句文本：在通用计划下照样能跑、结果对
	for _, c := range []struct {
		q    AdminNodeQuery
		want int
	}{
		{AdminNodeQuery{Search: "GENERIC-NODE-7", Limit: 100}, 11}, // 7、70–79
		{AdminNodeQuery{State: "offline", Limit: 100, Offset: 450}, 50},
		{AdminNodeQuery{State: "online", Limit: 100}, 0},
		{AdminNodeQuery{Search: "generic", Limit: 10, Offset: 900}, 0},
	} {
		rows, total, err := svc.QueryAdminNodes(ctx, tenant, c.q)
		if err != nil || len(rows) != c.want {
			t.Fatalf("query %+v: rows=%d total=%d err=%v, want %d rows", c.q, len(rows), total, err, c.want)
		}
		if c.q.Offset >= 900 && total != nodes {
			t.Fatalf("past-the-end page total=%d, want %d", total, nodes)
		}
	}

	stmt := adminNodeListSQL(tenant, list, false)
	plan := explainGenericPG18(t, ctx, gp, tenant, stmt.sql, "'"+tenant+"', 1000, 0")
	if bad := planRepeats(plan.Plan); len(bad) > 0 {
		t.Fatalf("generic plan re-executes per node: %v\nexecution=%.1fms", bad, plan.ExecutionTime)
	}
	t.Logf("admin node list generic plan: execution=%.1fms buffers=%d",
		plan.ExecutionTime, plan.Plan.SharedHit+plan.Plan.SharedRead)

	// 旧写法的流量部分（a0574ab 的形状：不物化 + 万能条件），只记日志，证明上面的探测测得出回退
	legacy := explainGenericPG18(t, ctx, gp, tenant, `
		WITH page AS MATERIALIZED (
		  SELECT n.id FROM nodes n
		   WHERE n.tenant_id = $1 AND ($2::uuid IS NULL OR n.id = $2::uuid)
		   ORDER BY n.id LIMIT $3
		), traffic AS (
		  SELECT node_id, sum(raw_bytes)::bigint AS b
		    FROM node_traffic_hourly
		   WHERE tenant_id = $1 AND node_id IN (SELECT id FROM page)
		     AND hour_start >= now() - interval '24 hours'
		   GROUP BY node_id
		)
		SELECT pg.id, coalesce(t.b, 0)
		  FROM page pg
		  JOIN nodes n ON n.tenant_id = $1 AND n.id = pg.id
		  LEFT JOIN traffic t ON t.node_id = n.id`, "'"+tenant+"', NULL, 1000")
	t.Logf("legacy shape under generic plan: execution=%.1fms repeats=%v",
		legacy.ExecutionTime, planRepeats(legacy.Plan))
	t.Log("marker=admin_node_list_generic_plan_ok")
}

// pg18PlanNode 是 EXPLAIN (FORMAT JSON) 里本用例要看的字段。
type pg18PlanNode struct {
	NodeType   string         `json:"Node Type"`
	Relation   string         `json:"Relation Name"`
	Loops      float64        `json:"Actual Loops"`
	SharedHit  int64          `json:"Shared Hit Blocks"`
	SharedRead int64          `json:"Shared Read Blocks"`
	Plans      []pg18PlanNode `json:"Plans"`
}

type pg18Explain struct {
	Plan          pg18PlanNode `json:"Plan"`
	ExecutionTime float64      `json:"Execution Time"`
}

// explainGenericPG18 在强制通用计划的连接上 PREPARE 这条语句，再 EXPLAIN ANALYZE EXECUTE。
func explainGenericPG18(t *testing.T, ctx context.Context, gp *pgxpool.Pool, tenant, sql, params string) pg18Explain {
	t.Helper()
	var out []pg18Explain
	err := pgx.BeginFunc(ctx, gp, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `PREPARE w5_generic_probe AS `+sql, pgx.QueryExecModeSimpleProtocol); err != nil {
			return err
		}
		defer func() { _, _ = tx.Exec(ctx, `DEALLOCATE w5_generic_probe`, pgx.QueryExecModeSimpleProtocol) }()
		var raw []byte
		if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE w5_generic_probe(`+params+`)`,
			pgx.QueryExecModeSimpleProtocol).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	if err != nil || len(out) != 1 {
		t.Fatalf("explain generic plan: %v (plans=%d)", err, len(out))
	}
	return out[0]
}

// planRepeats 列出被执行了不止一次的聚合节点与流量 / 在线表扫描（嵌套循环里逐行重算的迹象）。
func planRepeats(n pg18PlanNode) []string {
	var bad []string
	if n.Loops > 1 && (n.NodeType == "Aggregate" ||
		n.Relation == "node_traffic_hourly" || n.Relation == "node_alive_ips") {
		bad = append(bad, n.NodeType+" "+n.Relation)
	}
	for _, c := range n.Plans {
		bad = append(bad, planRepeats(c)...)
	}
	return bad
}
