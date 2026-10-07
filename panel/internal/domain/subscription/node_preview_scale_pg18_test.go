package subscription

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// checkNodePreviewScales：门户节点预览走缓存后，命中时的耗时与节点数无关（200 → 1000
// 近似不变）；未命中现查不取协议配置。日志给出三组数字：未命中一次、命中均值、以及改前
// 每次都要做的「带协议配置的全量资格查询」均值，供报告对照。
func checkNodePreviewScales(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *platformdb.Pool) {
	const (
		tenant  = "c19c0000-0000-4000-8000-000000000001"
		user    = "c19c0000-0000-4000-8000-000000000011"
		product = "c19c0000-0000-4000-8000-000000000021"
		plan    = "c19c0000-0000-4000-8000-000000000031"
		planVer = "c19c0000-0000-4000-8000-000000000041"
		pool    = "c19c0000-0000-4000-8000-000000000051"
		server  = "c19c0000-0000-4000-8000-000000000061"
		sub     = "c19c0000-0000-4000-8000-000000000081"
		hits    = 50
		legacy  = 10
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed node preview scale fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'node-preview-scale','Node Preview Scale','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@node-preview-scale.invalid','Owner','active')`, tenant, user)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'scale-product','Scale Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'scale','Scale','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'scale-plan','Scale Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'scale-server','ready')`, tenant, server)
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
	      VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, user, plan, planVer)
	must(`INSERT INTO subscription_credentials
	        (tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,status)
	      VALUES($1,$2,$3,decode(lpad('c19c',64,'0'),'hex'),'0000c19c','subscription','active')`, tenant, sub, user)

	// 节点带一份约 2 KB 的协议配置：改前每次预览都要读出并反序列化它
	addNodes := func(from, to int) {
		must(`INSERT INTO nodes(tenant_id,name,pool_id,status,node_type,server_host,server_port,traffic_rate,
		        server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,protocol_config)
		      SELECT $1, 'scale-node-' || g, $2, 'active', 'vless', 'scale-' || g || '.invalid',
		             10000 + g, 1.0, $3, 'active', 1, now(), now(),
		             jsonb_build_object('flow', 'xtls-rprx-vision', 'padding', repeat('x', 2000), 'n', g)
		        FROM generate_series($4::int, $5::int) g`, tenant, pool, server, from, to)
		must(`ANALYZE nodes`)
	}

	measure := func(want int) (miss, hit, full time.Duration) {
		t.Helper()
		svc := New(app, nil, nil)
		start := time.Now()
		got, err := svc.ListOwnedNodePreviews(ctx, tenant, user, sub)
		miss = time.Since(start)
		if err != nil || len(got) != want {
			t.Fatalf("preview with %d nodes: got %d err=%v", want, len(got), err)
		}
		start = time.Now()
		for range hits {
			if got, err = svc.ListOwnedNodePreviews(ctx, tenant, user, sub); err != nil || len(got) != want {
				t.Fatalf("cached preview with %d nodes: got %d err=%v", want, len(got), err)
			}
		}
		hit = time.Since(start) / hits
		start = time.Now()
		for range legacy {
			var nodes []Node
			if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant, ActorID: user}, func(tx pgx.Tx) error {
				var err error
				nodes, err = listEligibleNodesTx(ctx, tx, tenant, user, planVer, true)
				return err
			}); err != nil || len(nodes) != want {
				t.Fatalf("full eligibility with %d nodes: got %d err=%v", want, len(nodes), err)
			}
		}
		full = time.Since(start) / legacy
		return miss, hit, full
	}

	addNodes(1, 200)
	miss200, hit200, full200 := measure(200)
	addNodes(201, 1000)
	miss1000, hit1000, full1000 := measure(1000)
	t.Logf("门户节点预览 200 节点：未命中 %s，命中均值 %s；改前（带协议配置全量现查）均值 %s", miss200, hit200, full200)
	t.Logf("门户节点预览 1000 节点：未命中 %s，命中均值 %s；改前（带协议配置全量现查）均值 %s", miss1000, hit1000, full1000)
	// 命中只剩一次归属查询：比改前每次都做的全量查询便宜（宽松判定，避免 CI 抖动误报）
	if hit1000 >= full1000 {
		t.Fatalf("cached preview (%s) is not cheaper than the uncached full query (%s) at 1000 nodes", hit1000, full1000)
	}
}
