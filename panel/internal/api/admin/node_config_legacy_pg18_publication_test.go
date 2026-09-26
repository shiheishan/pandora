// [INPUT]: 依赖 domain/nodefabric 的 PublishConfig / FetchConfig / ReportConfigApplied / GetAdminNode，依赖 node_config_legacy_pg18_test.go 的夹具与共用发布、种数据、断言工具
// [OUTPUT]: 包内提供 runNodeConfigPG18PublicationLimitBatch 与并发发布任务类型 nodeConfigPG18PublishJob / nodeConfigPG18PublishResult（主文件的 runNodeConfigPG18Publishes 用它们）
// [POS]: TestNodeConfigLegacyPG18 的发布批次：并发发布与投影一致、租户与 RLS 隔离、上报归属（错作用域、被取代、池迁移、旧版本重复只追加）、过期 / 重复 / 畸形层拒绝与版本上限
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type nodeConfigPG18PublishJob struct {
	tenant string
	index  int
	input  nodefabric.PublishInput
}

type nodeConfigPG18PublishResult struct {
	job nodeConfigPG18PublishJob
	out *nodefabric.PublishOutput
	err error
}

func runNodeConfigPG18PublicationLimitBatch(t *testing.T, ctx context.Context, admin *pgx.Conn,
	appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	service := nodefabric.NewService(appPool, signer)

	t.Run("concurrent publication preserves projections", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		targetNode := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19001, "target")
		samePoolNode := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19002, "same-pool")
		outsidePool := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,region,status)
			VALUES($1,$2,$3,$4,'test','active')`, outsidePool, fx.tenant,
			"outside-"+fx.suffix, "Outside "+fx.suffix); err != nil {
			t.Fatalf("seed outside pool: %v", err)
		}
		outsideNode := createNodeConfigPG18Node(t, ctx, service, fx, outsidePool, 19003, "outside")

		var base, configsBefore, auditsBefore int
		if err := admin.QueryRow(ctx, `SELECT
			coalesce(max(version),0), count(*),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish')
			FROM node_configs WHERE tenant_id=$1`, fx.tenant).Scan(&base, &configsBefore, &auditsBefore); err != nil {
			t.Fatalf("read publication baseline: %v", err)
		}
		jobs := make([]nodeConfigPG18PublishJob, 0, 100)
		for i := 1; i <= 100; i++ {
			in := nodefabric.PublishInput{ActorID: fx.actor}
			switch i % 3 {
			case 0:
				in.Scope = "global"
				in.Payload = json.RawMessage(fmt.Sprintf(`{"global_seq":%d,"winner":"global"}`, i))
			case 1:
				in.Scope, in.ScopeRef = "pool", fx.pool
				in.Payload = json.RawMessage(fmt.Sprintf(`{"pool_seq":%d,"winner":"pool"}`, i))
			default:
				in.Scope, in.ScopeRef = "node", targetNode
				in.Payload = json.RawMessage(fmt.Sprintf(`{"node_seq":%d,"winner":"node"}`, i))
			}
			jobs = append(jobs, nodeConfigPG18PublishJob{tenant: fx.tenant, index: i, input: in})
		}
		results := runNodeConfigPG18Publishes(ctx, service, jobs)
		if len(results) != len(jobs) {
			t.Fatalf("publish result count=%d want=%d", len(results), len(jobs))
		}
		versions := make([]int, 0, len(results))
		ids := map[string]bool{}
		latest := map[string]nodeConfigPG18PublishResult{}
		for _, result := range results {
			if result.err != nil || result.out == nil {
				t.Fatalf("publish job %d failed: %v", result.job.index, result.err)
			}
			if result.out.ConfigID == "" || ids[result.out.ConfigID] {
				t.Fatalf("publish job %d returned duplicate/empty config id %q", result.job.index, result.out.ConfigID)
			}
			ids[result.out.ConfigID] = true
			versions = append(versions, result.out.Version)
			wantAffected := map[string]int{"global": 3, "pool": 2, "node": 1}[result.out.Scope]
			if result.out.AffectedNodes != wantAffected {
				t.Fatalf("publish job %d scope=%s affected=%d want=%d", result.job.index,
					result.out.Scope, result.out.AffectedNodes, wantAffected)
			}
			if old, ok := latest[result.out.Scope]; !ok || result.out.Version > old.out.Version {
				latest[result.out.Scope] = result
			}
		}
		sort.Ints(versions)
		for i, version := range versions {
			if version != base+i+1 {
				t.Fatalf("version[%d]=%d want=%d", i, version, base+i+1)
			}
		}
		var configs, distinctVersions, published, superseded, audits int
		if err := admin.QueryRow(ctx, `SELECT count(*),count(DISTINCT version),
			count(*) FILTER (WHERE status='published'),count(*) FILTER (WHERE status='superseded'),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish')
			FROM node_configs WHERE tenant_id=$1`, fx.tenant).
			Scan(&configs, &distinctVersions, &published, &superseded, &audits); err != nil {
			t.Fatalf("verify publication rows: %v", err)
		}
		if configs != configsBefore+100 || distinctVersions != 100 || published != 3 || superseded != 97 || audits != auditsBefore+100 {
			t.Fatalf("publication totals configs=%d distinct=%d published=%d superseded=%d audits=%d",
				configs, distinctVersions, published, superseded, audits)
		}
		for scope, result := range latest {
			var publishedID string
			if err := admin.QueryRow(ctx, `SELECT id::text FROM node_configs
				WHERE tenant_id=$1 AND scope=$2 AND scope_ref IS NOT DISTINCT FROM nullif($3,'')::uuid
				AND status='published'`, fx.tenant, scope, result.job.input.ScopeRef).Scan(&publishedID); err != nil {
				t.Fatalf("read published %s config: %v", scope, err)
			}
			if publishedID != result.out.ConfigID {
				t.Fatalf("published %s config=%s want=%s", scope, publishedID, result.out.ConfigID)
			}
		}
		t.Log("marker=node_config_pg18_publish_concurrency_ok")

		global, pool, node := latest["global"], latest["pool"], latest["node"]
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, targetNode,
			max(global.out.Version, pool.out.Version, node.out.Version),
			[]string{fmt.Sprintf("global@v%d", global.out.Version), fmt.Sprintf("pool@v%d", pool.out.Version), fmt.Sprintf("node@v%d", node.out.Version)},
			"node", map[string]int{"global_seq": global.job.index, "pool_seq": pool.job.index, "node_seq": node.job.index})
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, samePoolNode,
			max(global.out.Version, pool.out.Version),
			[]string{fmt.Sprintf("global@v%d", global.out.Version), fmt.Sprintf("pool@v%d", pool.out.Version)},
			"pool", map[string]int{"global_seq": global.job.index, "pool_seq": pool.job.index})
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, outsideNode,
			global.out.Version, []string{fmt.Sprintf("global@v%d", global.out.Version)},
			"global", map[string]int{"global_seq": global.job.index})
		t.Log("marker=node_config_pg18_projection_consistency_ok")
	})

	t.Run("tenant publication locks and RLS are isolated", func(t *testing.T) {
		fxA, fxB := seedNodeConfigPG18Fixture(t, ctx, admin), seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeA := createNodeConfigPG18Node(t, ctx, service, fxA, fxA.pool, 19101, "tenant-a")
		nodeB := createNodeConfigPG18Node(t, ctx, service, fxB, fxB.pool, 19102, "tenant-b")
		conn, err := appPool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire tenant lock probe: %v", err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			conn.Release()
			t.Fatalf("begin tenant lock probe: %v", err)
		}
		released := false
		defer func() {
			if !released {
				_ = tx.Rollback(context.Background())
				conn.Release()
			}
		}()
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.actor_id',$2,true)`, fxA.tenant, fxA.actor); err != nil {
			t.Fatalf("scope tenant lock probe: %v", err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(
			pg_catalog.hashtextextended('node-config-release/' || $1,0))`, fxA.tenant); err != nil {
			t.Fatalf("hold tenant A publication lock: %v", err)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		probeB, err := service.PublishConfig(probeCtx, fxB.tenant, nodefabric.PublishInput{
			ActorID: fxB.actor, Scope: "global", Payload: json.RawMessage(`{"tenant":"b","seq":1,"winner":"global"}`),
		})
		cancel()
		if err != nil || probeB == nil || probeB.Version != 1 {
			t.Fatalf("tenant B publish while A lock held: out=%+v err=%v", probeB, err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("release tenant A lock: %v", err)
		}
		conn.Release()
		released = true

		jobs := make([]nodeConfigPG18PublishJob, 0, 99)
		for i := 1; i <= 50; i++ {
			jobs = append(jobs, nodeConfigPG18PublishJob{tenant: fxA.tenant, index: i, input: nodefabric.PublishInput{
				ActorID: fxA.actor, Scope: "global", Payload: json.RawMessage(fmt.Sprintf(`{"tenant":"a","seq":%d,"winner":"global"}`, i)),
			}})
		}
		for i := 2; i <= 50; i++ {
			jobs = append(jobs, nodeConfigPG18PublishJob{tenant: fxB.tenant, index: i, input: nodefabric.PublishInput{
				ActorID: fxB.actor, Scope: "global", Payload: json.RawMessage(fmt.Sprintf(`{"tenant":"b","seq":%d,"winner":"global"}`, i)),
			}})
		}
		results := runNodeConfigPG18Publishes(ctx, service, jobs)
		if len(results) != len(jobs) {
			t.Fatalf("tenant publish result count=%d want=%d", len(results), len(jobs))
		}
		versions := map[string][]int{fxA.tenant: {}, fxB.tenant: {1}}
		configID := map[string]string{fxB.tenant: probeB.ConfigID}
		latestTenant := map[string]nodeConfigPG18PublishResult{
			fxB.tenant: {
				job: nodeConfigPG18PublishJob{tenant: fxB.tenant, index: 1},
				out: probeB,
			},
		}
		for _, result := range results {
			if result.err != nil || result.out == nil {
				t.Fatalf("tenant publish %s/%d failed: %v", result.job.tenant, result.job.index, result.err)
			}
			versions[result.job.tenant] = append(versions[result.job.tenant], result.out.Version)
			configID[result.job.tenant] = result.out.ConfigID
			if old, ok := latestTenant[result.job.tenant]; !ok || result.out.Version > old.out.Version {
				latestTenant[result.job.tenant] = result
			}
		}
		for _, fx := range []nodeConfigPG18Fixture{fxA, fxB} {
			sort.Ints(versions[fx.tenant])
			for i, version := range versions[fx.tenant] {
				if version != i+1 {
					t.Fatalf("tenant %s version[%d]=%d want=%d", fx.suffix, i, version, i+1)
				}
			}
			var configs, published, audits int
			if err := admin.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='published'),
				(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish')
				FROM node_configs WHERE tenant_id=$1`, fx.tenant).Scan(&configs, &published, &audits); err != nil {
				t.Fatalf("verify tenant %s totals: %v", fx.suffix, err)
			}
			if configs != 50 || published != 1 || audits != 50 {
				t.Fatalf("tenant %s configs=%d published=%d audits=%d", fx.suffix, configs, published, audits)
			}
		}
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fxA.tenant, nodeA, 50,
			[]string{"global@v50"}, "global", map[string]int{"seq": latestTenant[fxA.tenant].job.index})
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fxB.tenant, nodeB, 50,
			[]string{"global@v50"}, "global", map[string]int{"seq": latestTenant[fxB.tenant].job.index})
		assertNodeConfigPG18CrossTenantInvisible(t, ctx, appPool, fxA, nodeB, configID[fxB.tenant])
		assertNodeConfigPG18CrossTenantInvisible(t, ctx, appPool, fxB, nodeA, configID[fxA.tenant])
		t.Log("marker=node_config_pg18_tenant_publish_isolation_ok")
	})

	t.Run("cross tenant paths and reused connections fail closed", func(t *testing.T) {
		fxA, fxB := seedNodeConfigPG18Fixture(t, ctx, admin), seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeA := createNodeConfigPG18Node(t, ctx, service, fxA, fxA.pool, 19121, "rls-a")
		nodeB := createNodeConfigPG18Node(t, ctx, service, fxB, fxB.pool, 19122, "rls-b")
		publishedB, err := service.PublishConfig(ctx, fxB.tenant, nodefabric.PublishInput{
			ActorID: fxB.actor, Scope: "global", Payload: json.RawMessage(`{"rls":"tenant-b"}`),
		})
		if err != nil {
			t.Fatalf("publish tenant B RLS fixture: %v", err)
		}
		if err := service.ReportConfigApplied(ctx, fxB.tenant, nodeB, publishedB.Version, "verified", "tenant-b-evidence"); err != nil {
			t.Fatalf("report tenant B RLS fixture: %v", err)
		}
		draftB := seedNodeConfigPG18Draft(t, ctx, admin, fxB)
		beforeB := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fxB.tenant)

		var pools, servers, nodes, configs, applications int
		if err := appPool.InTx(ctx, platformdb.Scope{TenantID: fxA.tenant, ActorID: fxA.actor}, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM node_pools WHERE id=$1),
				(SELECT count(*) FROM servers WHERE id=$2),
				(SELECT count(*) FROM nodes WHERE id=$3),
				(SELECT count(*) FROM node_configs WHERE id=$4),
				(SELECT count(*) FROM node_config_applications WHERE config_id=$4)`,
				fxB.pool, fxB.server, nodeB, publishedB.ConfigID).
				Scan(&pools, &servers, &nodes, &configs, &applications); err != nil {
				return err
			}
			updated, err := tx.Exec(ctx, `UPDATE nodes SET weight=weight+1 WHERE tenant_id=$1 AND id=$2`, fxB.tenant, nodeB)
			if err != nil {
				return err
			}
			deleted, err := tx.Exec(ctx, `DELETE FROM node_configs WHERE tenant_id=$1 AND id=$2`, fxB.tenant, draftB)
			if err != nil {
				return err
			}
			if updated.RowsAffected() != 0 || deleted.RowsAffected() != 0 {
				return fmt.Errorf("cross-tenant update/delete affected %d/%d rows", updated.RowsAffected(), deleted.RowsAffected())
			}
			return nil
		}); err != nil {
			t.Fatalf("cross-tenant RLS read/write probe: %v", err)
		}
		if pools != 0 || servers != 0 || nodes != 0 || configs != 0 || applications != 0 {
			t.Fatalf("cross-tenant rows visible pools=%d servers=%d nodes=%d configs=%d applications=%d",
				pools, servers, nodes, configs, applications)
		}
		insertErr := appPool.InTx(ctx, platformdb.Scope{TenantID: fxA.tenant, ActorID: fxA.actor}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO node_configs
				(tenant_id,scope,version,payload,content_hash,status)
				VALUES($1,'global',424242,'{}'::jsonb,decode(repeat('00',32),'hex'),'draft')`, fxB.tenant)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(insertErr, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("cross-tenant WITH CHECK error=%v sqlstate=%v, want 42501", insertErr, func() string {
				if pgErr == nil {
					return ""
				}
				return pgErr.Code
			}())
		}
		_, getErr := service.GetAdminNode(ctx, fxA.tenant, nodeB)
		_, fetchErr := service.FetchConfig(ctx, fxA.tenant, nodeB)
		_, publishErr := service.PublishConfig(ctx, fxA.tenant, nodefabric.PublishInput{
			ActorID: fxA.actor, Scope: "pool", ScopeRef: fxB.pool, Payload: json.RawMessage(`{"blocked":true}`),
		})
		reportErr := service.ReportConfigApplied(ctx, fxA.tenant, nodeB, publishedB.Version, "verified", "blocked")
		for label, got := range map[string]error{
			"get": getErr, "fetch": fetchErr, "publish": publishErr, "report": reportErr,
		} {
			assertNodeConfigPG18NotFoundNeutral(t, label, got)
		}
		if afterB := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fxB.tenant); beforeB != afterB {
			t.Fatal("cross-tenant probes changed tenant B business state")
		}
		_ = nodeA
		t.Log("marker=node_config_pg18_rls_cross_tenant_ok")

		conn, err := appPool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire SET LOCAL probe: %v", err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			conn.Release()
			t.Fatalf("begin SET LOCAL probe: %v", err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.actor_id',$2,true)`, fxA.tenant, fxA.actor); err != nil {
			_ = tx.Rollback(ctx)
			conn.Release()
			t.Fatalf("set local RLS scope: %v", err)
		}
		var visible int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1`, nodeA).Scan(&visible); err != nil || visible != 1 {
			_ = tx.Rollback(ctx)
			conn.Release()
			t.Fatalf("SET LOCAL scoped visibility=%d err=%v", visible, err)
		}
		if err := tx.Commit(ctx); err != nil {
			conn.Release()
			t.Fatalf("commit SET LOCAL probe: %v", err)
		}
		assertNodeConfigPG18ConnectionUnscoped(t, ctx, conn)
		conn.Release()

		maxConns := int(appPool.Config().MaxConns)
		held := make([]*pgxpool.Conn, 0, maxConns)
		for len(held) < maxConns {
			c, err := appPool.Acquire(ctx)
			if err != nil {
				for _, acquired := range held {
					acquired.Release()
				}
				t.Fatalf("fill pool for deterministic reuse: %v", err)
			}
			held = append(held, c)
		}
		defer func() {
			for _, c := range held {
				if c != nil {
					c.Release()
				}
			}
		}()
		polluted := held[0]
		var pollutedPID int
		if err := polluted.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pollutedPID); err != nil {
			t.Fatalf("read polluted connection PID: %v", err)
		}
		if _, err := polluted.Exec(ctx, `SELECT set_config('app.tenant_id',$1,false),set_config('app.actor_id',$2,false)`, fxA.tenant, fxA.actor); err != nil {
			t.Fatalf("pollute pooled session scope: %v", err)
		}
		if err := polluted.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1`, nodeA).Scan(&visible); err != nil || visible != 1 {
			t.Fatalf("polluted session visibility=%d err=%v", visible, err)
		}
		polluted.Release()
		held[0] = nil
		reused, err := appPool.Acquire(ctx)
		if err != nil {
			t.Fatalf("reacquire deterministically released connection: %v", err)
		}
		held[0] = reused
		var reusedPID int
		if err := reused.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&reusedPID); err != nil {
			t.Fatalf("read reused connection PID: %v", err)
		}
		if reusedPID != pollutedPID {
			t.Fatalf("pool reuse PID=%d want=%d", reusedPID, pollutedPID)
		}
		assertNodeConfigPG18ConnectionUnscoped(t, ctx, reused)
		t.Log("marker=node_config_pg18_rls_connection_reuse_ok")
	})

	t.Run("legacy reports preserve attribution and append evidence", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeA := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19131, "report-a")
		otherPool := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,region,status)
			VALUES($1,$2,$3,$4,'test','active')`, otherPool, fx.tenant,
			"report-other-"+fx.suffix, "Report Other "+fx.suffix); err != nil {
			t.Fatalf("seed report other pool: %v", err)
		}
		otherNode := createNodeConfigPG18Node(t, ctx, service, fx, otherPool, 19132, "report-other")
		_, wrongPoolVersion := seedNodeConfigPG18History(t, ctx, admin, fx, "pool", otherPool, "superseded", `{"report":"wrong-pool"}`)
		_, wrongNodeVersion := seedNodeConfigPG18History(t, ctx, admin, fx, "node", otherNode, "superseded", `{"report":"wrong-node"}`)
		before := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeA)
		wrongPoolErr := service.ReportConfigApplied(ctx, fx.tenant, nodeA, wrongPoolVersion, "verified", "wrong-pool")
		wrongNodeErr := service.ReportConfigApplied(ctx, fx.tenant, nodeA, wrongNodeVersion, "verified", "wrong-node")
		unknownErr := service.ReportConfigApplied(ctx, fx.tenant, nodeA, wrongNodeVersion+100000, "verified", "unknown")
		assertNodeConfigPG18SameConflict(t, wrongPoolErr, wrongNodeErr, unknownErr)
		if after := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeA); after != before {
			t.Fatalf("wrong-scope reports wrote applications: before=%d after=%d", before, after)
		}
		t.Log("marker=node_config_pg18_report_wrong_scope_ok")

		poolConfig, poolVersion := seedNodeConfigPG18History(t, ctx, admin, fx, "pool", fx.pool, "superseded", `{"report":"late-pool"}`)
		if err := service.ReportConfigApplied(ctx, fx.tenant, nodeA, poolVersion, "health_passed", "late-pool-evidence"); err != nil {
			t.Fatalf("report superseded pool config: %v", err)
		}
		assertNodeConfigPG18LatestApplication(t, ctx, admin, fx.tenant, nodeA, poolConfig,
			"health_passed", "late-pool-evidence")
		t.Log("marker=node_config_pg18_report_superseded_ok")

		_, movedVersion := seedNodeConfigPG18History(t, ctx, admin, fx, "pool", fx.pool, "superseded", `{"report":"before-move"}`)
		if _, err := admin.Exec(ctx, `UPDATE nodes SET pool_id=$3 WHERE tenant_id=$1 AND id=$2`, fx.tenant, nodeA, otherPool); err != nil {
			t.Fatalf("fixture move node to other pool: %v", err)
		}
		movedBefore := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeA)
		movedErr := service.ReportConfigApplied(ctx, fx.tenant, nodeA, movedVersion, "verified", "stale-pool")
		assertNodeConfigPG18SameConflict(t, movedErr, unknownErr)
		var currentPool string
		if err := admin.QueryRow(ctx, `SELECT pool_id::text FROM nodes WHERE tenant_id=$1 AND id=$2`, fx.tenant, nodeA).Scan(&currentPool); err != nil {
			t.Fatalf("read moved node pool: %v", err)
		}
		if currentPool != otherPool || nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeA) != movedBefore {
			t.Fatalf("stale pool report mutated node/application state: pool=%s", currentPool)
		}
		if _, err := admin.Exec(ctx, `UPDATE nodes SET pool_id=$3 WHERE tenant_id=$1 AND id=$2`, fx.tenant, nodeA, fx.pool); err != nil {
			t.Fatalf("restore fixture node pool: %v", err)
		}
		t.Log("marker=node_config_pg18_report_pool_move_ok")

		repeatConfig, repeatVersion := seedNodeConfigPG18History(t, ctx, admin, fx, "node", nodeA, "rolled_back", `{"report":"repeat"}`)
		repeatBefore := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeA)
		for i := 0; i < 2; i++ {
			if err := service.ReportConfigApplied(ctx, fx.tenant, nodeA, repeatVersion, "rolled_back", "repeat-evidence"); err != nil {
				t.Fatalf("append repeated legacy report %d: %v", i+1, err)
			}
		}
		var total, distinctIDs int
		if err := admin.QueryRow(ctx, `SELECT count(*),count(DISTINCT id) FROM node_config_applications
			WHERE tenant_id=$1 AND node_id=$2 AND config_id=$3 AND phase='rolled_back'
			AND detail->>'contract'='legacy_layer_attribution' AND detail->>'message'='repeat-evidence'`,
			fx.tenant, nodeA, repeatConfig).Scan(&total, &distinctIDs); err != nil {
			t.Fatalf("verify repeated legacy evidence: %v", err)
		}
		if total != 2 || distinctIDs != 2 || nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeA) != repeatBefore+2 {
			t.Fatalf("repeated legacy evidence total=%d distinct=%d", total, distinctIDs)
		}
		t.Log("marker=node_config_pg18_report_legacy_repeat_append_only_ok")
	})

	t.Run("expired stored layer is never re-signed", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19141, "expired-layer")
		published, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"expiry":"must-fail-closed"}`),
		})
		if err != nil {
			t.Fatalf("publish expiry fixture: %v", err)
		}
		var contentHash []byte
		if err := admin.QueryRow(ctx, `SELECT content_hash FROM node_configs WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, published.ConfigID).Scan(&contentHash); err != nil {
			t.Fatalf("read expiry fixture hash: %v", err)
		}
		expiredAt := time.Now().UTC().Add(-time.Minute)
		message := append(append([]byte{}, contentHash...), []byte(expiredAt.Format(time.RFC3339))...)
		expiredSignature := signer.Sign(message)
		if _, err := admin.Exec(ctx, `UPDATE node_configs
			SET signature=$3,signature_expires_at=$4,signed_at=$4
			WHERE tenant_id=$1 AND id=$2`, fx.tenant, published.ConfigID, expiredSignature, expiredAt); err != nil {
			t.Fatalf("install correctly signed expired layer: %v", err)
		}
		_, err = service.FetchConfig(ctx, fx.tenant, nodeID)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeUnavailable {
			t.Fatalf("fetch correctly signed expired layer error=%v, want unavailable", err)
		}
		t.Log("marker=node_config_pg18_expired_layer_refusal_ok")
	})

	t.Run("layer expiring while fetch waits is refused", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19144, "expiry-lock-wait")
		published, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"expiry":"cross-lock-wait"}`),
		})
		if err != nil {
			t.Fatalf("publish lock-wait expiry fixture: %v", err)
		}
		var contentHash []byte
		if err := admin.QueryRow(ctx, `SELECT content_hash FROM node_configs WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, published.ConfigID).Scan(&contentHash); err != nil {
			t.Fatalf("read lock-wait expiry hash: %v", err)
		}
		lockConn, err := appPool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire expiry lock holder: %v", err)
		}
		lockTx, err := lockConn.Begin(ctx)
		if err != nil {
			lockConn.Release()
			t.Fatalf("begin expiry lock holder: %v", err)
		}
		released := false
		defer func() {
			if !released {
				_ = lockTx.Rollback(context.Background())
				lockConn.Release()
			}
		}()
		if _, err := lockTx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.actor_id',$2,true)`, fx.tenant, fx.actor); err != nil {
			t.Fatalf("scope expiry lock holder: %v", err)
		}
		var lockedNode string
		if err := lockTx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
			fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("lock node across expiry: %v", err)
		}
		expiresAt := time.Now().UTC().Add(3 * time.Second)
		message := append(append([]byte{}, contentHash...), []byte(expiresAt.Format(time.RFC3339))...)
		signature := signer.Sign(message)
		if _, err := admin.Exec(ctx, `UPDATE node_configs
			SET signature=$3,signature_expires_at=$4,signed_at=now()
			WHERE tenant_id=$1 AND id=$2`, fx.tenant, published.ConfigID, signature, expiresAt); err != nil {
			t.Fatalf("install short-lived signed layer after node lock: %v", err)
		}
		if remaining := time.Until(expiresAt); remaining < 2*time.Second {
			t.Fatalf("short-lived signature has insufficient pre-fetch lifetime: %s", remaining)
		}
		fetchResult := make(chan error, 1)
		go func() {
			_, err := service.FetchConfig(ctx, fx.tenant, nodeID)
			fetchResult <- err
		}()
		observeDeadline := time.NewTimer(2 * time.Second)
		observeTicker := time.NewTicker(10 * time.Millisecond)
		observedWait := false
		for !observedWait {
			select {
			case err := <-fetchResult:
				observeDeadline.Stop()
				observeTicker.Stop()
				t.Fatalf("FetchConfig returned before node lock release: %v", err)
			case <-observeTicker.C:
				var waits int
				if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
					WHERE datname=current_database() AND wait_event_type='Lock'
					AND query LIKE '%SELECT pool_id FROM nodes%FOR SHARE%'`).Scan(&waits); err != nil {
					t.Fatalf("observe FetchConfig node-lock wait: %v", err)
				}
				observedWait = waits > 0
			case <-observeDeadline.C:
				observeTicker.Stop()
				t.Fatal("FetchConfig node-lock wait was not observed")
			}
		}
		observeDeadline.Stop()
		observeTicker.Stop()
		if remaining := time.Until(expiresAt) + 50*time.Millisecond; remaining > 0 {
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				t.Fatalf("context ended while crossing signature expiry: %v", ctx.Err())
			}
		}
		if err := lockTx.Rollback(ctx); err != nil {
			t.Fatalf("release expiry node lock: %v", err)
		}
		lockConn.Release()
		released = true
		select {
		case err := <-fetchResult:
			var he *httpx.Error
			if !errors.As(err, &he) || he.Code != httpx.CodeUnavailable {
				t.Fatalf("post-wait expired fetch error=%v, want unavailable", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("FetchConfig did not finish after expiry lock release")
		}
		t.Log("marker=node_config_pg18_expiry_lock_wait_refusal_ok")
	})

	t.Run("duplicate published logical layer fails closed", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19142, "duplicate-layer")
		published, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"duplicate":false}`),
		})
		if err != nil {
			t.Fatalf("publish duplicate-layer baseline: %v", err)
		}
		payload := `{"duplicate":true}`
		contentHash := sha256.Sum256([]byte(payload))
		expiresAt := time.Now().UTC().Add(time.Hour)
		message := append(append([]byte{}, contentHash[:]...), []byte(expiresAt.Format(time.RFC3339))...)
		signature := signer.Sign(message)
		if _, err := admin.Exec(ctx, `INSERT INTO node_configs
			(tenant_id,scope,version,payload,content_hash,signature,signing_key_id,
			 signed_at,signature_expires_at,status,rollout_percent,published_at,created_by)
			VALUES($1,'global',$2,$3::jsonb,$4,$5,$6,now(),$7,'published',100,now(),$8)`,
			fx.tenant, published.Version, payload, contentHash[:], signature, signer.KeyID(), expiresAt, fx.actor); err != nil {
			t.Fatalf("seed duplicate published logical layer: %v", err)
		}
		_, err = service.FetchConfig(ctx, fx.tenant, nodeID)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeUnavailable {
			t.Fatalf("fetch duplicate published logical layer error=%v, want unavailable", err)
		}
		t.Log("marker=node_config_pg18_duplicate_layer_refusal_ok")
	})

	t.Run("malformed global layer reference fails closed", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19143, "malformed-global")
		payload := `{"malformed_global":true}`
		contentHash := sha256.Sum256([]byte(payload))
		expiresAt := time.Now().UTC().Add(time.Hour)
		message := append(append([]byte{}, contentHash[:]...), []byte(expiresAt.Format(time.RFC3339))...)
		signature := signer.Sign(message)
		if _, err := admin.Exec(ctx, `INSERT INTO node_configs
			(tenant_id,scope,scope_ref,version,payload,content_hash,signature,signing_key_id,
			 signed_at,signature_expires_at,status,rollout_percent,published_at,created_by)
			VALUES($1,'global',$2,1,$3::jsonb,$4,$5,$6,now(),$7,'published',100,now(),$8)`,
			fx.tenant, fx.pool, payload, contentHash[:], signature, signer.KeyID(), expiresAt, fx.actor); err != nil {
			t.Fatalf("seed malformed referenced global layer: %v", err)
		}
		_, err := service.FetchConfig(ctx, fx.tenant, nodeID)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeUnavailable {
			t.Fatalf("fetch malformed referenced global layer error=%v, want unavailable", err)
		}
		t.Log("marker=node_config_pg18_malformed_global_refusal_ok")
	})

	t.Run("version allocator fails closed without partial writes", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19201, "max-race")
		seedNodeConfigPG18LegacyVersion(t, ctx, admin, fx, int64(math.MaxInt32-1))
		var configsBefore, auditsBefore int
		if err := admin.QueryRow(ctx, `SELECT count(*),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish')
			FROM node_configs WHERE tenant_id=$1`, fx.tenant).Scan(&configsBefore, &auditsBefore); err != nil {
			t.Fatalf("read max-race baseline: %v", err)
		}
		jobs := []nodeConfigPG18PublishJob{
			{tenant: fx.tenant, index: 1, input: nodefabric.PublishInput{ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"race":1}`)}},
			{tenant: fx.tenant, index: 2, input: nodefabric.PublishInput{ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"race":2}`)}},
		}
		results := runNodeConfigPG18Publishes(ctx, service, jobs)
		var success *nodefabric.PublishOutput
		conflicts := 0
		for _, result := range results {
			if result.err == nil && result.out != nil {
				if success != nil {
					t.Fatal("max-race returned more than one success")
				}
				success = result.out
			} else if nodeConfigPG18IsConflict(result.err) {
				conflicts++
			} else {
				t.Fatalf("max-race returned unexpected error: %v", result.err)
			}
		}
		if success == nil || success.Version != math.MaxInt32 || conflicts != 1 {
			t.Fatalf("max-race success=%+v conflicts=%d", success, conflicts)
		}
		var configs, audits, desired, successRows, rolledBack, published, superseded int
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish'),
			coalesce((SELECT desired_config_version FROM nodes WHERE tenant_id=$1 AND id=$2),0),
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND id=$3),
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND status='rolled_back'),
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND status='published'),
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND status='superseded')`,
			fx.tenant, nodeID, success.ConfigID).Scan(&configs, &audits, &desired, &successRows, &rolledBack, &published, &superseded); err != nil {
			t.Fatalf("verify max-race state: %v", err)
		}
		if configs != configsBefore+1 || audits != auditsBefore+1 || desired != math.MaxInt32 ||
			successRows != 1 || rolledBack != 1 || published != 1 || superseded != 0 {
			t.Fatalf("max-race final state configs=%d audits=%d desired=%d success=%d rolled_back=%d published=%d superseded=%d",
				configs, audits, desired, successRows, rolledBack, published, superseded)
		}

		for _, version := range []int64{math.MaxInt32, 0, -1} {
			fx := seedNodeConfigPG18Fixture(t, ctx, admin)
			seedNodeConfigPG18LegacyVersion(t, ctx, admin, fx, version)
			before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
			_, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"must":"rollback"}`),
			})
			if !nodeConfigPG18IsConflict(err) {
				t.Fatalf("seed version %d publish error=%v, want conflict", version, err)
			}
			after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
			if before != after {
				t.Fatalf("seed version %d changed business state after conflict", version)
			}
		}
		t.Log("marker=node_config_pg18_version_limit_ok")
	})
}
