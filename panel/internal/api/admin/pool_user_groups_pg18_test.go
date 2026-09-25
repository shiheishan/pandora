// [INPUT]: 依赖 delivery_pg18_test.go 的 openDeliveryPG18 与 deliveryHarness，依赖 pools.go / pool_user_groups.go / usergroup.go 的处理器与 nodefabric.ListNodeUsers
// [OUTPUT]: 对外提供 TestPoolUserGroupsAdminPG18
// [POS]: api/admin 的节点池限定用户组 PG18 门禁（delivery 域）：名单读写的字段级 reauth、校验、审计与通知，删组被拒，换组后节点实际拉到的用户跟着变（R104）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

func TestPoolUserGroupsAdminPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant      = "93000000-0000-4000-8000-000000000201"
		otherTenant = "93000000-0000-4000-8000-000000000202"
		actor       = "93000000-0000-4000-8000-000000000211"
		member      = "93000000-0000-4000-8000-000000000212"
		product     = "93000000-0000-4000-8000-000000000221"
		plan        = "93000000-0000-4000-8000-000000000231"
		planVer     = "93000000-0000-4000-8000-000000000241"
		vipGroup    = "93000000-0000-4000-8000-000000000251"
		spareGroup  = "93000000-0000-4000-8000-000000000252"
		foreign     = "93000000-0000-4000-8000-000000000253"
		server      = "93000000-0000-4000-8000-000000000261"
		node        = "93000000-0000-4000-8000-000000000271"
		sub         = "93000000-0000-4000-8000-000000000281"
		missing     = "93000000-0000-4000-8000-000000000299"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed pool user group fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'pool-groups-pg18','Pool Groups PG18','USD')`, tenant)
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'pool-groups-other-pg18','Pool Groups Other','USD')`, otherTenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@pool-groups.invalid','Operator','active')`, tenant, actor)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'member@pool-groups.invalid','Member','active')`, tenant, member)
	must(`INSERT INTO user_groups(id,tenant_id,code,name,policy) VALUES($2,$1,'vip','内测','{}')`, tenant, vipGroup)
	must(`INSERT INTO user_groups(id,tenant_id,code,name,policy) VALUES($2,$1,'spare','备用','{}')`, tenant, spareGroup)
	must(`INSERT INTO user_groups(id,tenant_id,code,name,policy) VALUES($2,$1,'foreign','别家','{}')`, otherTenant, foreign)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'pool-groups-product','Pool Groups Product','active')`, tenant, product)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'pool-groups-plan','Pool Groups Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, actor)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'pool-groups-server','ready')`, tenant, server)

	d := newDeliveryHarness(t, ctx, app, tenant, actor, nil)
	decode := func(body []byte, into any) {
		t.Helper()
		if err := json.Unmarshal(body, into); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	errorOf := func(body []byte) (code string, fields map[string]string, msg string) {
		var env struct {
			Error struct {
				Code    string            `json:"code"`
				Message string            `json:"message"`
				Fields  map[string]string `json:"fields"`
			} `json:"error"`
		}
		decode(body, &env)
		return env.Error.Code, env.Error.Fields, env.Error.Message
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	auditCount := func(action string) int {
		return count(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenant, action)
	}

	// --- 新建：带名单要 reauth，没通过什么都不建 ---
	d.reauthed = false
	w := d.do(http.MethodPost, "/v1/node-pools", `{"name":"专属线路","allowed_user_group_ids":["`+vipGroup+`"]}`)
	if code, _, _ := errorOf(w.Body.Bytes()); w.Code != http.StatusForbidden || code != "reauth_required" {
		t.Fatalf("create with groups unreauthed = %d %s, want 403 reauth_required", w.Code, w.Body)
	}
	if n := count(`SELECT count(*) FROM node_pools WHERE tenant_id=$1`, tenant); n != 0 {
		t.Fatalf("unreauthed create left %d pools", n)
	}
	// 不带名单的新建照旧不用 reauth（交付集合不变），也不通知节点
	w = d.do(http.MethodPost, "/v1/node-pools", `{"name":"公共线路","code":"open"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create without groups unreauthed = %d %s", w.Code, w.Body)
	}
	if n := d.usersChanged(); n != 0 {
		t.Fatalf("plain pool create published %d node.users.changed", n)
	}
	d.reauthed = true
	w = d.do(http.MethodPost, "/v1/node-pools", `{"name":"专属线路","code":"vip","allowed_user_group_ids":["`+vipGroup+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create with groups = %d %s", w.Code, w.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(w.Body.Bytes(), &created)
	pool := created.ID
	if n := auditCount("node_pool.user_groups_changed"); n != 1 {
		t.Fatalf("group list audits after create = %d, want 1", n)
	}
	if n := d.usersChanged(); n != 1 {
		t.Fatalf("create with groups published %d node.users.changed, want 1", n)
	}

	// --- 列表两头都看得见这条限定 ---
	w = d.do(http.MethodGet, "/v1/node-pools", "")
	var pools struct {
		Pools []struct {
			ID      string     `json:"id"`
			Allowed []namedRef `json:"allowed_user_groups"`
		} `json:"pools"`
	}
	decode(w.Body.Bytes(), &pools)
	for _, p := range pools.Pools {
		want := []namedRef{}
		if p.ID == pool {
			want = []namedRef{{ID: vipGroup, Name: "内测"}}
		}
		if !slices.Equal(p.Allowed, want) {
			t.Fatalf("pool %s allowed_user_groups = %+v, want %+v", p.ID, p.Allowed, want)
		}
	}
	w = d.do(http.MethodGet, "/v1/user-groups", "")
	var groups struct {
		Groups []struct {
			ID        string     `json:"id"`
			Exclusive []namedRef `json:"exclusive_pools"`
		} `json:"groups"`
	}
	decode(w.Body.Bytes(), &groups)
	for _, g := range groups.Groups {
		want := []namedRef{}
		if g.ID == vipGroup {
			want = []namedRef{{ID: pool, Name: "专属线路"}}
		}
		if !slices.Equal(g.Exclusive, want) {
			t.Fatalf("group %s exclusive_pools = %+v, want %+v", g.ID, g.Exclusive, want)
		}
	}

	// --- 编辑：校验、不变不通知、只改名不要 reauth ---
	for name, ids := range map[string]string{
		"missing":      `["` + missing + `"]`,
		"cross-tenant": `["` + foreign + `"]`,
		"duplicate":    `["` + vipGroup + `","` + vipGroup + `"]`,
		"malformed":    `["not-a-uuid"]`,
	} {
		w = d.do(http.MethodPost, "/v1/node-pools/"+pool, `{"allowed_user_group_ids":`+ids+`}`)
		if code, fields, _ := errorOf(w.Body.Bytes()); w.Code != http.StatusUnprocessableEntity ||
			code != "validation_failed" || fields["allowed_user_group_ids"] == "" {
			t.Fatalf("%s group ids = %d %s, want 422 fields.allowed_user_group_ids", name, w.Code, w.Body)
		}
	}
	w = d.do(http.MethodPost, "/v1/node-pools/"+pool, `{"allowed_user_group_ids":["`+vipGroup+`"]}`)
	if w.Code != http.StatusOK || d.usersChanged() != 0 || auditCount("node_pool.user_groups_changed") != 1 {
		t.Fatalf("resubmitting the same list = %d %s; must not notify or audit a change", w.Code, w.Body)
	}
	d.reauthed = false
	if w = d.do(http.MethodPost, "/v1/node-pools/"+pool, `{"name":"专属线路 A"}`); w.Code != http.StatusOK {
		t.Fatalf("rename without groups unreauthed = %d %s", w.Code, w.Body)
	}
	if w = d.do(http.MethodPost, "/v1/node-pools/"+pool, `{"allowed_user_group_ids":[]}`); w.Code != http.StatusForbidden {
		t.Fatalf("clearing the list unreauthed = %d %s, want 403", w.Code, w.Body)
	}
	d.reauthed = true
	if n := count(`SELECT count(*) FROM node_pool_user_groups WHERE pool_id=$1`, pool); n != 1 {
		t.Fatalf("list rows after rejected edits = %d, want 1", n)
	}

	// --- 下发：只有名单内组的用户能从这个池的节点拿到线路 ---
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
		  VALUES($2,$1,'pool-groups-node',$3,'active','vless','vip.pool-groups.invalid',443,$4,'active',1,now(),now())`,
		tenant, node, pool, server)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, member, plan, planVer)
	nodeUsers := func() int {
		t.Helper()
		poolID := pool
		users, err := d.nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: node, PoolID: &poolID})
		if err != nil {
			t.Fatalf("ListNodeUsers: %v", err)
		}
		return len(users)
	}
	if n := nodeUsers(); n != 0 {
		t.Fatalf("default-group member gets %d users on a restricted pool, want 0", n)
	}
	if w = d.do(http.MethodPost, "/v1/users/"+member+"/group", `{"group_id":"`+vipGroup+`"}`); w.Code != http.StatusOK {
		t.Fatalf("assign group = %d %s", w.Code, w.Body)
	}
	if n := d.usersChanged(); n != 1 {
		t.Fatalf("assign group published %d node.users.changed, want 1", n)
	}
	if n := nodeUsers(); n != 1 {
		t.Fatalf("member in the listed group gets %d users, want 1", n)
	}
	if w = d.do(http.MethodPost, "/v1/users/"+member+"/group", `{"group_id":"`+spareGroup+`"}`); w.Code != http.StatusOK || d.usersChanged() != 1 {
		t.Fatalf("move to unlisted group = %d %s", w.Code, w.Body)
	}
	if n := nodeUsers(); n != 0 {
		t.Fatalf("member in an unlisted group gets %d users, want 0", n)
	}
	// 失败的换组不通知
	if w = d.do(http.MethodPost, "/v1/users/"+member+"/group", `{"group_id":"`+missing+`"}`); w.Code != http.StatusUnprocessableEntity || d.usersChanged() != 0 {
		t.Fatalf("assign unknown group = %d %s; must not notify", w.Code, w.Body)
	}

	// --- 删组：被名单引用就拒，消息写明池名 ---
	w = d.do(http.MethodDelete, "/v1/user-groups/"+vipGroup, "")
	if code, _, msg := errorOf(w.Body.Bytes()); w.Code != http.StatusConflict || code != "conflict" || !strings.Contains(msg, "「专属线路 A」") {
		t.Fatalf("delete referenced group = %d %s, want 409 naming the pool", w.Code, w.Body)
	}
	if n := count(`SELECT count(*) FROM user_groups WHERE id=$1`, vipGroup); n != 1 {
		t.Fatal("referenced group was deleted")
	}
	// 数据库兜底：绕过处理器直接删同样被外键挡住（00093）
	if _, err := admin.Exec(ctx, `DELETE FROM user_groups WHERE id=$1`, vipGroup); err == nil {
		t.Fatal("foreign key allowed deleting a group still on a pool list")
	}

	// --- 取消限定：池对所有订阅了套餐的人开放，组随后可删 ---
	if w = d.do(http.MethodPost, "/v1/node-pools/"+pool, `{"allowed_user_group_ids":[]}`); w.Code != http.StatusOK {
		t.Fatalf("clear list = %d %s", w.Code, w.Body)
	}
	if n := d.usersChanged(); n != 1 || auditCount("node_pool.user_groups_changed") != 2 {
		t.Fatalf("clearing the list published %d events / %d audits, want 1 / 2", n, auditCount("node_pool.user_groups_changed"))
	}
	if n := nodeUsers(); n != 1 {
		t.Fatalf("unrestricted pool gives %d users, want 1", n)
	}
	if w = d.do(http.MethodDelete, "/v1/user-groups/"+vipGroup, ""); w.Code != http.StatusOK {
		t.Fatalf("delete unreferenced group = %d %s", w.Code, w.Body)
	}
	t.Log("pool_user_groups_admin_pg18 reauth=field-level audit=on-change notify=on-change delete=409+fk assign=delivery")
}
