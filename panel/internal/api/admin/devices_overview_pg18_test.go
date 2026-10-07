package admin

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// 改前的在线设备概览 SQL：每条在用订阅走一次在线设备视图的 LATERAL，再按设备数排序取 200。
// 只用于对照结果与计划。
const legacyOnlineDevicesSQL = `
	SELECT s.id::text, s.user_id::text, COALESCE(u.email,''), COALESCE(p.name,''),
	       COALESCE(s.device_limit, pv.max_devices, 0),
	       COALESCE(d.device_count, 0), COALESCE(d.node_count, 0),
	       (s.device_limit IS NOT NULL),
	       d.last_seen_at
	  FROM subscriptions s
	  JOIN plan_versions pv ON pv.id = s.plan_version_id
	  LEFT JOIN plans p ON p.id = s.plan_id
	  LEFT JOIN users u ON u.id = s.user_id
	  LEFT JOIN LATERAL (
	        SELECT od.device_count, od.node_count, od.last_seen_at
	          FROM subscription_online_devices od
	         WHERE od.tenant_id = s.tenant_id AND od.subscription_id = s.id
	  ) d ON true
	 WHERE s.tenant_id = $1
	   AND s.status IN ('active','trialing','grace')
	 ORDER BY COALESCE(d.device_count,0) DESC, s.created_at DESC
	 LIMIT 200`

// checkOnlineDevicesOverviewMatchesLegacy：/v1/devices 改成「先聚合、取前 200、再拼用户
// 与套餐」之后，与改前的 SQL 逐行一致（含补齐的离线订阅、同数按创建时间、窗口外的旧记录、
// 跨节点同一 IP、订阅级覆盖），并在日志里给出两条 SQL 的 EXPLAIN ANALYZE 对照。
func checkOnlineDevicesOverviewMatchesLegacy(t *testing.T, ctx context.Context, adminDB *pgxpool.Pool, app *platformdb.Pool) {
	const (
		tenant  = "c19d0000-0000-4000-8000-000000000001"
		actor   = "c19d0000-0000-4000-8000-000000000011"
		product = "c19d0000-0000-4000-8000-000000000021"
		plan    = "c19d0000-0000-4000-8000-000000000031"
		planVer = "c19d0000-0000-4000-8000-000000000041"
		pool    = "c19d0000-0000-4000-8000-000000000051"
		server  = "c19d0000-0000-4000-8000-000000000061"
		node1   = "c19d0000-0000-4000-8000-000000000071"
		node2   = "c19d0000-0000-4000-8000-000000000072"
		node3   = "c19d0000-0000-4000-8000-000000000073"
		subs    = 1200
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := adminDB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed online devices overview fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'devices-overview-pg18','Devices Overview PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@devices-overview.invalid','Operator','active')`, tenant, actor)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'dov-product','DOV Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'dov','DOV','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'dov-plan','DOV Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by,max_devices) VALUES($3,$1,$2,1,$4,2)`, tenant, plan, planVer, actor)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'dov-server','ready')`, tenant, server)
	for i, node := range []string{node1, node2, node3} {
		must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
			  VALUES($2,$1,$3,$4,'active','vless','dov.invalid',$5,$6,'active',1,now(),now())`,
			tenant, node, "dov-node-"+string(rune('a'+i)), pool, 443+i, server)
	}
	// 用户与订阅：状态混着在用与不在用，创建时间互不相同（同设备数时的次序可判定）
	must(`INSERT INTO users(tenant_id,email,status)
	      SELECT $1, 'dev' || g || '@devices-overview.invalid', 'active' FROM generate_series(1,$2::int) g`, tenant, subs)
	must(`INSERT INTO subscriptions(tenant_id,user_id,plan_id,plan_version_id,status,device_limit,
	                                snapshot_currency,snapshot_amount,created_at)
	      SELECT $1, u.id, $2, $3,
	             CASE WHEN g % 7 = 0 THEN 'expired' WHEN g % 11 = 0 THEN 'grace'
	                  WHEN g % 13 = 0 THEN 'trialing' ELSE 'active' END,
	             CASE WHEN g % 17 = 0 THEN 5 END,
	             'USD', 100, now() - g * interval '1 minute'
	        FROM (SELECT id, split_part(split_part(email::text,'@',1),'dev',2)::int AS g
	                FROM users WHERE tenant_id = $1 AND email LIKE 'dev%') u`, tenant, plan, planVer)
	// 在线记录：约十分之一的订阅在线 1–5 台（分在两个节点上），一部分同一 IP 也连着第三个
	// 节点（设备数不变、节点数 +1），一部分还有窗口外的旧记录（不该算进来）
	must(`WITH s AS (SELECT id, row_number() OVER (ORDER BY created_at DESC) AS g
	                   FROM subscriptions WHERE tenant_id = $1)
	      INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
	      SELECT $1, CASE WHEN k % 2 = 0 THEN $2::uuid ELSE $3::uuid END, s.id,
	             decode(lpad(to_hex(k),4,'0'),'hex'), now() - (s.g % 3) * interval '1 minute'
	        FROM s CROSS JOIN LATERAL generate_series(1, 1 + (s.g % 5)::int) k
	       WHERE s.g % 10 = 0`, tenant, node1, node2)
	must(`WITH s AS (SELECT id, row_number() OVER (ORDER BY created_at DESC) AS g
	                   FROM subscriptions WHERE tenant_id = $1)
	      INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
	      SELECT $1, $2::uuid, s.id, decode('0001','hex'), now() FROM s WHERE s.g % 20 = 0`, tenant, node3)
	must(`WITH s AS (SELECT id, row_number() OVER (ORDER BY created_at DESC) AS g
	                   FROM subscriptions WHERE tenant_id = $1)
	      INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
	      SELECT $1, $2::uuid, s.id, decode('ffff','hex'), now() - interval '30 minutes'
	        FROM s WHERE s.g % 9 = 0`, tenant, node1)
	must(`ANALYZE subscriptions`)
	must(`ANALYZE node_alive_ips`)
	must(`ANALYZE users`)

	svc := nodefabric.NewService(app, nil)
	got, err := svc.ListOnlineDevices(ctx, tenant)
	if err != nil {
		t.Fatalf("list online devices: %v", err)
	}
	var want []nodefabric.OnlineDevice
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, legacyOnlineDevicesSQL, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it nodefabric.OnlineDevice
			if err := rows.Scan(&it.SubscriptionID, &it.UserID, &it.Email, &it.Plan,
				&it.Limit, &it.Online, &it.Nodes, &it.Overridden, &it.LastSeenAt); err != nil {
				return err
			}
			it.Exceeded = it.Limit > 0 && it.Online > it.Limit+got.Grace
			want = append(want, it)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("legacy online devices: %v", err)
	}
	if len(want) != 200 || len(got.Devices) != len(want) {
		t.Fatalf("overview rows new=%d legacy=%d, want 200 each", len(got.Devices), len(want))
	}
	online, overridden := 0, 0
	for i := range want {
		a, b := got.Devices[i], want[i]
		sameSeen := (a.LastSeenAt == nil) == (b.LastSeenAt == nil) &&
			(a.LastSeenAt == nil || a.LastSeenAt.Equal(*b.LastSeenAt))
		a.LastSeenAt, b.LastSeenAt = nil, nil
		if !sameSeen || !reflect.DeepEqual(a, b) {
			t.Fatalf("row %d differs:\nnew    %+v\nlegacy %+v", i, got.Devices[i], want[i])
		}
		if a.Online > 0 {
			online++
		}
		if a.Overridden {
			overridden++
		}
	}
	// 夹具确实覆盖了补齐分支与订阅级覆盖
	if online == 0 || online == 200 || overridden == 0 {
		t.Fatalf("fixture does not exercise both branches: online rows=%d overridden=%d", online, overridden)
	}

	explain := func(query string) (float64, int64) {
		var raw []byte
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, tenant).Scan(&raw)
		}); err != nil {
			t.Fatalf("explain: %v", err)
		}
		var plans []struct {
			Plan struct {
				Hit  int64 `json:"Shared Hit Blocks"`
				Read int64 `json:"Shared Read Blocks"`
			} `json:"Plan"`
			ExecutionTime float64 `json:"Execution Time"`
		}
		if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
			t.Fatalf("decode plan: %v", err)
		}
		return plans[0].ExecutionTime, plans[0].Plan.Hit + plans[0].Plan.Read
	}
	// 先各跑一遍热缓存再量
	explain(legacyOnlineDevicesSQL)
	explain(nodefabric.OnlineDevicesOverviewSQL())
	start := time.Now()
	beforeMS, beforeBuf := explain(legacyOnlineDevicesSQL)
	afterMS, afterBuf := explain(nodefabric.OnlineDevicesOverviewSQL())
	t.Logf("/v1/devices（%d 条订阅、%d 行在用前 200 中在线 %d 行）改前 %.2fms / %d 块，改后 %.2fms / %d 块（量测 %s）",
		subs, len(want), online, beforeMS, beforeBuf, afterMS, afterBuf, time.Since(start).Round(time.Millisecond))
	if afterBuf >= beforeBuf {
		t.Fatalf("new overview reads %d buffers, legacy %d: aggregating first must read less", afterBuf, beforeBuf)
	}
}
