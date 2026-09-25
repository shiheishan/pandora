// [INPUT]: 依赖 delivery_pg18_test.go 的 openDeliveryPG18，依赖 nodefabric 的 ListNodeUsers 与本包的 ListNodes / ListOwnedNodePreviews，依赖迁移 00093 的 node_pool_user_groups
// [OUTPUT]: 对外提供 TestPoolUserGroupsDeliveryPG18
// [POS]: domain/subscription 的节点池限定用户组 PG18 门禁（delivery 域）：同一份夹具上按用户对照节点用户列表、订阅下载、门户预览三处下发（R104）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// TestPoolUserGroupsDeliveryPG18 钉住 R104 的规则在三处下发路径上是同一个答案：
// 用户能用某个池 = 套餐版本绑定了它，且它没限定用户组或用户所在的组在名单里。
//
//   - 没配任何限定时，三个用户（名单组、别的组、默认组）拿到的节点完全一样——默认行为不变；
//   - 限定之后，限定池只下发给名单内组的用户，别的组与默认组都拿不到，未限定的池照旧；
//   - 用户换进名单内的组，立刻拿到限定池。
func TestPoolUserGroupsDeliveryPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant    = "93000000-0000-4000-8000-000000000301"
		vipUser   = "93000000-0000-4000-8000-000000000311"
		otherUser = "93000000-0000-4000-8000-000000000312"
		plainUser = "93000000-0000-4000-8000-000000000313"
		vipGroup  = "93000000-0000-4000-8000-000000000321"
		otherGrp  = "93000000-0000-4000-8000-000000000322"
		product   = "93000000-0000-4000-8000-000000000331"
		plan      = "93000000-0000-4000-8000-000000000341"
		planVer   = "93000000-0000-4000-8000-000000000351"
		openPool  = "93000000-0000-4000-8000-000000000361"
		vipPool   = "93000000-0000-4000-8000-000000000362"
		server    = "93000000-0000-4000-8000-000000000371"
		openNode  = "93000000-0000-4000-8000-000000000381"
		vipNode   = "93000000-0000-4000-8000-000000000382"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed pool user group delivery fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'pool-groups-delivery-pg18','Pool Groups Delivery','USD')`, tenant)
	must(`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'vip','VIP')`, tenant, vipGroup)
	must(`INSERT INTO user_groups(id,tenant_id,code,name) VALUES($2,$1,'other','Other')`, tenant, otherGrp)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id) VALUES($2,$1,'vip@pool-groups.invalid','VIP','active',$3)`, tenant, vipUser, vipGroup)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status,user_group_id) VALUES($2,$1,'other@pool-groups.invalid','Other','active',$3)`, tenant, otherUser, otherGrp)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'plain@pool-groups.invalid','Plain','active')`, tenant, plainUser)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'pool-groups-product','Pool Groups Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'open','Open','active')`, tenant, openPool)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'vip','VIP Lines','active')`, tenant, vipPool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'pool-groups-plan','Pool Groups Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, vipUser)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3),($1,$2,$4)`, tenant, planVer, openPool, vipPool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'pool-groups-server','ready')`, tenant, server)
	for _, n := range []struct{ id, name, host, pool string }{
		{openNode, "Open Node", "open.pool-groups.invalid", openPool},
		{vipNode, "VIP Node", "vip.pool-groups.invalid", vipPool},
	} {
		must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,sort_order)
			  VALUES($2,$1,$3,$3,$4,'active','vless',$5,443,$6,'active',1,now(),now(),0)`,
			tenant, n.id, n.name, n.pool, n.host, server)
	}

	type holder struct {
		user, sub string
		uid       int64
	}
	holders := map[string]*holder{
		"vip":   {user: vipUser, sub: "93000000-0000-4000-8000-000000000391"},
		"other": {user: otherUser, sub: "93000000-0000-4000-8000-000000000392"},
		"plain": {user: plainUser, sub: "93000000-0000-4000-8000-000000000393"},
	}
	for i, key := range []string{"vip", "other", "plain"} {
		h := holders[key]
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
			  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, h.sub, tenant, h.user, plan, planVer)
		must(`INSERT INTO subscription_credentials
			(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,status)
			VALUES($1,$2,$3,decode(lpad($4,64,'0'),'hex'),right(lpad($4,8,'0'),8),'subscription','active')`,
			tenant, h.sub, h.user, "93"+string(rune('1'+i)))
		if err := admin.QueryRow(ctx, `SELECT node_uid FROM subscriptions WHERE id=$1`, h.sub).Scan(&h.uid); err != nil {
			t.Fatalf("read node_uid: %v", err)
		}
	}

	nodes := nodefabric.NewService(app, nil)
	svc := New(app, nil, nil)
	nodeUsers := func(nodeID, poolID string) []int64 {
		t.Helper()
		users, err := nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: nodeID, PoolID: &poolID})
		if err != nil {
			t.Fatalf("ListNodeUsers(%s): %v", nodeID, err)
		}
		ids := []int64{}
		for _, u := range users {
			ids = append(ids, u.ID)
		}
		slices.Sort(ids)
		return ids
	}
	uids := func(keys ...string) []int64 {
		out := []int64{}
		for _, k := range keys {
			out = append(out, holders[k].uid)
		}
		slices.Sort(out)
		return out
	}
	// subscriberView 返回某个用户经订阅下载与门户预览拿到的节点名；两处必须一致。
	subscriberView := func(key string) []string {
		t.Helper()
		h := holders[key]
		download, err := svc.ListNodes(ctx, tenant, &Credential{UserID: h.user, PlanVersionID: planVer})
		if err != nil {
			t.Fatalf("ListNodes(%s): %v", key, err)
		}
		previews, err := svc.ListOwnedNodePreviews(ctx, tenant, h.user, h.sub)
		if err != nil {
			t.Fatalf("ListOwnedNodePreviews(%s): %v", key, err)
		}
		var a, b []string
		for _, n := range download {
			a = append(a, n.Name)
		}
		for _, p := range previews {
			b = append(b, p.Name)
		}
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Fatalf("%s download=%v preview=%v disagree", key, a, b)
		}
		return a
	}
	expect := func(step string, vipNodeUsers []int64, views map[string][]string) {
		t.Helper()
		if got := nodeUsers(openNode, openPool); !slices.Equal(got, uids("vip", "other", "plain")) {
			t.Fatalf("%s: open node users = %v, want every subscriber", step, got)
		}
		if got := nodeUsers(vipNode, vipPool); !slices.Equal(got, vipNodeUsers) {
			t.Fatalf("%s: vip node users = %v, want %v", step, got, vipNodeUsers)
		}
		for key, want := range views {
			if got := subscriberView(key); !slices.Equal(got, want) {
				t.Fatalf("%s: %s sees %v, want %v", step, key, got, want)
			}
		}
	}
	both := []string{"Open Node", "VIP Node"}
	openOnly := []string{"Open Node"}

	// 没有任何限定：三个人拿到的完全一样，与改动前一致
	expect("unrestricted", uids("vip", "other", "plain"),
		map[string][]string{"vip": both, "other": both, "plain": both})

	// 限定 VIP 池只给 VIP 组
	must(`INSERT INTO node_pool_user_groups(tenant_id,pool_id,user_group_id) VALUES($1,$2,$3)`, tenant, vipPool, vipGroup)
	expect("restricted", uids("vip"),
		map[string][]string{"vip": both, "other": openOnly, "plain": openOnly})

	// 名单里再加一个组：那个组的人也进来，默认组仍然进不来
	must(`INSERT INTO node_pool_user_groups(tenant_id,pool_id,user_group_id) VALUES($1,$2,$3)`, tenant, vipPool, otherGrp)
	expect("two groups", uids("vip", "other"),
		map[string][]string{"vip": both, "other": both, "plain": openOnly})

	// 默认组用户换进名单内的组，立刻拿到限定池
	must(`UPDATE users SET user_group_id=$2 WHERE id=$1`, plainUser, vipGroup)
	expect("moved in", uids("vip", "other", "plain"),
		map[string][]string{"vip": both, "other": both, "plain": both})

	// 删掉名单行 = 取消限定，回到所有人都能用
	must(`UPDATE users SET user_group_id=NULL WHERE id=$1`, plainUser)
	must(`DELETE FROM node_pool_user_groups WHERE pool_id=$1`, vipPool)
	expect("cleared", uids("vip", "other", "plain"),
		map[string][]string{"vip": both, "other": both, "plain": both})

	// 名单表与其他租户表一样开了 FORCE RLS（00093 经 app.enable_tenant_rls）
	var forced bool
	if err := app.QueryRow(ctx, `SELECT relrowsecurity AND relforcerowsecurity FROM pg_class
		WHERE oid='node_pool_user_groups'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("node_pool_user_groups FORCE RLS forced=%v err=%v", forced, err)
	}
	t.Log("pool_user_groups_delivery_pg18 unrestricted=unchanged restricted=listed-groups-only default_group=excluded download=preview=node_users rls=forced")
}
