package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// legacyEligibleNodesTx 是抽出 DeliverableNodeSQL 之前 listEligibleNodesTx 的原样
// 拷贝（基点 66a2043），只给对照用：抽取前后同一份夹具上谁能拿到哪些节点必须一字不差。
// 唯一有意的差别是地址列：原先 inet::text 带掩码，这里与新代码一样用 host()，只比
// 「谁被下发」，地址的修正由下面单独断言。
func legacyEligibleNodesTx(ctx context.Context, tx pgx.Tx, tenantID, userID, planVersionID string) ([]Node, error) {
	out := []Node{}
	rows, err := tx.Query(ctx, `
			SELECT COALESCE(NULLIF(n.display_name, ''), n.name),
			       n.node_type,
			       COALESCE(NULLIF(n.server_host, ''), host(n.public_ipv4), n.hostname, ''),
			       COALESCE(n.server_port, 0),
			       COALESCE(n.protocol_config, '{}'::jsonb),
			       COALESCE(n.traffic_rate, 1.0),
			       (n.last_heartbeat_at >= now() - $3::interval) AS heartbeat_fresh
			  FROM nodes n
			  JOIN servers s
			    ON s.id = n.server_id AND s.tenant_id = n.tenant_id
			  JOIN plan_node_pools p
			    ON p.pool_id = n.pool_id AND p.tenant_id = n.tenant_id
			 WHERE n.tenant_id = $1
			   AND p.plan_version_id = $2::uuid
			   AND (s.control_node_id IS DISTINCT FROM n.id OR n.status = 'active')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND s.status = 'ready' AND s.deleted_at IS NULL
			   AND n.serving_status = 'active'
			   AND n.last_heartbeat_at IS NOT NULL
			   AND `+nodefabric.StableProtocolReadySQL("n")+`
			   AND `+nodefabric.PoolAdmitsUserSQL("n.tenant_id", "n.pool_id", "$4::uuid")+`
			 ORDER BY n.sort_order, n.id`,
		tenantID, planVersionID, HeartbeatFreshWindow.String(), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n Node
		var raw []byte
		if err := rows.Scan(&n.Name, &n.Type, &n.Host, &n.Port, &raw, &n.TrafficRate, &n.HeartbeatFresh); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &n.Config)
		}
		if n.Host == "" || n.Port == 0 {
			continue
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return preferFreshNodes(out), nil
}

// runDeliverableExtractionPG18 证明抽出 DeliverableNodeSQL 只是搬家：
//
//  1. 每个节点只违反一条资格条件，新旧查询在「有新鲜节点」「全部超时」两种心跳形态下
//     下发的节点名单完全相同，且与手算的期望一致；
//  2. 套餐页按 DeliverableNodeSQL 数出的某池可下发节点数，等于全部超时（保底给全）时
//     订阅里该池的节点数；后台 NodeDeliverability 对每个节点的判定与订阅是否含它一致；
//  3. 没填 server_host 的节点回落到 host(public_ipv4)，订阅地址不带 /32。
func runDeliverableExtractionPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *platformdb.Pool) {
	const (
		tenant   = "93000000-0000-4000-8000-000000000801"
		user     = "93000000-0000-4000-8000-000000000811"
		product  = "93000000-0000-4000-8000-000000000821"
		plan     = "93000000-0000-4000-8000-000000000831"
		planVer  = "93000000-0000-4000-8000-000000000841"
		pool     = "93000000-0000-4000-8000-000000000851"
		unbound  = "93000000-0000-4000-8000-000000000852"
		ready    = "93000000-0000-4000-8000-000000000861"
		draftSrv = "93000000-0000-4000-8000-000000000862"
		ctlBad   = "93000000-0000-4000-8000-000000000863"
		ctlOK    = "93000000-0000-4000-8000-000000000864"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed deliverable fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'deliverable-pg18','Deliverable PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@deliverable.invalid','Owner','active')`, tenant, user)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'deliverable-product','Deliverable Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'deliverable-bound','Bound','active')`, tenant, pool)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'deliverable-unbound','Unbound','active')`, tenant, unbound)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'deliverable-plan','Deliverable Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	for _, srv := range []struct{ id, name, status string }{
		{ready, "deliverable-ready", "ready"},
		{draftSrv, "deliverable-draft", "draft"},
		{ctlBad, "deliverable-ctl-bad", "ready"},
		{ctlOK, "deliverable-ctl-ok", "ready"},
	} {
		must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,$3,$4)`, tenant, srv.id, srv.name, srv.status)
	}

	// 每个节点从同一个「能下发」的基线出发，只改一列。期望 want 是手算的结论。
	// 夹具 id 用 8xx 段：delivery 域各用例共用一个库，段不能与别的用例重叠。
	type variant struct {
		id, name string
		want     bool
		pool     any
		server   any
		status   string
		serving  string
		nodeType any
		host     any
		ipv4     any
		port     any
		schema   int
		valid    bool
		beat     string // SQL 表达式；空串表示 NULL（从未心跳）
	}
	base := func(id, name string, want bool) variant {
		return variant{id: id, name: name, want: want, pool: pool, server: ready, status: "active", serving: "active",
			nodeType: "vless", host: name + ".deliverable.invalid", port: 443, schema: 1, valid: true, beat: "now()"}
	}
	id := func(n int) string { return fmt.Sprintf("93000000-0000-4000-8000-000000000%03d", 870+n) }
	variants := []variant{
		base(id(1), "ok", true),
		func() variant { v := base(id(2), "stale", true); v.beat = "now() - interval '1 day'"; return v }(),
		func() variant { v := base(id(3), "never-beat", false); v.beat = ""; return v }(),
		func() variant { v := base(id(4), "serving-disabled", false); v.serving = "disabled"; return v }(),
		func() variant { v := base(id(5), "no-type", false); v.nodeType = nil; return v }(),
		func() variant { v := base(id(6), "no-port", false); v.port = nil; return v }(),
		func() variant { v := base(id(7), "draft-server", false); v.server = draftSrv; return v }(),
		func() variant { v := base(id(8), "no-server", false); v.server = nil; return v }(),
		func() variant { v := base(id(9), "no-host", false); v.host = nil; return v }(),
		func() variant {
			v := base(id(10), "ctl-not-active", false)
			v.server = ctlBad
			v.status = "draft"
			return v
		}(),
		func() variant { v := base(id(11), "ctl-active", true); v.server = ctlOK; return v }(),
		func() variant { v := base(id(12), "unvalidated", false); v.valid = false; return v }(),
		func() variant { v := base(id(13), "schema-zero", false); v.schema = 0; return v }(),
		func() variant { v := base(id(14), "unbound-pool", false); v.pool = unbound; return v }(),
		func() variant { v := base(id(15), "no-pool", false); v.pool = nil; return v }(),
		func() variant {
			v := base(id(16), "ipv4-fallback", true)
			v.host = nil
			v.ipv4 = "203.0.113.7"
			return v
		}(),
	}
	for i, v := range variants {
		beat := "NULL"
		if v.beat != "" {
			beat = v.beat
		}
		must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,public_ipv4,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,sort_order)
			  VALUES($2,$1,$3,$3,$4,$5,$6,$7,$8::inet,$9,$10,$11,$12,CASE WHEN $13 THEN now() END,`+beat+`,$14)`,
			tenant, v.id, v.name, v.pool, v.status, v.nodeType, v.host, v.ipv4, v.port, v.server, v.serving, v.schema, v.valid, i)
	}
	must(`UPDATE servers SET control_node_id=$2 WHERE tenant_id=$1 AND id=$3`, tenant, id(10), ctlBad)
	must(`UPDATE servers SET control_node_id=$2 WHERE tenant_id=$1 AND id=$3`, tenant, id(11), ctlOK)

	names := func(nodes []Node) []string {
		out := make([]string, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, n.Name)
		}
		return out
	}
	compare := func(label string, want []string) []Node {
		t.Helper()
		var got, legacy []Node
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant, ActorID: user}, func(tx pgx.Tx) error {
			var err error
			if got, err = listEligibleNodesTx(ctx, tx, tenant, user, planVer, true); err != nil {
				return err
			}
			legacy, err = legacyEligibleNodesTx(ctx, tx, tenant, user, planVer)
			return err
		}); err != nil {
			t.Fatalf("%s: eligibility queries: %v", label, err)
		}
		if !slices.Equal(names(got), names(legacy)) || !slices.Equal(names(got), want) {
			t.Fatalf("%s: extracted=%v legacy=%v want=%v", label, names(got), names(legacy), want)
		}
		return got
	}

	// 有新鲜节点时超时的那个不给（preferFreshNodes），新旧一致
	fresh := compare("fresh", []string{"ok", "ctl-active", "ipv4-fallback"})
	for _, n := range fresh {
		if n.Name == "ipv4-fallback" && n.Host != "203.0.113.7" {
			t.Fatalf("ipv4 fallback host = %q, want 203.0.113.7 without the /32 mask", n.Host)
		}
	}

	// 全部超时：保底把能下发的全给出去，这就是「可下发节点数」的口径
	must(`UPDATE nodes SET last_heartbeat_at = now() - interval '1 day'
	       WHERE tenant_id=$1 AND last_heartbeat_at IS NOT NULL`, tenant)
	all := compare("all-stale", []string{"ok", "stale", "ctl-active", "ipv4-fallback"})

	var counted int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM nodes n
		  JOIN servers s ON s.id = n.server_id AND s.tenant_id = n.tenant_id
		 WHERE n.tenant_id=$1 AND n.pool_id=$2 AND `+DeliverableNodeSQL(), tenant, pool).Scan(&counted); err != nil {
		t.Fatalf("count deliverable nodes: %v", err)
	}
	if counted != len(all) {
		t.Fatalf("deliverable count = %d, subscription carries %d nodes", counted, len(all))
	}

	ids := make([]string, 0, len(variants))
	for _, v := range variants {
		ids = append(ids, v.id)
	}
	facts, err := NodeDeliverability(ctx, app, tenant, ids)
	if err != nil {
		t.Fatalf("NodeDeliverability: %v", err)
	}
	inSubscription := map[string]bool{}
	for _, n := range all {
		inSubscription[n.Name] = true
	}
	for _, v := range variants {
		f, ok := facts[v.id]
		if !ok {
			t.Fatalf("NodeDeliverability missed node %s", v.name)
		}
		// 订阅里有它 ⇔ 节点自身可下发且池绑了这个套餐（本夹具只有一个套餐，无用户组限定）
		if got := f.Deliverable && f.PoolBound; got != inSubscription[v.name] || got != v.want {
			t.Fatalf("node %s deliverable=%v pool_bound=%v, subscription=%v want=%v",
				v.name, f.Deliverable, f.PoolBound, inSubscription[v.name], v.want)
		}
	}
	if f := facts[id(7)]; f.ServerReady || f.Deliverable {
		t.Fatalf("draft-server facts = %+v, want server not ready", f)
	}
	if f := facts[id(14)]; !f.Deliverable || f.PoolBound {
		t.Fatalf("unbound-pool facts = %+v, want deliverable but pool unbound", f)
	}
	t.Log("deliverable_extraction_pg18 variants=16 legacy=extracted fresh=3 all_stale=4 count=subscription ipv4_host=no_mask")
}
