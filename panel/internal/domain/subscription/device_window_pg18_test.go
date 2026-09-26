// [INPUT]: 依赖 delivery_pg18_test.go 的 openDeliveryPG18，依赖 nodefabric 的 ListNodeUsers / PurgeStaleAlive，依赖迁移 00094 的 app.device_limit_window_minutes 与 subscription_online_devices
// [OUTPUT]: 对外提供 TestDeviceWindowPG18
// [POS]: domain/subscription 的设备识别窗口 PG18 门禁（delivery 域）：同一批在线记录在 5 与 30 分钟窗口下在线数不同，strict 判定与下发给节点的用户跟着变；清理截止不删窗口内的行（R103）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

func TestDeviceWindowPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant  = "93000000-0000-4000-8000-000000000401"
		user    = "93000000-0000-4000-8000-000000000411"
		product = "93000000-0000-4000-8000-000000000421"
		plan    = "93000000-0000-4000-8000-000000000431"
		planVer = "93000000-0000-4000-8000-000000000441"
		pool    = "93000000-0000-4000-8000-000000000451"
		server  = "93000000-0000-4000-8000-000000000461"
		node    = "93000000-0000-4000-8000-000000000471"
		sub     = "93000000-0000-4000-8000-000000000481"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed device window fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'device-window-pg18','Device Window PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@device-window.invalid','Owner','active')`, tenant, user)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'device-window-product','Device Window Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'device-window','Device Window','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'device-window-plan','Device Window Plan','draft')`, tenant, product, plan)
	// 套餐只许一台设备
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by,max_devices) VALUES($3,$1,$2,1,$4,1)`, tenant, plan, planVer, user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'device-window-server','ready')`, tenant, server)
	must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
		  VALUES($2,$1,'device-window-node',$3,'active','vless','window.invalid',443,$4,'active',1,now(),now())`,
		tenant, node, pool, server)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, user, plan, planVer)
	// strict、宽容 0：在线设备数一超过 1 就摘掉订阅
	must(`INSERT INTO system_settings(tenant_id,key,value) VALUES($1,'device_limit.mode','"strict"'),($1,'device_limit.grace','0')`, tenant)
	// 同一批在线记录：一台刚上报，一台 20 分钟前上报
	must(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
		  VALUES($1,$2,$3,'\x01',now() - interval '1 minute'),($1,$2,$3,'\x02',now() - interval '20 minutes')`,
		tenant, node, sub)

	var nodeUID int64
	if err := admin.QueryRow(ctx, `SELECT node_uid FROM subscriptions WHERE id=$1`, sub).Scan(&nodeUID); err != nil {
		t.Fatal(err)
	}
	nodes := nodefabric.NewService(app, nil)
	poolID := pool
	state := func() (window, online int, delivered []int64) {
		t.Helper()
		// 运行时角色、带租户作用域读视图：与 strict 判定、后台与门户读到的是同一个数
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `
				SELECT app.device_limit_window_minutes($1),
				       coalesce((SELECT device_count FROM subscription_online_devices WHERE subscription_id=$2), 0)`,
				tenant, sub).Scan(&window, &online)
		}); err != nil {
			t.Fatalf("read online devices: %v", err)
		}
		users, err := nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: node, PoolID: &poolID})
		if err != nil {
			t.Fatalf("ListNodeUsers: %v", err)
		}
		delivered = []int64{}
		for _, u := range users {
			delivered = append(delivered, u.ID)
		}
		return
	}
	expect := func(step string, wantWindow, wantOnline int, wantDelivered bool) {
		t.Helper()
		window, online, delivered := state()
		want := []int64{}
		if wantDelivered {
			want = []int64{nodeUID}
		}
		if window != wantWindow || online != wantOnline || !slices.Equal(delivered, want) {
			t.Fatalf("%s: window=%d online=%d delivered=%v, want %d/%d/%v",
				step, window, online, delivered, wantWindow, wantOnline, want)
		}
	}

	// 缺行按 5：只算刚上报的那台，没超限，照常下发——与改动前一致
	expect("default", 5, 1, true)
	// 30 分钟：两台都算，超过 1 + 0，strict 把订阅从节点摘掉
	must(`INSERT INTO system_settings(tenant_id,key,value) VALUES($1,'device_limit.window_minutes','30')`, tenant)
	expect("30 minutes", 30, 2, false)
	// 10 分钟：20 分钟前那台又出了窗口，恢复下发
	must(`UPDATE system_settings SET value='10' WHERE tenant_id=$1 AND key='device_limit.window_minutes'`, tenant)
	expect("10 minutes", 10, 1, true)
	// 读不懂的值不放宽窗口，按 5
	must(`UPDATE system_settings SET value='7' WHERE tenant_id=$1 AND key='device_limit.window_minutes'`, tenant)
	expect("invalid", 5, 1, true)
	must(`UPDATE system_settings SET value='"thirty"' WHERE tenant_id=$1 AND key='device_limit.window_minutes'`, tenant)
	expect("non-numeric", 5, 1, true)
	// loose 模式下窗口只改在线数，不摘订阅
	must(`UPDATE system_settings SET value='60' WHERE tenant_id=$1 AND key='device_limit.window_minutes'`, tenant)
	must(`UPDATE system_settings SET value='"loose"' WHERE tenant_id=$1 AND key='device_limit.mode'`, tenant)
	expect("loose 60 minutes", 60, 2, true)

	// 清理截止不小于最大窗口：65 分钟前的行留着，75 分钟前的删掉
	must(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
		  VALUES($1,$2,$3,'\x03',now() - interval '65 minutes'),($1,$2,$3,'\x04',now() - interval '75 minutes')`,
		tenant, node, sub)
	purged, err := nodes.PurgeStaleAlive(ctx, tenant)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeStaleAlive purged=%d err=%v, want 1", purged, err)
	}
	var left []string
	rows, err := admin.Query(ctx, `SELECT encode(ip_hash,'hex') FROM node_alive_ips WHERE subscription_id=$1 ORDER BY 1`, sub)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		left = append(left, h)
	}
	rows.Close()
	if !slices.Equal(left, []string{"01", "02", "03"}) {
		t.Fatalf("alive rows after purge = %v, want 01 02 03", left)
	}
	t.Log("device_window_pg18 default=5 w5_online=1 w30_online=2 strict_follows_window=yes invalid=5 purge_cutoff=70m")
}
