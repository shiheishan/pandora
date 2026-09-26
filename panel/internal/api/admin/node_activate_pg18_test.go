// [INPUT]: 依赖 delivery_pg18_test.go 的 openDeliveryPG18 与 deliveryHarness，依赖 node_admin.go 的 nodeActivate、nodefabric 的 ActivateNode / ListNodeUsers、subscription 的 ListNodes，依赖 00005 的 node_transitions 与状态机触发器
// [OUTPUT]: 对外提供 TestNodeActivatePG18
// [POS]: api/admin 的一步上线 PG18 门禁（delivery 域）：新服务器 + 新接入节点调一次即被下发用户、已上线幂等、每种前置条件 409 且不留痕、状态机触发器逐步生效（R108、R110）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

func TestNodeActivatePG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant   = "93000000-0000-4000-8000-000000000601"
		actor    = "93000000-0000-4000-8000-000000000611"
		owner    = "93000000-0000-4000-8000-000000000612"
		product  = "93000000-0000-4000-8000-000000000621"
		plan     = "93000000-0000-4000-8000-000000000631"
		planVer  = "93000000-0000-4000-8000-000000000641"
		pool     = "93000000-0000-4000-8000-000000000651"
		freshSrv = "93000000-0000-4000-8000-000000000661"
		badSrv   = "93000000-0000-4000-8000-000000000662"
		sub      = "93000000-0000-4000-8000-000000000681"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed node activate fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'node-activate-pg18','Node Activate PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@node-activate.invalid','Operator','active')`, tenant, actor)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@node-activate.invalid','Owner','active')`, tenant, owner)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'activate-product','Activate Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'activate','Activate','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'activate-plan','Activate Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, actor)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, owner, plan, planVer)
	// 两台新服务器：一台草稿（接入刚建出来的样子），一台已隔离（进不了 ready）
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'activate-fresh','draft')`, tenant, freshSrv)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'activate-quarantined','quarantined')`, tenant, badSrv)

	// mkNode 造一个接入到某个阶段的节点：identity 为 active / expired / 空，
	// ready 表示协议已校验（接入之后管理员保存过协议）
	serial := 0
	mkNode := func(n int, status string, server any, identity string, ready bool) string {
		t.Helper()
		id := "93000000-0000-4000-8000-0000000007" + strconv.Itoa(10+n)
		validated := "now()"
		if !ready {
			validated = "NULL"
		}
		must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
				server_id,protocol_schema_version,config_validated_at,last_heartbeat_at)
			  VALUES($2,$1,$3,$4,$5,'vless',$3 || '.invalid',443,$6,1,`+validated+`,now())`,
			tenant, id, "activate-node-"+strconv.Itoa(n), pool, status, server)
		if identity != "" {
			serial++
			expires := "now() + interval '90 days'"
			if identity == "expired" {
				expires = "now() - interval '1 minute'"
			}
			must(`INSERT INTO node_identities(tenant_id,node_id,serial,public_key,spiffe_id,fingerprint,expires_at)
				  VALUES($1,$2,1,'\x01',$3,$4,`+expires+`)`,
				tenant, id, "spiffe://aegis/test/"+id, []byte("activate-fp-"+strconv.Itoa(serial)))
		}
		return id
	}
	fresh := mkNode(0, "attesting", freshSrv, "active", true)
	must(`UPDATE servers SET control_node_id=$2 WHERE id=$1`, freshSrv, fresh)

	d := newDeliveryHarness(t, ctx, app, tenant, actor, nil)
	errMsg := func(body map[string]any) string {
		env, _ := body["error"].(map[string]any)
		msg, _ := env["message"].(string)
		return msg
	}

	rowVersion := func(id string) int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	activate := func(id string, version int64) (int, map[string]any) {
		t.Helper()
		w := d.do(http.MethodPost, "/v1/nodes/"+id+"/activate", `{"row_version":`+strconv.FormatInt(version, 10)+`}`)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	stateOf := func(id string) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, `SELECT n.status||'/'||n.serving_status||'/'||coalesce(sv.status,'-')
			FROM nodes n LEFT JOIN servers sv ON sv.id=n.server_id WHERE n.id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	auditCount := func() int {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.activate'`, tenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	nodeUsers := func(id string) int {
		t.Helper()
		p := pool
		users, err := d.nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: id, PoolID: &p})
		if err != nil {
			t.Fatalf("ListNodeUsers: %v", err)
		}
		return len(users)
	}
	drainEvents := func() (configChanged, usersChanged int) {
		for {
			select {
			case ev := <-d.events:
				switch ev.Topic {
				case realtime.TopicNodeConfigChanged:
					configChanged++
				case realtime.TopicNodeUsersChanged:
					usersChanged++
				}
			default:
				return
			}
		}
	}

	// --- 新服务器 + 新接入节点：调一次就开始下发 ---
	if n := nodeUsers(fresh); n != 0 {
		t.Fatalf("attesting node already delivers %d users", n)
	}
	code, body := activate(fresh, rowVersion(fresh))
	if code != http.StatusOK || body["status"] != "active" || body["serving_status"] != "active" || body["id"] != fresh {
		t.Fatalf("activate fresh node = %d %v", code, body)
	}
	if _, has := body["warnings"]; has {
		t.Fatalf("pooled, plan-bound node must not carry warnings: %v", body["warnings"])
	}
	if got := stateOf(fresh); got != "active/active/ready" {
		t.Fatalf("fresh node state = %s, want active/active/ready", got)
	}
	if n := nodeUsers(fresh); n != 1 {
		t.Fatalf("activated node delivers %d users, want 1", n)
	}
	download, err := subscription.New(app, nil, nil).ListNodes(ctx, tenant,
		&subscription.Credential{UserID: owner, PlanVersionID: planVer})
	if err != nil || len(download) != 1 || download[0].Name != "activate-node-0" {
		t.Fatalf("subscription download after activate = %+v err=%v", download, err)
	}
	if cfg, users := drainEvents(); cfg != 1 || users != 1 || auditCount() != 1 {
		t.Fatalf("activate published config=%d users=%d audits=%d, want 1/1/1", cfg, users, auditCount())
	}

	// --- 已上线：幂等，陈旧的版本号也回 200，不改不审计不通知 ---
	before := rowVersion(fresh)
	if code, body := activate(fresh, 1); code != http.StatusOK || body["status"] != "active" {
		t.Fatalf("activate active node = %d %v", code, body)
	}
	if rowVersion(fresh) != before || auditCount() != 1 {
		t.Fatal("idempotent activate changed the node or wrote an audit")
	}
	if cfg, users := drainEvents(); cfg+users != 0 {
		t.Fatalf("idempotent activate published %d events", cfg+users)
	}

	// --- 第二个节点上同一台已 ready 的服务器：服务器不动，只通知这个节点 ---
	second := mkNode(1, "canary", freshSrv, "active", true)
	if code, body := activate(second, rowVersion(second)); code != http.StatusOK || body["status"] != "active" {
		t.Fatalf("activate second node = %d %v", code, body)
	}
	if cfg, users := drainEvents(); cfg != 1 || users != 0 {
		t.Fatalf("second activate published config=%d users=%d, want 1/0", cfg, users)
	}

	// --- 前置条件：每种都 409、写明原因、什么都不改 ---
	for _, tc := range []struct {
		name, id, want string
		version        int64
	}{
		{"version", mkNode(2, "attesting", freshSrv, "active", true), "已被其他管理员修改", 99},
		{"no identity", mkNode(3, "attesting", freshSrv, "", true), "节点身份", 0},
		{"expired identity", mkNode(4, "validating", freshSrv, "expired", true), "节点身份", 0},
		{"protocol", mkNode(5, "standby", freshSrv, "active", false), "协议配置", 0},
		{"no server", mkNode(6, "attesting", nil, "active", true), "没有绑定服务器", 0},
		{"server quarantined", mkNode(7, "attesting", badSrv, "active", true), "服务器处于 quarantined", 0},
		{"enrolling", mkNode(8, "bootstrapping", freshSrv, "active", true), "还没完成接入", 0},
		{"failed", mkNode(9, "bootstrap_failed", freshSrv, "active", true), "bootstrap_failed", 0},
		{"quarantined", mkNode(10, "quarantined", freshSrv, "active", true), "quarantined", 0},
		{"draining", mkNode(11, "draining", freshSrv, "active", true), "不在接入尾段", 0},
	} {
		version := tc.version
		if version == 0 {
			version = rowVersion(tc.id)
		}
		stateBefore, audits := stateOf(tc.id), auditCount()
		code, body := activate(tc.id, version)
		msg := errMsg(body)
		if code != http.StatusConflict || !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: activate = %d %v, want 409 containing %q", tc.name, code, body, tc.want)
		}
		if stateOf(tc.id) != stateBefore || auditCount() != audits {
			t.Fatalf("%s: refused activate changed state %s -> %s", tc.name, stateBefore, stateOf(tc.id))
		}
		if cfg, users := drainEvents(); cfg+users != 0 {
			t.Fatalf("%s: refused activate published %d events", tc.name, cfg+users)
		}
	}
	if code, _ := activate("93000000-0000-4000-8000-000000000799", 1); code != http.StatusNotFound {
		t.Fatalf("unknown node = %d, want 404", code)
	}

	// --- 状态机触发器逐步生效：临时抽掉 standby → canary 这条边，上线就在那一步被拒、整体回滚 ---
	stepped := mkNode(12, "attesting", freshSrv, "active", true)
	must(`DELETE FROM node_transitions WHERE from_status='standby' AND to_status='canary'`)
	restored := false
	restore := func() {
		if !restored {
			must(`INSERT INTO node_transitions(from_status,to_status) VALUES('standby','canary')`)
			restored = true
		}
	}
	t.Cleanup(restore)
	code, body = activate(stepped, rowVersion(stepped))
	msg := errMsg(body)
	if code != http.StatusConflict || !strings.Contains(msg, "非法状态跳转") || !strings.Contains(msg, "standby -> canary") {
		t.Fatalf("activate across a missing edge = %d %v, want trigger 409", code, body)
	}
	if got := stateOf(stepped); got != "attesting/draft/ready" {
		t.Fatalf("trigger refusal left state %s, want rollback to attesting", got)
	}
	restore()
	if code, body := activate(stepped, rowVersion(stepped)); code != http.StatusOK || body["status"] != "active" {
		t.Fatalf("activate after restoring the edge = %d %v", code, body)
	}

	// --- 无池节点能上线，但带 R105 的提示 ---
	loose := mkNode(13, "canary", freshSrv, "active", true)
	must(`UPDATE nodes SET pool_id=NULL WHERE id=$1`, loose)
	code, body = activate(loose, rowVersion(loose))
	warnings, _ := body["warnings"].([]any)
	if code != http.StatusOK || len(warnings) != 1 || warnings[0] != "未划入节点池，不服务任何用户" {
		t.Fatalf("activate pool-less node = %d %v, want warning", code, body)
	}
	t.Log("node_activate_pg18 fresh_server=ready delivered=1 idempotent=yes preconditions=10x409 trigger=per-step warnings=R105")
}
