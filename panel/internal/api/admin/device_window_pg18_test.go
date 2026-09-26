// [INPUT]: 依赖 delivery_pg18_test.go 的 openDeliveryPG18 与 deliveryHarness，依赖 devices.go 的 listOnlineDevices / setDeviceMode、nodes.go 的 nodeList，依赖迁移 00094 的 app.device_limit_window_minutes
// [OUTPUT]: 对外提供 TestDeviceWindowAdminPG18
// [POS]: api/admin 的设备识别窗口 PG18 门禁（delivery 域）：窗口写入与审计、设备概览回显窗口、设备概览与节点列表的在线统计按同一窗口变化（R103）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDeviceWindowAdminPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant  = "93000000-0000-4000-8000-000000000501"
		actor   = "93000000-0000-4000-8000-000000000511"
		owner   = "93000000-0000-4000-8000-000000000512"
		product = "93000000-0000-4000-8000-000000000521"
		plan    = "93000000-0000-4000-8000-000000000531"
		planVer = "93000000-0000-4000-8000-000000000541"
		pool    = "93000000-0000-4000-8000-000000000551"
		server  = "93000000-0000-4000-8000-000000000561"
		node    = "93000000-0000-4000-8000-000000000571"
		sub     = "93000000-0000-4000-8000-000000000581"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed device window admin fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'device-window-admin-pg18','Device Window Admin','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@device-window-admin.invalid','Operator','active')`, tenant, actor)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@device-window-admin.invalid','Owner','active')`, tenant, owner)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'dwa-product','DWA Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'dwa','DWA','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'dwa-plan','DWA Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by,max_devices) VALUES($3,$1,$2,1,$4,1)`, tenant, plan, planVer, actor)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'dwa-server','ready')`, tenant, server)
	must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
		  VALUES($2,$1,'dwa-node',$3,'active','vless','dwa.invalid',443,$4,'active',1,now(),now())`,
		tenant, node, pool, server)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, owner, plan, planVer)
	must(`INSERT INTO node_alive_ips(tenant_id,node_id,subscription_id,ip_hash,last_seen_at)
		  VALUES($1,$2,$3,'\x01',now() - interval '1 minute'),($1,$2,$3,'\x02',now() - interval '20 minutes')`,
		tenant, node, sub)

	d := newDeliveryHarness(t, ctx, app, tenant, actor, nil)
	type devicesResp struct {
		Devices []struct {
			SubscriptionID string `json:"subscription_id"`
			Online         int    `json:"online"`
			Exceeded       bool   `json:"exceeded"`
		} `json:"devices"`
		Mode   string `json:"mode"`
		Window int    `json:"window_minutes"`
	}
	read := func(step string) (window, devicesOnline, nodeUsers, nodeIPs int, exceeded bool) {
		t.Helper()
		w := d.do(http.MethodGet, "/v1/devices", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: GET v1/devices = %d %s", step, w.Code, w.Body)
		}
		var dv devicesResp
		if err := json.Unmarshal(w.Body.Bytes(), &dv); err != nil {
			t.Fatal(err)
		}
		for _, row := range dv.Devices {
			if row.SubscriptionID == sub {
				devicesOnline, exceeded = row.Online, row.Exceeded
			}
		}
		w = d.do(http.MethodGet, "/v1/nodes", "")
		var nl struct {
			Nodes []struct {
				ID          string `json:"id"`
				OnlineUsers int    `json:"online_users"`
				OnlineIPs   int    `json:"online_ips"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &nl); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s: GET v1/nodes = %d %s", step, w.Code, w.Body)
		}
		for _, n := range nl.Nodes {
			if n.ID == node {
				nodeUsers, nodeIPs = n.OnlineUsers, n.OnlineIPs
			}
		}
		return dv.Window, devicesOnline, nodeUsers, nodeIPs, exceeded
	}
	windowAudits := func() int {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE tenant_id=$1 AND action='device_limit.mode_changed'
			  AND after_digest->>'window_minutes' IS NOT NULL`, tenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 缺行：窗口回显 5，概览与节点列表都只算刚上报的那台——与改动前一致
	if window, online, users, ips, exceeded := read("default"); window != 5 || online != 1 || users != 1 || ips != 1 || exceeded {
		t.Fatalf("default window=%d online=%d node_users=%d node_ips=%d exceeded=%v", window, online, users, ips, exceeded)
	}
	// 非法窗口 422，什么都不写
	if w := d.do(http.MethodPost, "/v1/settings/device-limit", `{"mode":"strict","grace":0,"window_minutes":15}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("window 15 = %d %s, want 422", w.Code, w.Body)
	}
	// 改成 30 分钟：两台都算，strict + 宽容 0 下超限；节点列表同一口径
	if w := d.do(http.MethodPost, "/v1/settings/device-limit", `{"mode":"strict","grace":0,"window_minutes":30}`); w.Code != http.StatusOK {
		t.Fatalf("set window 30 = %d %s", w.Code, w.Body)
	}
	if window, online, users, ips, exceeded := read("30 minutes"); window != 30 || online != 2 || users != 1 || ips != 2 || !exceeded {
		t.Fatalf("30 min window=%d online=%d node_users=%d node_ips=%d exceeded=%v", window, online, users, ips, exceeded)
	}
	if n := windowAudits(); n != 1 {
		t.Fatalf("window change audits = %d, want 1", n)
	}
	var before string
	if err := admin.QueryRow(ctx, `SELECT before_digest->>'window_minutes' FROM audit_events
		WHERE tenant_id=$1 AND action='device_limit.mode_changed' AND before_digest IS NOT NULL
		ORDER BY occurred_at DESC LIMIT 1`, tenant).Scan(&before); err != nil || before != "5" {
		t.Fatalf("audit before window = %q err=%v, want 5", before, err)
	}
	// 省略 window_minutes = 不改
	if w := d.do(http.MethodPost, "/v1/settings/device-limit", `{"mode":"loose"}`); w.Code != http.StatusOK {
		t.Fatalf("set mode only = %d %s", w.Code, w.Body)
	}
	if window, online, _, _, exceeded := read("mode only"); window != 30 || online != 2 || exceeded != true {
		// exceeded 只看 limit + grace，与模式无关
		t.Fatalf("mode-only change touched the window: window=%d online=%d exceeded=%v", window, online, exceeded)
	}
	t.Log("device_window_admin_pg18 default=5 invalid=422 w30=devices+node_list reauth=route audit=before/after omitted=unchanged")
}
