// [INPUT]: 依赖 platform/pg18test 的一次性库护栏（run-pg18-gates.sh 的 delivery 域），依赖 nodefabric 的 ListNodeUsers 与本包的 ListNodes / ListOwnedNodePreviews
// [OUTPUT]: 对外提供 TestDeliverySetPG18、openDeliveryPG18
// [POS]: domain/subscription 的交付集合 PG18 门禁：同一份夹具上对照节点用户列表、订阅下载、门户预览三处下发口径（R104）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// openDeliveryPG18 打开 delivery 域的一次性库。api/admin 的同域测试有自己的一份
// 同参数调用（跨包不能共享测试辅助函数），两边的 Fixture 必须一字不差。
func openDeliveryPG18(t *testing.T) (context.Context, *pgxpool.Pool, *platformdb.Pool) {
	return pg18test.Open(t, pg18test.Fixture{
		Domain:         "DELIVERY",
		DatabasePrefix: "pandora_delivery_",
		MarkerTable:    "pandora_delivery_test_marker",
		CommentTag:     "pandora-delivery-pg18",
	})
}

// TestDeliverySetPG18 钉住「哪些用户能连哪些节点」在三处下发路径上是同一个答案：
// 节点拉用户（nodefabric.ListNodeUsers）、订阅下载（ListNodes）、门户节点预览
// （ListOwnedNodePreviews）。
//
//   - 没划进节点池的节点不服务任何订阅（R104，有意改变：过去它对所有有效订阅开放）；
//   - 划进了池、但池没绑到订阅的套餐版本的节点，同样一个人都不给；
//   - 划进了已绑定池的节点照旧下发——默认行为不变。
func TestDeliverySetPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant    = "93000000-0000-4000-8000-000000000001"
		user      = "93000000-0000-4000-8000-000000000011"
		product   = "93000000-0000-4000-8000-000000000021"
		plan      = "93000000-0000-4000-8000-000000000031"
		planVer   = "93000000-0000-4000-8000-000000000041"
		boundPool = "93000000-0000-4000-8000-000000000051"
		otherPool = "93000000-0000-4000-8000-000000000052"
		server    = "93000000-0000-4000-8000-000000000061"
		pooled    = "93000000-0000-4000-8000-000000000071"
		noPool    = "93000000-0000-4000-8000-000000000072"
		unbound   = "93000000-0000-4000-8000-000000000073"
		sub       = "93000000-0000-4000-8000-000000000081"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed delivery fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'delivery-set-pg18','Delivery Set PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@delivery.invalid','Owner','active')`, tenant, user)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'delivery-product','Delivery Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'bound','Bound','active')`, tenant, boundPool)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'other','Other','active')`, tenant, otherPool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'delivery-plan','Delivery Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, boundPool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'delivery-server','ready')`, tenant, server)
	// 三个节点除了 pool_id 完全一样：都可服务、协议稳定、见过心跳、有可连地址，
	// 任何一个被排除都只能是因为节点池。
	for _, node := range []struct {
		id, name, host string
		pool           any
	}{
		{pooled, "Pooled Node", "pooled.invalid", boundPool},
		{noPool, "No Pool Node", "nopool.invalid", nil},
		{unbound, "Unbound Pool Node", "unbound.invalid", otherPool},
	} {
		must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
			  VALUES($2,$1,$3,$3,$4,'active','vless',$5,443,$6,'active',1,now(),now())`,
			tenant, node.id, node.name, node.pool, node.host, server)
	}
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, user, plan, planVer)
	must(`INSERT INTO subscription_credentials
		(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,status)
		VALUES($1,$2,$3,decode(lpad('93',64,'0'),'hex'),'00000093','subscription','active')`, tenant, sub, user)

	var nodeUID int64
	if err := admin.QueryRow(ctx, `SELECT node_uid FROM subscriptions WHERE id=$1`, sub).Scan(&nodeUID); err != nil {
		t.Fatalf("read subscription node_uid: %v", err)
	}

	// 节点侧：每个节点拉到的用户。ServingNode 的 pool_id 取库里的真实值。
	nodes := nodefabric.NewService(app, nil)
	usersOf := func(nodeID string) []int64 {
		t.Helper()
		var poolID *string
		if err := admin.QueryRow(ctx, `SELECT pool_id::text FROM nodes WHERE id=$1`, nodeID).Scan(&poolID); err != nil {
			t.Fatalf("read node pool: %v", err)
		}
		users, err := nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: nodeID, PoolID: poolID})
		if err != nil {
			t.Fatalf("ListNodeUsers(%s): %v", nodeID, err)
		}
		ids := make([]int64, 0, len(users))
		for _, u := range users {
			ids = append(ids, u.ID)
		}
		return ids
	}
	if got := usersOf(pooled); !slices.Equal(got, []int64{nodeUID}) {
		t.Fatalf("pooled node users = %v, want [%d] (default delivery must not change)", got, nodeUID)
	}
	if got := usersOf(noPool); len(got) != 0 {
		t.Fatalf("no-pool node users = %v, want none (R104: no pool serves nobody)", got)
	}
	if got := usersOf(unbound); len(got) != 0 {
		t.Fatalf("unbound-pool node users = %v, want none", got)
	}

	// 订阅侧：下载与预览只含已绑定池里的那个节点。
	svc := New(app, nil, nil)
	download, err := svc.ListNodes(ctx, tenant, &Credential{PlanVersionID: planVer})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	previews, err := svc.ListOwnedNodePreviews(ctx, tenant, user, sub)
	if err != nil {
		t.Fatalf("ListOwnedNodePreviews: %v", err)
	}
	var downloadNames, previewNames []string
	for _, n := range download {
		downloadNames = append(downloadNames, n.Name)
	}
	for _, p := range previews {
		previewNames = append(previewNames, p.Name)
	}
	want := []string{"Pooled Node"}
	if !slices.Equal(downloadNames, want) || !slices.Equal(previewNames, want) {
		t.Fatalf("download=%v preview=%v, want both %v", downloadNames, previewNames, want)
	}

	// 把无池节点划进已绑定的池，它立刻对同一个用户下发——排除它的只是「没有池」。
	must(`UPDATE nodes SET pool_id=$2 WHERE id=$1`, noPool, boundPool)
	if got := usersOf(noPool); !slices.Equal(got, []int64{nodeUID}) {
		t.Fatalf("node users after joining the bound pool = %v, want [%d]", got, nodeUID)
	}
	t.Log("delivery_set_pg18 role=aegis_app no_pool=0 unbound_pool=0 bound_pool=1 download=preview=node_users")
}
