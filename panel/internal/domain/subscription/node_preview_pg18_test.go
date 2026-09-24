// [INPUT]: 依赖 platform/db，依赖一次性 PG18 库（run-pg18-gates.sh 的 node_preview 域）
// [OUTPUT]: 对外提供 TestNodePreviewPG18
// [POS]: domain/subscription 的 PG18 集成测试：订阅可拿到的节点集合与门户节点预览的资格规则
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

const nodePreviewPG18Fixture = "disposable-v1"

func TestNodePreviewPG18(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_NODE_PREVIEW_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_NODE_PREVIEW_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_NODE_PREVIEW_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_NODE_PREVIEW_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_NODE_PREVIEW_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("node preview PostgreSQL 18 fixture is not configured")
	}
	if fixture != nodePreviewPG18Fixture || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_node_preview_") ||
		!regexp.MustCompile(`^[a-z0-9]{32}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", expectedDatabase, runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture administrator pool: %v", err)
	}
	defer admin.Close()
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open real aegis_app pool: %v", err)
	}
	defer app.Close()

	var database, marker, comment string
	var version int
	if err := app.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.pandora_node_preview_test_marker), shobj_description(oid,'pg_database')
		FROM pg_database WHERE datname=current_database()`).Scan(&database, &version, &marker, &comment); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	if database != expectedDatabase || version/10000 != 18 || marker != runID || comment != "pandora-node-preview-pg18:"+runID {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q comment=%q", database, version, marker, comment)
	}

	const (
		tenantA    = "74000000-0000-4000-8000-000000000001"
		tenantB    = "74000000-0000-4000-8000-000000000002"
		userA      = "74000000-0000-4000-8000-000000000011"
		outsiderA  = "74000000-0000-4000-8000-000000000012"
		userB      = "74000000-0000-4000-8000-000000000013"
		productA   = "74000000-0000-4000-8000-000000000021"
		planA      = "74000000-0000-4000-8000-000000000031"
		planVerA   = "74000000-0000-4000-8000-000000000041"
		poolA      = "74000000-0000-4000-8000-000000000051"
		otherPool  = "74000000-0000-4000-8000-000000000052"
		serverA    = "74000000-0000-4000-8000-000000000061"
		serverDown = "74000000-0000-4000-8000-000000000062"
		goodNode   = "74000000-0000-4000-8000-000000000071"
		subNever   = "74000000-0000-4000-8000-000000000081"
		subFuture  = "74000000-0000-4000-8000-000000000082"
		subGrace   = "74000000-0000-4000-8000-000000000083"
		subBadNull = "74000000-0000-4000-8000-000000000084"
		subExpired = "74000000-0000-4000-8000-000000000085"
		subState   = "74000000-0000-4000-8000-000000000086"
	)

	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'node-preview-a','Node Preview A','USD')`, []any{tenantA}},
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'node-preview-b','Node Preview B','USD')`, []any{tenantB}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@node.invalid','Owner','active')`, []any{tenantA, userA}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'outsider@node.invalid','Outsider','active')`, []any{tenantA, outsiderA}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'other@node.invalid','Other','active')`, []any{tenantB, userB}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'node-product','Node Product','active')`, []any{tenantA, productA}},
		{`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'eligible','Eligible','active')`, []any{tenantA, poolA}},
		{`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'other','Other','active')`, []any{tenantA, otherPool}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'node-plan','Node Plan','draft')`, []any{tenantA, productA, planA}},
		{`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, []any{tenantA, planA, planVerA, userA}},
		{`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, []any{tenantA, planVerA, poolA}},
		{`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, []any{tenantA, planVerA}},
		{`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, []any{tenantA, planVerA, planA}},
		{`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'ready-server','ready')`, []any{tenantA, serverA}},
		{`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'down-server','maintenance')`, []any{tenantA, serverDown}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,traffic_rate,
			server_id,serving_status,protocol_schema_version,config_validated_at,display_name)
		  VALUES($2,$1,'good-node',$3,'active','vless','good.invalid',443,1.50,$4,'active',1,now(),'Good Node')`, []any{tenantA, goodNode, poolA, serverA}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000072',$1,'disabled-node',$2,'active','vless','disabled.invalid',443,$3,'disabled',1,now())`, []any{tenantA, poolA, serverA}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000073',$1,'maintenance-node',$2,'active','vless','maintenance.invalid',443,$3,'active',1,now())`, []any{tenantA, poolA, serverDown}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000074',$1,'other-pool-node',$2,'active','vless','other.invalid',443,$3,'active',1,now())`, []any{tenantA, otherPool, serverA}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000075',$1,'schema-zero-node',$2,'active','vless','schema0.invalid',443,$3,'active',0,now())`, []any{tenantA, poolA, serverA}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000076',$1,'unvalidated-node',$2,'active','vless','unvalidated.invalid',443,$3,'active',1,NULL)`, []any{tenantA, poolA, serverA}},
		{`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000077',$1,'empty-host-node',$2,'active','vless','',443,$3,'active',1,now())`, []any{tenantA, poolA, serverA}},
	}
	for _, row := range seed {
		if _, err := admin.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed node preview fixture: %v\nSQL: %s", err, row.sql)
		}
	}
	// 下发规则要求节点至少上报过一次心跳（listEligibleNodesTx 的
	// last_heartbeat_at IS NOT NULL）。上面每个反例都该只因自己那一条被排除，
	// 所以先让它们全部「见过」；从没心跳过的情形单独用 never-seen-node 验。
	if _, err := admin.Exec(ctx, `UPDATE nodes SET last_heartbeat_at = now() WHERE tenant_id = $1`, tenantA); err != nil {
		t.Fatalf("mark fixture nodes as seen: %v", err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at)
		  VALUES('74000000-0000-4000-8000-000000000078',$1,'never-seen-node',$2,'active','vless','neverseen.invalid',443,$3,'active',1,now())`,
		tenantA, poolA, serverA); err != nil {
		t.Fatalf("seed never-seen node: %v", err)
	}

	for _, item := range []struct {
		id     string
		status string
	}{
		{subNever, "active"}, {subFuture, "active"}, {subGrace, "active"},
		{subBadNull, "active"}, {subExpired, "active"}, {subState, "expired"},
	} {
		if _, err := admin.Exec(ctx, `INSERT INTO subscriptions
			(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
			VALUES($1,$2,$3,$4,$5,$6,'USD',100)`, item.id, tenantA, userA, planA, planVerA, item.status); err != nil {
			t.Fatalf("seed subscription %s: %v", item.id, err)
		}
	}
	credentialSQL := `INSERT INTO subscription_credentials
		(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,status,expires_at,grace_until)
		VALUES($1,$2,$3,decode(lpad($4,64,'0'),'hex'),right($4,8),'subscription','active',$5,$6)`
	now := time.Now().UTC()
	for _, item := range []struct {
		subID              string
		hash               string
		expiresAt, graceAt *time.Time
	}{
		{subNever, "1", nil, nil},
		{subFuture, "2", ptrTime(now.Add(time.Hour)), nil},
		{subGrace, "3", ptrTime(now.Add(-time.Hour)), ptrTime(now.Add(time.Hour))},
		{subBadNull, "4", nil, ptrTime(now.Add(-time.Hour))},
		{subExpired, "5", ptrTime(now.Add(-2 * time.Hour)), ptrTime(now.Add(-time.Hour))},
		{subState, "6", ptrTime(now.Add(time.Hour)), nil},
	} {
		if _, err := admin.Exec(ctx, credentialSQL, tenantA, item.subID, userA, item.hash, item.expiresAt, item.graceAt); err != nil {
			t.Fatalf("seed credential %s: %v", item.subID, err)
		}
	}

	service := New(app, nil, nil)
	credentialNodes, err := service.ListNodes(ctx, tenantA, &Credential{PlanVersionID: planVerA})
	if err != nil || len(credentialNodes) != 1 || credentialNodes[0].Name != "Good Node" {
		t.Fatalf("subscription eligibility mismatch nodes=%+v err=%v", credentialNodes, err)
	}
	for _, subID := range []string{subNever, subFuture, subGrace} {
		previews, err := service.ListOwnedNodePreviews(ctx, tenantA, userA, subID)
		if err != nil || len(previews) != 1 || previews[0].Name != credentialNodes[0].Name ||
			previews[0].Protocol != credentialNodes[0].Type || previews[0].TrafficRate != credentialNodes[0].TrafficRate {
			t.Fatalf("preview %s mismatch previews=%+v nodes=%+v err=%v", subID, previews, credentialNodes, err)
		}
	}
	for name, input := range map[string]struct{ tenant, user, sub string }{
		"null-expired-grace": {tenantA, userA, subBadNull},
		"both-expired":       {tenantA, userA, subExpired},
		"expired-state":      {tenantA, userA, subState},
		"outsider":           {tenantA, outsiderA, subNever},
		"cross-tenant":       {tenantB, userB, subNever},
		"unknown":            {tenantA, userA, "74000000-0000-4000-8000-000000000099"},
	} {
		if got, err := service.ListOwnedNodePreviews(ctx, input.tenant, input.user, input.sub); !errors.Is(err, ErrNotFound) || len(got) != 0 {
			t.Fatalf("%s preview = %+v err=%v, want neutral not found", name, got, err)
		}
	}

	var forced bool
	if err := app.QueryRow(ctx, `SELECT bool_and(relrowsecurity AND relforcerowsecurity)
		FROM pg_class WHERE oid IN ('subscriptions'::regclass,'subscription_credentials'::regclass,
		'nodes'::regclass,'servers'::regclass,'plan_node_pools'::regclass)`).Scan(&forced); err != nil || !forced {
		t.Fatalf("node preview FORCE RLS proof failed forced=%v err=%v", forced, err)
	}
	t.Log("node_preview_pg18_business=ok role=aegis_app rls=on schema=40 expiry=5/5 owner=ok neutral=outsider,cross-tenant,unknown eligible=1")
}

func ptrTime(value time.Time) *time.Time { return &value }
