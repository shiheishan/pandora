package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// runDeliverableAdminPG18 接着 TestDeliveryAdminPG18 的夹具（池已绑到发布版本与草稿），
// 证明套餐页与节点列表都按订阅资格的同一个片段（subscription.DeliverableNodeSQL）说话：
//
//   - 服务器没就绪的节点：节点列表报「所属服务器未就绪」，套餐页不计入可下发节点数；
//   - 池没绑任何套餐的节点：节点列表报「所在节点池没有绑定任何套餐」，节点拉不到用户；
//   - 套餐页某池的可下发节点数 = 订阅下载里的节点数，active_nodes 照旧更宽。
func runDeliverableAdminPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *platformdb.Pool,
	d *deliveryHarness, tenant, owner, plan, published, pool, pooled string) {
	const (
		draftServer  = "93000000-0000-4000-8000-000000000191"
		otherPool    = "93000000-0000-4000-8000-000000000192"
		onDraftSrv   = "93000000-0000-4000-8000-000000000193"
		inOtherPool  = "93000000-0000-4000-8000-000000000194"
		readyServer  = "93000000-0000-4000-8000-000000000161"
		wantPoolName = "Delivery Admin Pool"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed deliverable admin fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'delivery-admin-draft-server','draft')`, tenant, draftServer)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'delivery-admin-other','Delivery Admin Other','active')`, tenant, otherPool)
	// readyServer 上已有 TestDeliveryAdminPG18 的节点（443、8443）：端口错开，免撞同机端口门禁的唯一索引（00122）
	for _, node := range []struct {
		id, name, pool, server string
		port                   int
	}{
		{onDraftSrv, "delivery-admin-draft-server-node", pool, draftServer, 443},
		{inOtherPool, "delivery-admin-unbound-pool-node", otherPool, readyServer, 9443},
	} {
		must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
			  VALUES($2,$1,$3,$4,'active','vless',$5,$7,$6,'active',1,now(),now())`,
			tenant, node.id, node.name, node.pool, node.name+".invalid", node.server, node.port)
	}

	// 节点列表：两条新说明，原有两条不变
	w := d.do(http.MethodGet, "/v1/nodes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET v1/nodes = %d %s", w.Code, w.Body)
	}
	var list struct {
		Nodes []struct {
			ID           string `json:"id"`
			Delivered    bool   `json:"delivered_to_users"`
			DeliveryNote string `json:"delivery_note"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode node list: %v", err)
	}
	want := map[string]struct {
		delivered bool
		note      string
	}{
		pooled:      {true, ""},
		onDraftSrv:  {false, subscription.ServerNotReadyNote},
		inOtherPool: {false, subscription.PoolUnboundNote},
	}
	for _, n := range list.Nodes {
		if exp, ok := want[n.ID]; ok {
			if n.Delivered != exp.delivered || n.DeliveryNote != exp.note {
				t.Fatalf("node %s delivered=%v note=%q, want %v %q", n.ID, n.Delivered, n.DeliveryNote, exp.delivered, exp.note)
			}
			delete(want, n.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("node list missing %v", want)
	}
	other := otherPool
	if users, err := d.nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: inOtherPool, PoolID: &other}); err != nil || len(users) != 0 {
		t.Fatalf("unbound-pool node users = %d err=%v, want none", len(users), err)
	}

	// 套餐页：可下发节点数与订阅下载一致
	bindings, err := adminops.NewService(app).PlanPools(ctx, tenant, plan)
	if err != nil {
		t.Fatalf("PlanPools: %v", err)
	}
	download, err := subscription.New(app, nil, nil).ListNodes(ctx, tenant, &subscription.Credential{UserID: owner, PlanVersionID: published})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var found bool
	for _, p := range bindings.Pools {
		if p.ID != pool {
			continue
		}
		found = true
		if !p.Bound || p.Name != wantPoolName || p.Active != 2 || p.Deliverable != 1 || p.Deliverable != len(download) {
			t.Fatalf("plan pool %+v, download=%d; want bound, active=2, deliverable=1=download", p, len(download))
		}
	}
	if !found {
		t.Fatalf("plan pools missing the bound pool: %+v", bindings.Pools)
	}
	t.Log("deliverable_admin_pg18 node_list=refined plan_deliverable=download active_nodes=wider")
}
