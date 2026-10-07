package subscription

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// legacyOnlineDevicesView 是 00094 的视图定义原文，建成一张对照视图，
// 与 00098 的新定义在同一批数据上逐行比对。
const legacyOnlineDevicesView = `
CREATE OR REPLACE VIEW w1sub_legacy_online_devices AS
SELECT tenant_id,
       subscription_id,
       count(DISTINCT ip_hash)                    AS device_count,
       count(DISTINCT node_id)                    AS node_count,
       max(last_seen_at)                          AS last_seen_at
  FROM node_alive_ips
 WHERE last_seen_at > now() - make_interval(mins => app.device_limit_window_minutes(tenant_id))
 GROUP BY tenant_id, subscription_id`

const onlineViewDiffSQL = `
	SELECT (SELECT count(*) FROM subscription_online_devices),
	       (SELECT count(*) FROM w1sub_legacy_online_devices),
	       (SELECT count(*) FROM (
	          (SELECT tenant_id, subscription_id, device_count, node_count, last_seen_at
	             FROM subscription_online_devices
	           EXCEPT ALL
	           SELECT tenant_id, subscription_id, device_count, node_count, last_seen_at
	             FROM w1sub_legacy_online_devices)
	          UNION ALL
	          (SELECT tenant_id, subscription_id, device_count, node_count, last_seen_at
	             FROM w1sub_legacy_online_devices
	           EXCEPT ALL
	           SELECT tenant_id, subscription_id, device_count, node_count, last_seen_at
	             FROM subscription_online_devices)) d)`

// testOnlineDevicesViewPG18 钉住 00098：在线设备视图换了写法，但在多租户、各窗口、
// 非法值、边界时刻下与 00094 的结果逐行相同；当前租户的窗口一条语句只算一次；
// 按订阅取数走 idx_node_alive_recent；三个调用点（门户订阅列表、后台在线设备、
// 订阅链接）的输出与改前口径一致。挂在 TestDeviceWindowPG18 下跑（delivery 域的
// -run 过滤是写死的函数名列表）。
func testOnlineDevicesViewPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *platformdb.Pool) {
	// 同一个 delivery 库里还住着别的测试的夹具（api/admin 的 node_status_refusal 用 …0901 段），
	// 这里用十六进制 a0 段，与十进制编号的夹具不会撞
	const (
		tenantA = "93000000-0000-4000-8000-00000000a001"
		tenantB = "93000000-0000-4000-8000-00000000a002"
		owner   = "93000000-0000-4000-8000-00000000a011"
		subOn   = "93000000-0000-4000-8000-00000000a081"
		subOff  = "93000000-0000-4000-8000-00000000a082"
		prefix  = "f0e1d2c3b4a5"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed online devices fixture: %v\nSQL: %s", err, sql)
		}
	}
	// 每个租户：一个套餐版本、一台服务器、两个节点、一个批量用户
	for i, tenant := range []string{tenantA, tenantB} {
		tag := []string{"a", "b"}[i]
		must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'online-view-'||$2::text,'Online View','USD')`, tenant, tag)
		must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES(gen_random_uuid(),$1,'bulk-'||$2::text||'@online-view.invalid','Bulk','active')`, tenant, tag)
		must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES(gen_random_uuid(),$1,'ov-product','OV Product','active')`, tenant)
		must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES(gen_random_uuid(),$1,'ov','OV','active')`, tenant)
		must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status)
		      SELECT gen_random_uuid(),$1,p.id,'ov-plan','OV Plan','draft' FROM products p WHERE p.tenant_id=$1`, tenant)
		must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by,max_devices)
		      SELECT gen_random_uuid(),$1,pl.id,1,u.id,2 FROM plans pl, users u WHERE pl.tenant_id=$1 AND u.tenant_id=$1`, tenant)
		must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1`, tenant)
		must(`UPDATE plans pl SET current_version_id=pv.id, status='active' FROM plan_versions pv WHERE pv.plan_id=pl.id AND pl.tenant_id=$1`, tenant)
		must(`INSERT INTO servers(id,tenant_id,name,status) VALUES(gen_random_uuid(),$1,'ov-server-'||$2::text,'ready')`, tenant, tag)
		must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
		      SELECT gen_random_uuid(),$1,'ov-node-'||$2::text||'-'||g,np.id,'active','vless','ov-'||$2::text||'.invalid',443+g,sv.id,'active',1,now(),now()
		        FROM node_pools np, servers sv, generate_series(1,2) g WHERE np.tenant_id=$1 AND sv.tenant_id=$1`, tenant, tag)
		// 批量订阅：A 600 条、B 40 条，让规划器按真实形状选计划
		n := map[string]int{tenantA: 600, tenantB: 40}[tenant]
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		      SELECT gen_random_uuid(),$1,u.id,pv.plan_id,pv.id,'active','USD',100
		        FROM users u, plan_versions pv, generate_series(1,$2::int) g
		       WHERE u.tenant_id=$1 AND pv.tenant_id=$1`, tenant, n)
		// 每条批量订阅 8 行在线记录：4 个 IP、每个 IP 两个节点都在线，年龄铺在 0–119 分钟。
		// 年龄取「整分钟 + 30 秒」，离 5/10/30/60 分钟的截止点都有 30 秒，跨事务比对时
		// 不会有行恰好在两次查询之间跨过截止点
		must(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
		      SELECT $1, nd.id, s.id, decode(md5(s.id::text || k::text), 'hex'),
		             now() - make_interval(secs => (((hashtext(s.id::text)::bigint & 2147483647) + k * 17) % 120) * 60 + 30)
		        FROM subscriptions s
		        CROSS JOIN (SELECT id FROM nodes WHERE tenant_id=$1) nd
		        CROSS JOIN generate_series(1,4) k
		       WHERE s.tenant_id=$1`, tenant)
	}
	// A 里的目标用户：一条在线订阅、一条只有 90 分钟前记录的订阅
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@online-view.invalid','Owner','active')`, tenantA, owner)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,created_at)
	      SELECT x.id,$1,$2,pv.plan_id,pv.id,'active','USD',100,x.created
	        FROM plan_versions pv, (VALUES ($3::uuid, now()), ($4::uuid, now() - interval '1 day')) AS x(id, created)
	       WHERE pv.tenant_id=$1`, tenantA, owner, subOn, subOff)
	// subOn：同一 IP 连两个节点（1 台设备、2 个节点），其余 IP 落在 7/12/25/45/70 分钟前
	must(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
	      SELECT $1, nd.id, $2, decode(v.ip,'hex'), now() - make_interval(mins => v.age)
	        FROM (VALUES ('aa01',1,1),('aa01',2,2),('aa02',1,7),('aa03',1,12),('aa04',2,25),('aa05',1,45),('aa06',1,70)) AS v(ip,node,age)
	        JOIN (SELECT id, row_number() OVER (ORDER BY name) AS rn FROM nodes WHERE tenant_id=$1) nd ON nd.rn = v.node`,
		tenantA, subOn)
	must(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
	      SELECT $1, id, $2, '\xbb01', now() - interval '90 minutes' FROM nodes WHERE tenant_id=$1 ORDER BY name LIMIT 1`,
		tenantA, subOff)
	must(`INSERT INTO system_settings(tenant_id,key,value) VALUES($1,'device_limit.window_minutes','30')
	      ON CONFLICT (tenant_id,key) DO UPDATE SET value=EXCLUDED.value`, tenantB)
	must(`ANALYZE node_alive_ips`)
	must(`ANALYZE subscriptions`)
	must(legacyOnlineDevicesView)
	must(`GRANT SELECT ON w1sub_legacy_online_devices TO aegis_app`)
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP VIEW IF EXISTS w1sub_legacy_online_devices`) })

	type querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}
	compare := func(step, who string, q querier) {
		t.Helper()
		var newRows, oldRows, diff int64
		if err := q.QueryRow(ctx, onlineViewDiffSQL).Scan(&newRows, &oldRows, &diff); err != nil {
			t.Fatalf("%s/%s: compare views: %v", step, who, err)
		}
		if diff != 0 || newRows != oldRows || newRows == 0 {
			t.Fatalf("%s/%s: 00098 view differs from 00094: new=%d old=%d diff=%d", step, who, newRows, oldRows, diff)
		}
	}
	inAdminTx := func(tenant string, fn func(pgx.Tx)) {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if tenant != "" {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
				t.Fatal(err)
			}
		}
		fn(tx)
	}
	compareAll := func(step string) {
		t.Helper()
		// 不带租户的超级用户会话：所有租户的行，全部走「逐个调函数」的分支
		compare(step, "admin", admin)
		// 带租户的超级用户会话：本租户走 InitPlan，别的租户走逐个调函数
		inAdminTx(tenantA, func(tx pgx.Tx) { compare(step, "admin@A", tx) })
		// 运行时角色
		for _, tenant := range []string{tenantA, tenantB} {
			if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
				compare(step, "app@"+tenant[len(tenant)-3:], tx)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	setWindow := func(value string) {
		t.Helper()
		if value == "" {
			must(`DELETE FROM system_settings WHERE tenant_id=$1 AND key='device_limit.window_minutes'`, tenantA)
			return
		}
		must(`INSERT INTO system_settings(tenant_id,key,value) VALUES($1,'device_limit.window_minutes',$2::jsonb)
		      ON CONFLICT (tenant_id,key) DO UPDATE SET value=EXCLUDED.value`, tenantA, value)
	}
	// A 的窗口逐档切换，B 固定 30：两个租户同时用不同窗口
	wantOnline := map[string]int{"": 1, "10": 2, "30": 4, "60": 5, "7": 1, `"thirty"`: 1, "5": 1}
	for _, value := range []string{"", "10", "30", "60", "7", `"thirty"`, "5"} {
		setWindow(value)
		compareAll("window=" + value)
		var online int
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT coalesce((SELECT device_count FROM subscription_online_devices WHERE subscription_id=$1),0)`,
				subOn).Scan(&online)
		}); err != nil {
			t.Fatal(err)
		}
		if online != wantOnline[value] {
			t.Fatalf("window=%s: subOn online=%d, want %d", value, online, wantOnline[value])
		}
	}

	// 边界时刻：同一事务里 now() 不变，恰好在截止点上的行两边都不算、晚一微秒的都算
	boundary := errors.New("rollback boundary probe")
	err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
			SELECT $1, n.id, $2, v.ip, now() - make_interval(mins => 5) + v.shift
			  FROM (SELECT id FROM nodes WHERE tenant_id=$1 ORDER BY name LIMIT 1) n,
			       (VALUES ('\xcc01'::bytea, interval '0'), ('\xcc02'::bytea, interval '1 microsecond')) AS v(ip, shift)`,
			tenantA, subOn); err != nil {
			return err
		}
		compare("boundary", "app@A", tx)
		var online int
		if err := tx.QueryRow(ctx, `SELECT device_count FROM subscription_online_devices WHERE subscription_id=$1`, subOn).Scan(&online); err != nil {
			return err
		}
		if online != 2 {
			t.Errorf("boundary: online=%d, want 2 (aa01 + the row one microsecond inside the window)", online)
		}
		return boundary
	})
	if !errors.Is(err, boundary) {
		t.Fatalf("boundary probe: %v", err)
	}

	// 窗口函数的调用次数：新视图对当前租户只算一次，旧视图逐行算
	setWindow("30")
	var newCalls, oldCalls, aliveRows int64
	inAdminTx(tenantA, func(tx pgx.Tx) {
		calls := func() int64 {
			var n int64
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(calls),0) FROM pg_stat_xact_user_functions
				WHERE schemaname='app' AND funcname='device_limit_window_minutes'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		if _, err := tx.Exec(ctx, `SET LOCAL track_functions = 'all'`); err != nil {
			t.Fatal(err)
		}
		var rows int64
		base := calls()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM subscription_online_devices WHERE tenant_id=$1`, tenantA).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		newCalls = calls() - base
		base = calls()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM w1sub_legacy_online_devices WHERE tenant_id=$1`, tenantA).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		oldCalls = calls() - base
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM node_alive_ips WHERE tenant_id=$1`, tenantA).Scan(&aliveRows); err != nil {
			t.Fatal(err)
		}
	})
	if newCalls > 1 || oldCalls < aliveRows/2 {
		t.Fatalf("window function calls: new=%d old=%d alive rows=%d; want new<=1", newCalls, oldCalls, aliveRows)
	}

	// 计划形状：单条订阅、门户订阅列表、普通 LEFT JOIN（后台用户详情的写法）都按订阅走索引
	explain := func(name, sql string, args ...any) string {
		t.Helper()
		var plan []string
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantA, ActorID: owner}, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					return err
				}
				plan = append(plan, line)
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("explain %s: %v", name, err)
		}
		out := strings.Join(plan, "\n")
		t.Logf("EXPLAIN %s:\n%s", name, out)
		return out
	}
	perSub := func(name, plan string) {
		t.Helper()
		if !strings.Contains(plan, "idx_node_alive_recent") || strings.Contains(plan, "Seq Scan on node_alive_ips") {
			t.Errorf("%s does not probe idx_node_alive_recent per subscription", name)
		}
	}
	perSub("single subscription", explain("single subscription",
		`SELECT device_count FROM subscription_online_devices WHERE subscription_id=$1`, subOn))
	perSub("my subscriptions", explain("my subscriptions", mySubscriptionsSQL, tenantA, owner))
	perSub("plain left join", explain("plain left join", `
		SELECT s.id, coalesce(od.device_count, 0)
		  FROM subscriptions s
		  LEFT JOIN subscription_online_devices od ON od.tenant_id = s.tenant_id AND od.subscription_id = s.id
		 WHERE s.tenant_id = $1 AND s.user_id = $2
		 ORDER BY s.created_at DESC`, tenantA, owner))
	explain("plain left join on 00094 (reference)", `
		SELECT s.id, coalesce(od.device_count, 0)
		  FROM subscriptions s
		  LEFT JOIN w1sub_legacy_online_devices od ON od.tenant_id = s.tenant_id AND od.subscription_id = s.id
		 WHERE s.tenant_id = $1 AND s.user_id = $2
		 ORDER BY s.created_at DESC`, tenantA, owner)

	// 调用点一：门户「我的订阅」——在线数、配额分组与顺序、流量包余量
	must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
	      VALUES ($1,$2,'traffic.bytes','cycle',now()-interval '40 days',now()-interval '10 days',100,100,100),
	             ($1,$2,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',100,100,30),
	             ($1,$3,'traffic.bytes','cycle',now()-interval '5 days',now()+interval '25 days',50,50,5)`, tenantA, subOn, subOff)
	must(`INSERT INTO traffic_pack_grants(tenant_id,user_id,source,source_id,granted_bytes,consumed_bytes)
	      VALUES ($1,$2,'migration',gen_random_uuid(),100,40),($1,$2,'migration',gen_random_uuid(),50,50)`, tenantA, owner)
	svc := New(app, []byte("online-view-salt"), nil)
	mine, err := svc.MySubscriptions(ctx, tenantA, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 || mine[0].ID != subOn || mine[1].ID != subOff ||
		mine[0].OnlineDevices != 4 || mine[1].OnlineDevices != 0 ||
		mine[0].PackRemainingBytes != 60 || mine[1].PackRemainingBytes != 60 ||
		len(mine[0].Quotas) != 2 || len(mine[1].Quotas) != 1 ||
		mine[0].Quotas[0].Consumed != 30 || mine[0].Quotas[1].Consumed != 100 || mine[1].Quotas[0].Consumed != 5 {
		t.Fatalf("MySubscriptions = %+v", mine)
	}

	// 调用点二：后台在线设备概览——每行带 user_id，在线数与旧视图一致，按在线数降序
	owners := map[string]string{}
	legacyCounts := func() map[string]int {
		t.Helper()
		out := map[string]int{}
		inAdminTx(tenantA, func(tx pgx.Tx) {
			rows, err := tx.Query(ctx, `
				SELECT s.id::text, s.user_id::text, coalesce(o.device_count,0)::int
				  FROM subscriptions s LEFT JOIN w1sub_legacy_online_devices o ON o.subscription_id = s.id
				 WHERE s.tenant_id=$1`, tenantA)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var id, user string
				var n int
				if err := rows.Scan(&id, &user, &n); err != nil {
					t.Fatal(err)
				}
				out[id], owners[id] = n, user
			}
		})
		return out
	}
	// 概览与旧视图在两个事务里算，前后各取一次旧视图，概览的值必须等于其中之一
	before := legacyCounts()
	overview, err := nodefabric.NewService(app, nil).ListOnlineDevices(ctx, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	after := legacyCounts()
	if overview.WindowMinutes != 30 || len(overview.Devices) != 200 {
		t.Fatalf("overview window=%d rows=%d, want 30/200", overview.WindowMinutes, len(overview.Devices))
	}
	online := []int{}
	for _, d := range overview.Devices {
		if d.UserID == "" || d.UserID != owners[d.SubscriptionID] ||
			(d.Online != before[d.SubscriptionID] && d.Online != after[d.SubscriptionID]) {
			t.Fatalf("overview row %+v: want user %s online %d/%d", d, owners[d.SubscriptionID],
				before[d.SubscriptionID], after[d.SubscriptionID])
		}
		online = append(online, d.Online)
	}
	if !slices.IsSortedFunc(online, func(a, b int) int { return b - a }) {
		t.Fatalf("overview not ordered by online devices: %v", online)
	}

	// 调用点三：订阅链接——路径前缀与近 24 小时不同来源数并进一条语句
	must(`UPDATE tenants SET sub_path_prefix=$2 WHERE id=$1`, tenantA, prefix)
	envelope, err := crypto.NewEnvelope([]byte("online-view-master-key-0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("link", 10) // 虚构令牌
	sealed, err := envelope.Seal([]byte(token), []byte(subOn))
	if err != nil {
		t.Fatal(err)
	}
	var credID string
	if err := admin.QueryRow(ctx, `
		INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,token_encrypted)
		VALUES($1,$2,$3,$4,$5,'subscription',$6) RETURNING id::text`,
		tenantA, subOn, owner, crypto.HashToken(token), token[:8], sealed).Scan(&credID); err != nil {
		t.Fatal(err)
	}
	must(`INSERT INTO subscription_fetch_log(tenant_id,credential_id,subscription_id,ip_hash,result,fetched_at) VALUES
	      ($1,$2,$3,'\x01','ok',now()-interval '1 hour'),($1,$2,$3,'\x01','ok',now()-interval '2 hours'),
	      ($1,$2,$3,'\x02','ok',now()-interval '3 hours'),($1,$2,$3,'\x03','ok',now()-interval '25 hours'),
	      ($1,$2,$3,'\x04','not_found',now()-interval '1 hour')`, tenantA, credID, subOn)
	links, err := New(app, nil, envelope).ListLinks(ctx, tenantA, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Token != token || links[0].PathPrefix != prefix ||
		links[0].DistinctSources != 2 || links[0].CredentialID != credID {
		t.Fatalf("ListLinks = %+v", links)
	}
	t.Logf("online_view_00098 equivalent=yes windows=5/10/30/60/invalid tenants=2 boundary=strict window_calls new=%d old=%d alive_rows=%d", newCalls, oldCalls, aliveRows)
}
