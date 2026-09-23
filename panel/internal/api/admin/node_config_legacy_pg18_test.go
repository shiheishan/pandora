package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const nodeConfigPG18FixtureSentinel = "disposable-v1"

type nodeConfigPG18Fixture struct {
	tenant string
	actor  string
	pool   string
	server string
	suffix string
}

// TestNodeConfigLegacyPG18 is an opt-in database gate. It must only run
// against a database created for this suite and discarded as a whole by the
// outer runner. The fixture admin connection may seed and inspect rows; every
// business operation and every RLS assertion uses the aegis_app connection.
func TestNodeConfigLegacyPG18(t *testing.T) {
	fixtureMode := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_DATABASE"))
	expectedDatabaseOID := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_DATABASE_OID"))
	expectedSystemID := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_SYSTEM_ID"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_NODE_CONFIG_PG18_RUN_ID"))
	if fixtureMode == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" &&
		expectedDatabaseOID == "" && expectedSystemID == "" && runID == "" {
		t.Skip("node config PostgreSQL 18 fixture is not configured")
	}
	if fixtureMode != nodeConfigPG18FixtureSentinel || appDSN == "" || adminDSN == "" || expectedDatabase == "" ||
		expectedDatabaseOID == "" || expectedSystemID == "" || runID == "" {
		t.Fatal("disposable-v1, both DSNs, exact database/OID/system ID and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_nodecfg_") {
		t.Fatalf("refusing non-disposable database name %q", expectedDatabase)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{15,80}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable run ID %q", runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1770*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture administrator connection: %v", err)
	}
	defer admin.Close(ctx)
	appPool, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open aegis_app PostgreSQL 18 pool: %v", err)
	}
	defer appPool.Close()

	assertNodeConfigPG18Target(t, ctx, admin, appPool, expectedDatabase, expectedDatabaseOID, expectedSystemID, runID)
	fx := seedNodeConfigPG18Fixture(t, ctx, admin)
	assertNodeConfigPG18RuntimeRoleAndRLS(t, ctx, appPool, fx)

	signer, err := platformcrypto.NewSigner(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	if err != nil {
		t.Fatalf("construct deterministic test signer: %v", err)
	}
	service := nodefabric.NewService(appPool, signer)

	var nodeID string
	var globalConfigID string
	var globalVersion int
	t.Run("published config materializes on a new node", func(t *testing.T) {
		global, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"global":"v1","winner":"global"}`),
		})
		if err != nil {
			t.Fatalf("publish global config: %v", err)
		}
		pool, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
			Payload: json.RawMessage(`{"pool":"v1","winner":"pool"}`),
		})
		if err != nil {
			t.Fatalf("publish pool config: %v", err)
		}
		if global.Version <= 0 || pool.Version != global.Version+1 {
			t.Fatalf("unexpected tenant version sequence: global=%d pool=%d", global.Version, pool.Version)
		}
		globalConfigID, globalVersion = global.ConfigID, global.Version

		node, err := service.CreateAdminNode(ctx, fx.tenant, nodefabric.CreateAdminNodeInput{
			ActorID: fx.actor, Name: "node-" + fx.suffix, ServerID: fx.server, PoolID: fx.pool,
			NodeType: "shadowsocks", ServerHost: "edge.example.test", ServerPort: 8388,
			Kernel: "auto", TrafficRate: 1, DisplayName: "Node Config PG18",
			ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`), SortOrder: 10,
		})
		if err != nil {
			t.Fatalf("create admin node: %v", err)
		}
		nodeID = node.ID

		var desired *int
		if err := admin.QueryRow(ctx,
			`SELECT desired_config_version FROM nodes WHERE tenant_id=$1 AND id=$2`, fx.tenant, nodeID).
			Scan(&desired); err != nil {
			t.Fatalf("read materialized desired version: %v", err)
		}
		if desired == nil || *desired != pool.Version {
			t.Fatalf("new node desired version=%v, want %d", desired, pool.Version)
		}

		cfg, err := service.FetchConfig(ctx, fx.tenant, nodeID)
		if err != nil {
			t.Fatalf("fetch merged config: %v", err)
		}
		wantSources := []string{fmt.Sprintf("global@v%d", global.Version), fmt.Sprintf("pool@v%d", pool.Version)}
		if cfg.Version != pool.Version || fmt.Sprint(cfg.Sources) != fmt.Sprint(wantSources) {
			t.Fatalf("merged config version/sources=%d/%v, want %d/%v", cfg.Version, cfg.Sources, pool.Version, wantSources)
		}
		if !nodefabric.VerifyConfigSignature(signer.PublicKey(), cfg.Hash, cfg.Signature, cfg.ExpiresAt) {
			t.Fatal("merged config signature did not verify")
		}
		var payload map[string]any
		if err := json.Unmarshal(cfg.Payload, &payload); err != nil {
			t.Fatalf("decode merged payload: %v", err)
		}
		if payload["winner"] != "pool" || payload["global"] != "v1" || payload["pool"] != "v1" {
			t.Fatalf("unexpected merged payload: %#v", payload)
		}
		t.Log("marker=node_config_pg18_new_node_materialization_ok")
	})

	t.Run("unique report is attributed exactly", func(t *testing.T) {
		if nodeID == "" || globalConfigID == "" || globalVersion == 0 {
			t.Fatal("publish/new-node prerequisite did not run")
		}
		before := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID)
		if err := service.ReportConfigApplied(ctx, fx.tenant, nodeID, globalVersion, "verified", "unique"); err != nil {
			t.Fatalf("report unique legacy version: %v", err)
		}
		if after := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID); after != before+1 {
			t.Fatalf("unique report applications before=%d after=%d", before, after)
		}
		var configID, contract, message string
		if err := admin.QueryRow(ctx, `SELECT config_id::text,
			detail->>'contract', detail->>'message'
			FROM node_config_applications
			WHERE tenant_id=$1 AND node_id=$2 ORDER BY occurred_at DESC,id DESC LIMIT 1`,
			fx.tenant, nodeID).Scan(&configID, &contract, &message); err != nil {
			t.Fatalf("read unique report attribution: %v", err)
		}
		if configID != globalConfigID || contract != "legacy_layer_attribution" || message != "unique" {
			t.Fatalf("wrong report attribution config=%s contract=%s message=%s", configID, contract, message)
		}
		t.Log("marker=node_config_pg18_report_unique_ok")
	})

	t.Run("unknown and ambiguous reports fail closed", func(t *testing.T) {
		if nodeID == "" {
			t.Fatal("new-node prerequisite did not run")
		}
		before := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID)
		assertNodeConfigPG18Conflict(t, service.ReportConfigApplied(ctx, fx.tenant, nodeID, 999999, "verified", "unknown"))
		if got := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID); got != before {
			t.Fatalf("unknown report wrote applications: before=%d after=%d", before, got)
		}

		ambiguousVersion := globalVersion
		one := sha256.Sum256([]byte(`{"layer":"node"}`))
		if _, err := admin.Exec(ctx, `INSERT INTO node_configs
			(tenant_id,scope,scope_ref,version,payload,content_hash,status,published_at,created_by)
			VALUES ($1,'node',$5,$3,'{"layer":"node"}'::jsonb,$4,'superseded',now(),$2)`,
			fx.tenant, fx.actor, ambiguousVersion, one[:], nodeID); err != nil {
			t.Fatalf("seed ambiguous report history: %v", err)
		}
		assertNodeConfigPG18Conflict(t, service.ReportConfigApplied(ctx, fx.tenant, nodeID, ambiguousVersion, "verified", "ambiguous"))
		if got := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID); got != before {
			t.Fatalf("ambiguous report wrote applications: before=%d after=%d", before, got)
		}
		t.Log("marker=node_config_pg18_report_fail_closed_ok")
	})

	t.Run("pool delete refuses a published config", func(t *testing.T) {
		poolID := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,status)
			VALUES($1,$2,$3,$4,'active')`, poolID, fx.tenant, "delete-"+fx.suffix, "Delete "+fx.suffix); err != nil {
			t.Fatalf("seed delete pool: %v", err)
		}
		published, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "pool", ScopeRef: poolID,
			Payload: json.RawMessage(`{"fixture":"pool-delete-published"}`),
		})
		if err != nil {
			t.Fatalf("publish pool config before deletion: %v", err)
		}
		assertNodeConfigPG18AppVisible(t, ctx, appPool, fx.tenant, poolID, published.ConfigID)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		h := &handlers{d: Deps{Pool: appPool, Log: log}}
		req := httptest.NewRequest(http.MethodDelete, "/v1/node-pools/"+poolID, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", poolID)
		reqCtx := context.WithValue(req.Context(), chi.RouteCtxKey, route)
		reqCtx = httpx.WithTenantID(reqCtx, fx.tenant)
		reqCtx = httpx.WithRequestID(reqCtx, "nodecfg-pg18-delete")
		reqCtx = httpx.WithPrincipal(reqCtx, &httpx.Principal{
			Kind: "admin", Audience: "admin", UserID: fx.actor, TenantID: fx.tenant,
		})
		req = req.WithContext(reqCtx)
		rr := httptest.NewRecorder()
		h.deleteNodePool(rr, req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("delete pool with config history status=%d body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Error struct {
				Code httpx.Code `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error.Code != httpx.CodeConflict {
			t.Fatalf("delete pool conflict response=%s err=%v", rr.Body.String(), err)
		}
		var pools, configs int
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_pools WHERE tenant_id=$1 AND id=$2),
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND id=$3)`,
			fx.tenant, poolID, published.ConfigID).Scan(&pools, &configs); err != nil {
			t.Fatalf("verify refused pool deletion: %v", err)
		}
		if pools != 1 || configs != 1 {
			t.Fatalf("pool delete was not atomic: pools=%d configs=%d", pools, configs)
		}
		t.Log("marker=node_config_pg18_pool_delete_config_refusal_ok")
	})

	t.Log("node_config_legacy_pg18_first_batch=ok")
	runNodeConfigPG18PublicationLimitBatch(t, ctx, admin, appPool, signer)
	t.Log("node_config_legacy_pg18_second_batch=ok")
	runNodeConfigPG18CancellationRollbackBatch(t, ctx, admin, appPool, signer)
	t.Log("node_config_legacy_pg18_third_batch=ok")
	runNodeConfigPG18LifecycleRaceBatch(t, ctx, admin, appPool, signer)
	t.Log("node_config_legacy_pg18_fourth_batch=ok")
	runNodeConfigPG18BootstrapSecurityBatch(t, ctx, admin, appPool, signer)
	t.Log("node_config_legacy_pg18_fifth_batch=ok")
	runNodeConfigPG18PoolLifecycleRaceBatch(t, ctx, admin, appDSN, signer)
	runNodeConfigPG18GlobalPoolRetirementRaceBatch(t, ctx, admin, appDSN, signer)
	t.Log("node_config_legacy_pg18_sixth_batch=ok")
	runNodeConfigPG18PoolDeleteRaceBatch(t, ctx, admin, appDSN, signer)
	t.Log("node_config_legacy_pg18_seventh_batch=ok")
	runNodeConfigPG18NewMaterializationRaceBatch(t, ctx, admin, appDSN, signer)
	t.Log("node_config_legacy_pg18_eighth_batch=ok")
	runNodeConfigPG18LockStressBatch(t, ctx, admin, appDSN, signer)
	t.Log("node_config_legacy_pg18_ninth_batch=ok")
}

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

type nodeConfigPG18LockHolder struct {
	conn     *pgxpool.Conn
	tx       pgx.Tx
	pid      int
	released bool
}

func beginNodeConfigPG18LockHolder(t *testing.T, ctx context.Context,
	appPool *platformdb.Pool, fx nodeConfigPG18Fixture) *nodeConfigPG18LockHolder {
	t.Helper()
	conn, err := appPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire cancellation lock holder: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatalf("begin cancellation lock holder: %v", err)
	}
	holder := &nodeConfigPG18LockHolder{conn: conn, tx: tx}
	ok := false
	defer func() {
		if !ok {
			holder.cleanup()
		}
	}()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id',$1,true),
		        set_config('app.actor_id',$2,true)`, fx.tenant, fx.actor); err != nil {
		t.Fatalf("scope cancellation lock holder: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder.pid); err != nil {
		t.Fatalf("read cancellation lock holder PID: %v", err)
	}
	ok = true
	return holder
}

func (h *nodeConfigPG18LockHolder) cleanup() {
	if h == nil || h.released {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = h.tx.Rollback(ctx)
	h.conn.Release()
	h.released = true
}

func (h *nodeConfigPG18LockHolder) release(t *testing.T) {
	t.Helper()
	if h == nil || h.released {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := h.tx.Rollback(ctx)
	cancel()
	h.conn.Release()
	h.released = true
	if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("release cancellation lock holder: %v", err)
	}
}

func (h *nodeConfigPG18LockHolder) commit(t *testing.T) {
	t.Helper()
	if h == nil || h.released {
		t.Fatal("lock holder is already released")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := h.tx.Commit(ctx)
	h.conn.Release()
	h.released = true
	if err != nil {
		t.Fatalf("commit lock holder: %v", err)
	}
}

func waitNodeConfigPG18BlockedPID(t *testing.T, ctx context.Context,
	admin *pgx.Conn, holderPID int, queryLike string) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		if err := admin.QueryRow(ctx, `SELECT coalesce((
			SELECT a.pid
			  FROM pg_catalog.pg_stat_activity AS a
			 WHERE a.datname=current_database()
			   AND a.usename='aegis_app'
			   -- 不看 state：等锁时 pg_stat_activity 的 state / query /
			   -- wait_event 不是原子快照，state 可能还停在上一条语句。
			   AND a.wait_event_type='Lock'
			   AND a.query LIKE $2
			   AND $1 = ANY(pg_catalog.pg_blocking_pids(a.pid))
			 ORDER BY a.query_start,a.pid
			 LIMIT 1
		),0)`, holderPID, queryLike).Scan(&pid); err != nil {
			t.Fatalf("observe blocked PostgreSQL operation: %v", err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("PostgreSQL lock wait matching %q was not observed;%s",
				queryLike, nodeConfigPG18BlockedReport(t, ctx, admin, holderPID))
		case <-ctx.Done():
			t.Fatalf("context ended while observing PostgreSQL lock wait: %v", ctx.Err())
		}
	}
}

// nodeConfigPG18BlockedReport 列出当前被 holderPID 挡住的所有连接。
// 观察超时的时候，光说「没等到」没法判断是根本没人等锁、还是等锁的人
// 卡在另一条语句上，这份报告直接给出真实的 state / wait / query。
func nodeConfigPG18BlockedReport(t *testing.T, ctx context.Context,
	admin *pgx.Conn, holderPID int) string {
	t.Helper()
	rows, err := admin.Query(ctx, `
		SELECT a.pid,coalesce(a.application_name,''),a.state,
		       coalesce(a.wait_event_type,''),coalesce(a.wait_event,''),
		       coalesce(left(regexp_replace(a.query,'\s+',' ','g'),140),''),
		       pg_catalog.pg_blocking_pids(a.pid)
		  FROM pg_catalog.pg_stat_activity AS a
		 WHERE a.datname=current_database() AND a.pid<>pg_backend_pid()
		 ORDER BY a.pid`)
	if err != nil {
		return fmt.Sprintf("\n  <诊断查询失败: %v>", err)
	}
	defer rows.Close()
	var report strings.Builder
	for rows.Next() {
		var pid int
		var name, state, waitType, waitEvent, query string
		var blockers []int32
		if err := rows.Scan(&pid, &name, &state, &waitType, &waitEvent,
			&query, &blockers); err != nil {
			return report.String() + fmt.Sprintf("\n  <扫描失败: %v>", err)
		}
		mark := " "
		for _, blocker := range blockers {
			if int(blocker) == holderPID {
				mark = "*"
			}
		}
		fmt.Fprintf(&report, "\n %s pid=%d app=%q state=%s wait=%s/%s blockers=%v q=%s",
			mark, pid, name, state, waitType, waitEvent, blockers, query)
	}
	if report.Len() == 0 {
		return "\n  <库里没有其它连接>"
	}
	return "  （* = 被持锁方 " + fmt.Sprint(holderPID) + " 挡住）" + report.String()
}

func awaitNodeConfigPG18Cancellation(t *testing.T, callCtx context.Context, result <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-result:
		if callCtx.Err() != context.Canceled {
			t.Fatalf("call context=%v, want canceled", callCtx.Err())
		}
		var he *httpx.Error
		if errors.As(err, &he) {
			t.Fatalf("cancellation was converted to http error: %v", err)
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "57014" {
			return
		}
		t.Fatalf("canceled operation returned %v", err)
	case <-deadline.C:
		t.Fatal("canceled operation did not return while its blocker remained held")
	}
}

func assertNodeConfigPG18WaiterClean(t *testing.T, ctx context.Context,
	admin *pgx.Conn, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var residue int
		if err := admin.QueryRow(ctx, `SELECT count(*)
			FROM pg_catalog.pg_stat_activity AS a
			WHERE a.pid=$1 AND (
				a.xact_start IS NOT NULL
				OR a.wait_event_type='Lock'
				OR EXISTS (
					SELECT 1 FROM pg_catalog.pg_locks AS l
					WHERE l.pid=a.pid AND l.locktype='advisory'
				)
			)`, pid).Scan(&residue); err != nil {
			t.Fatalf("inspect canceled waiter cleanup: %v", err)
		}
		if residue == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("canceled waiter PID %d retained a transaction or lock", pid)
		case <-ctx.Done():
			t.Fatalf("context ended while verifying waiter cleanup: %v", ctx.Err())
		}
	}
}

func openNodeConfigPG18NamedPool(t *testing.T, ctx context.Context, dsn, applicationName string) *platformdb.Pool {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		t.Fatal("parse named PostgreSQL worker DSN failed")
	}
	q := u.Query()
	q.Set("application_name", applicationName)
	q.Set("statement_timeout", "12000")
	u.RawQuery = q.Encode()
	pool, err := platformdb.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open named PostgreSQL worker %q: %v", applicationName, err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SHOW application_name`).Scan(&got); err != nil || got != applicationName {
		pool.Close()
		t.Fatalf("verify named PostgreSQL worker got=%q want=%q err=%v", got, applicationName, err)
	}
	return pool
}

func assertNodeConfigPG18ApplicationName(t *testing.T, ctx context.Context,
	admin *pgx.Conn, pid int, want string) {
	t.Helper()
	var got string
	if err := admin.QueryRow(ctx, `SELECT application_name FROM pg_catalog.pg_stat_activity WHERE pid=$1`, pid).
		Scan(&got); err != nil {
		t.Fatalf("read PostgreSQL application_name for pid %d: %v", pid, err)
	}
	if got != want {
		t.Fatalf("PostgreSQL pid %d application_name=%q want=%q", pid, got, want)
	}
}

func runNodeConfigPG18CancellationRollbackBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	service := nodefabric.NewService(appPool, signer)

	t.Run("publish canceled behind release advisory lock rolls back", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		if _, err := holder.tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(
			pg_catalog.hashtextextended($1,0))`, "node-config-release/"+fx.tenant); err != nil {
			t.Fatalf("hold release advisory lock: %v", err)
		}
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "global",
				Payload: json.RawMessage(`{"rollback":"advisory-wait"}`),
			})
			result <- err
		}()
		waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		cancel()
		awaitNodeConfigPG18Cancellation(t, opCtx, result)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
		holder.release(t)
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("advisory-wait cancellation changed tenant business state")
		}
		t.Log("marker=node_config_pg18_publish_advisory_cancel_rollback_ok")
	})

	t.Run("publish canceled behind desired projection update rolls back", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19301, "desired-cancel")
		baseline, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global",
			Payload: json.RawMessage(`{"rollback":"baseline","winner":"global"}`),
		})
		if err != nil {
			t.Fatalf("publish desired-cancel baseline: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		var lockedNode string
		if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("hold desired projection node lock: %v", err)
		}
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "global",
				Payload: json.RawMessage(`{"rollback":"must-not-commit","winner":"global"}`),
			})
			result <- err
		}()
		waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%SELECT id FROM nodes%serving_status<>'retired'%`)
		cancel()
		awaitNodeConfigPG18Cancellation(t, opCtx, result)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
		holder.release(t)
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("desired-update cancellation changed tenant business state")
		}
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, nodeID,
			baseline.Version, []string{fmt.Sprintf("global@v%d", baseline.Version)}, "global", map[string]int{})
		t.Log("marker=node_config_pg18_publish_desired_wait_cancel_rollback_ok")
	})

	t.Run("report canceled behind node share lock writes no evidence", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19302, "report-cancel")
		published, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"report":"cancel-baseline"}`),
		})
		if err != nil {
			t.Fatalf("publish report-cancel baseline: %v", err)
		}
		applicationsBefore := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID)
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		var lockedNode string
		if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("hold report node lock: %v", err)
		}
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			result <- service.ReportConfigApplied(opCtx, fx.tenant, nodeID,
				published.Version, "verified", "must-not-commit")
		}()
		waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%SELECT pool_id::text FROM nodes%FOR SHARE%`)
		cancel()
		awaitNodeConfigPG18Cancellation(t, opCtx, result)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
		holder.release(t)
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("report-wait cancellation changed tenant business state")
		}
		if after := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID); after != applicationsBefore {
			t.Fatalf("report-wait cancellation applications before=%d after=%d", applicationsBefore, after)
		}
		t.Log("marker=node_config_pg18_report_wait_cancel_rollback_ok")
	})
}

type nodeConfigPG18PublishCallResult struct {
	out *nodefabric.PublishOutput
	err error
}

type nodeConfigPG18HTTPCallResult struct {
	code int
	body []byte
}

type nodeConfigPG18AdminNodeCallResult struct {
	out *nodefabric.AdminNode
	err error
}

type nodeConfigPG18BootstrapCallResult struct {
	out *nodefabric.BootstrapOutput
	err error
}

func callNodeConfigPG18DeletePool(ctx context.Context, pool *platformdb.Pool,
	fx nodeConfigPG18Fixture, poolID, requestID string) nodeConfigPG18HTTPCallResult {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handlers{d: Deps{Pool: pool, Log: log}}
	req := httptest.NewRequest(http.MethodDelete, "/v1/node-pools/"+poolID, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", poolID)
	reqCtx := context.WithValue(ctx, chi.RouteCtxKey, route)
	reqCtx = httpx.WithTenantID(reqCtx, fx.tenant)
	reqCtx = httpx.WithRequestID(reqCtx, requestID)
	reqCtx = httpx.WithPrincipal(reqCtx, &httpx.Principal{
		Kind: "admin", Audience: "admin", UserID: fx.actor, TenantID: fx.tenant,
	})
	req = req.WithContext(reqCtx)
	rr := httptest.NewRecorder()
	h.deleteNodePool(rr, req)
	return nodeConfigPG18HTTPCallResult{code: rr.Code, body: append([]byte(nil), rr.Body.Bytes()...)}
}

func awaitNodeConfigPG18HTTPCall(t *testing.T,
	result <-chan nodeConfigPG18HTTPCallResult) nodeConfigPG18HTTPCallResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP handler call did not finish after releasing its blocker")
		return nodeConfigPG18HTTPCallResult{}
	}
}

func awaitNodeConfigPG18AdminNodeCall(t *testing.T,
	result <-chan nodeConfigPG18AdminNodeCallResult) nodeConfigPG18AdminNodeCallResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("admin node call did not finish after releasing its blocker")
		return nodeConfigPG18AdminNodeCallResult{}
	}
}

func awaitNodeConfigPG18BootstrapCall(t *testing.T,
	result <-chan nodeConfigPG18BootstrapCallResult) nodeConfigPG18BootstrapCallResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap call did not finish after releasing its blocker")
		return nodeConfigPG18BootstrapCallResult{}
	}
}

func assertNodeConfigPG18PoolValidation(t *testing.T, label string, result nodeConfigPG18AdminNodeCallResult) {
	t.Helper()
	if result.out != nil {
		t.Fatalf("%s returned node %+v", label, result.out)
	}
	var he *httpx.Error
	if !errors.As(result.err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["pool_id"] == "" {
		t.Fatalf("%s error=%v, want pool_id validation failure", label, result.err)
	}
}

func assertNodeConfigPG18HTTPErrorCode(t *testing.T, result nodeConfigPG18HTTPCallResult, want httpx.Code) {
	t.Helper()
	var body struct {
		Error struct {
			Code httpx.Code `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(result.body, &body); err != nil || body.Error.Code != want {
		t.Fatalf("HTTP response status=%d body=%s code=%s want=%s decode=%v",
			result.code, string(result.body), body.Error.Code, want, err)
	}
}

func assertNodeConfigPG18HTTPOK(t *testing.T, result nodeConfigPG18HTTPCallResult) {
	t.Helper()
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(result.body, &body); err != nil || result.code != http.StatusOK || !body.OK {
		t.Fatalf("HTTP success response status=%d body=%s decode=%v", result.code, string(result.body), err)
	}
}

func assertNodeConfigPG18PoolsReleased(t *testing.T, pools ...*platformdb.Pool) {
	t.Helper()
	// pgxpool 归还连接与被测调用返回之间有一小段窗口：处理逻辑里的
	// defer Release 未必赶在调用方拿到结果之前跑完。瞬时断言会随机抓到
	// 「还剩一条」，那是竞态不是泄漏。给两秒收敛——真泄漏不会自己消失。
	deadline := time.Now().Add(2 * time.Second)
	for i, pool := range pools {
		for {
			got := pool.Stat().AcquiredConns()
			if got == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("named PostgreSQL pool[%d] retained %d acquired connections", i, got)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func awaitNodeConfigPG18PublishCall(t *testing.T, result <-chan nodeConfigPG18PublishCallResult) nodeConfigPG18PublishCallResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("publish call did not finish after releasing its blocker")
		return nodeConfigPG18PublishCallResult{}
	}
}

func awaitNodeConfigPG18LifecycleCall(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle call did not finish after releasing its blocker")
		return nil
	}
}

func assertNodeConfigPG18RetiredState(t *testing.T, ctx context.Context, admin *pgx.Conn,
	service *nodefabric.Service, tenantID, nodeID string, wantConfigs, wantPublishAudits int) {
	t.Helper()
	var serving string
	var desiredIsNull bool
	var configs, publishAudits int
	if err := admin.QueryRow(ctx, `SELECT n.serving_status,n.desired_config_version IS NULL,
		(SELECT count(*) FROM node_configs WHERE tenant_id=$1),
		(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish')
		FROM nodes n WHERE n.tenant_id=$1 AND n.id=$2`, tenantID, nodeID).
		Scan(&serving, &desiredIsNull, &configs, &publishAudits); err != nil {
		t.Fatalf("read retired race state: %v", err)
	}
	if serving != "retired" || !desiredIsNull || configs != wantConfigs || publishAudits != wantPublishAudits {
		t.Fatalf("retired race state serving=%s desired_null=%t configs=%d publish_audits=%d want retired/true/%d/%d",
			serving, desiredIsNull, configs, publishAudits, wantConfigs, wantPublishAudits)
	}
	_, err := service.FetchConfig(ctx, tenantID, nodeID)
	assertNodeConfigPG18NotFoundNeutral(t, "retired fetch", err)
}

func runNodeConfigPG18LifecycleRaceBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	service := nodefabric.NewService(appPool, signer)

	t.Run("publish serializes before retirement", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19401, "publish-retire")
		var rowVersion int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, nodeID).Scan(&rowVersion); err != nil {
			t.Fatalf("read publish-retire row version: %v", err)
		}
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		var lockedNode string
		if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("hold publish-retire node lock: %v", err)
		}
		publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
		go func() {
			out, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "node", ScopeRef: nodeID,
				Payload: json.RawMessage(`{"life":"publish-first"}`),
			})
			publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
		}()
		publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%SELECT id::text FROM nodes%FOR SHARE%`)
		lifecycleResult := make(chan error, 1)
		go func() {
			lifecycleResult <- service.BatchAdminNodeLifecycle(ctx, fx.tenant, nodefabric.BatchNodeLifecycleInput{
				ActorID: fx.actor, ServingStatus: "retired", Reason: "pg18-publish-first",
				Items: []nodefabric.BatchNodeLifecycleItem{{ID: nodeID, RowVersion: rowVersion}},
			})
		}()
		_ = waitNodeConfigPG18BlockedPID(t, ctx, admin, publishPID,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		holder.release(t)
		published := awaitNodeConfigPG18PublishCall(t, publishResult)
		if published.err != nil || published.out == nil || published.out.AffectedNodes != 1 {
			t.Fatalf("publish-first result out=%+v err=%v", published.out, published.err)
		}
		if err := awaitNodeConfigPG18LifecycleCall(t, lifecycleResult); err != nil {
			t.Fatalf("publish-first retirement: %v", err)
		}
		assertNodeConfigPG18RetiredState(t, ctx, admin, service, fx.tenant, nodeID, 1, 1)
		t.Log("marker=node_config_pg18_life_publish_then_retire_ok")
	})

	t.Run("retirement serializes before publish", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19402, "retire-publish")
		var rowVersion int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, nodeID).Scan(&rowVersion); err != nil {
			t.Fatalf("read retire-publish row version: %v", err)
		}
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		var lockedNode string
		if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("hold retire-publish node lock: %v", err)
		}
		lifecycleResult := make(chan error, 1)
		go func() {
			lifecycleResult <- service.BatchAdminNodeLifecycle(ctx, fx.tenant, nodefabric.BatchNodeLifecycleInput{
				ActorID: fx.actor, ServingStatus: "retired", Reason: "pg18-retire-first",
				Items: []nodefabric.BatchNodeLifecycleItem{{ID: nodeID, RowVersion: rowVersion}},
			})
		}()
		retirePID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%SELECT n.serving_status,n.row_version%FOR UPDATE OF n%`)
		publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
		go func() {
			out, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "node", ScopeRef: nodeID,
				Payload: json.RawMessage(`{"life":"retire-first"}`),
			})
			publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
		}()
		_ = waitNodeConfigPG18BlockedPID(t, ctx, admin, retirePID,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		holder.release(t)
		if err := awaitNodeConfigPG18LifecycleCall(t, lifecycleResult); err != nil {
			t.Fatalf("retire-first retirement: %v", err)
		}
		published := awaitNodeConfigPG18PublishCall(t, publishResult)
		if published.out != nil {
			t.Fatalf("retire-first publish returned output %+v", published.out)
		}
		assertNodeConfigPG18NotFoundNeutral(t, "retire-first publish", published.err)
		assertNodeConfigPG18RetiredState(t, ctx, admin, service, fx.tenant, nodeID, 0, 0)
		t.Log("marker=node_config_pg18_life_retire_then_publish_ok")
	})
}

func runNodeConfigPG18PoolLifecycleRaceBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appDSN string, signer *platformcrypto.Signer) {
	t.Helper()

	for _, commitDisable := range []bool{true, false} {
		commitDisable := commitDisable
		name := "rollback"
		if commitDisable {
			name = "commit"
		}
		t.Run("pool disable "+name, func(t *testing.T) {
			caseName := "nodecfg/life01-" + name
			workerA := openNodeConfigPG18NamedPool(t, ctx, appDSN, caseName+"/A")
			defer workerA.Close()
			workerB := openNodeConfigPG18NamedPool(t, ctx, appDSN, caseName+"/B")
			defer workerB.Close()
			service := nodefabric.NewService(workerB, signer)
			fx := seedNodeConfigPG18Fixture(t, ctx, admin)
			nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19501, "life01-"+name)
			holder := beginNodeConfigPG18LockHolder(t, ctx, workerA, fx)
			defer holder.cleanup()
			var poolID string
			if err := holder.tx.QueryRow(ctx, `UPDATE node_pools
				SET status='disabled',updated_at=now()
				WHERE tenant_id=$1 AND id=$2::uuid RETURNING id::text`, fx.tenant, fx.pool).Scan(&poolID); err != nil {
				t.Fatalf("stage pool disable: %v", err)
			}

			result := make(chan nodeConfigPG18PublishCallResult, 1)
			go func() {
				out, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
					ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
					Payload: json.RawMessage(`{"winner":"pool","life01":1}`),
				})
				result <- nodeConfigPG18PublishCallResult{out: out, err: err}
			}()
			waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
				`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
			assertNodeConfigPG18ApplicationName(t, ctx, admin, holder.pid, caseName+"/A")
			assertNodeConfigPG18ApplicationName(t, ctx, admin, waiterPID, caseName+"/B")
			if commitDisable {
				holder.commit(t)
			} else {
				holder.release(t)
			}
			published := awaitNodeConfigPG18PublishCall(t, result)
			assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)

			var poolStatus string
			var desired *int
			var configs, publishAudits, applications int
			if err := admin.QueryRow(ctx, `SELECT p.status,n.desired_config_version,
				(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND scope='pool' AND scope_ref=$2),
				(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish'),
				(SELECT count(*) FROM node_config_applications WHERE tenant_id=$1 AND node_id=$3)
				FROM node_pools p JOIN nodes n ON n.tenant_id=p.tenant_id AND n.pool_id=p.id
				WHERE p.tenant_id=$1 AND p.id=$2`, fx.tenant, fx.pool, nodeID).
				Scan(&poolStatus, &desired, &configs, &publishAudits, &applications); err != nil {
				t.Fatalf("inspect LIFE-01 result: %v", err)
			}
			if commitDisable {
				if published.out != nil {
					t.Fatalf("disabled-pool publish returned %+v", published.out)
				}
				assertNodeConfigPG18NotFoundNeutral(t, "disabled pool publish", published.err)
				if poolStatus != "disabled" || desired != nil || configs != 0 || publishAudits != 0 || applications != 0 {
					t.Fatalf("commit result status/desired/config/audit/apps=%s/%v/%d/%d/%d",
						poolStatus, desired, configs, publishAudits, applications)
				}
				t.Log("marker=node_config_pg18_life_pool_disable_commit_refusal_ok")
				return
			}
			if published.err != nil || published.out == nil || published.out.AffectedNodes != 1 {
				t.Fatalf("rollback publish out=%+v err=%v", published.out, published.err)
			}
			if poolStatus != "active" || desired == nil || *desired != published.out.Version ||
				configs != 1 || publishAudits != 1 || applications != 0 {
				t.Fatalf("rollback result status/desired/config/audit/apps=%s/%v/%d/%d/%d",
					poolStatus, desired, configs, publishAudits, applications)
			}
			assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, nodeID,
				published.out.Version, []string{fmt.Sprintf("pool@v%d", published.out.Version)},
				"pool", map[string]int{"life01": 1})
			t.Log("marker=node_config_pg18_life_pool_disable_rollback_publish_ok")
		})
	}
}

func runNodeConfigPG18GlobalPoolRetirementRaceBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appDSN string, signer *platformcrypto.Signer) {
	t.Helper()
	for scopeIndex, scope := range []string{"global", "pool"} {
		for orderIndex, publishFirst := range []bool{true, false} {
			scope, publishFirst := scope, publishFirst
			order := "retire_then_publish"
			if publishFirst {
				order = "publish_then_retire"
			}
			t.Run(scope+" "+order, func(t *testing.T) {
				caseName := "nodecfg/life03-" + scope + "-" + order
				holderPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, caseName+"/H")
				defer holderPool.Close()
				publishPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, caseName+"/A")
				defer publishPool.Close()
				retirePool := openNodeConfigPG18NamedPool(t, ctx, appDSN, caseName+"/B")
				defer retirePool.Close()
				publishService := nodefabric.NewService(publishPool, signer)
				retireService := nodefabric.NewService(retirePool, signer)
				fx := seedNodeConfigPG18Fixture(t, ctx, admin)
				port := 19601 + scopeIndex*10 + orderIndex
				nodeID := createNodeConfigPG18Node(t, ctx, publishService, fx, fx.pool, port,
					"life03-"+scope+"-"+order)
				var rowVersion int64
				if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE tenant_id=$1 AND id=$2`,
					fx.tenant, nodeID).Scan(&rowVersion); err != nil {
					t.Fatalf("read LIFE-03 row version: %v", err)
				}
				ref := ""
				// 投影阶段先枚举并锁住本 scope 的节点，UPDATE 在那之后；
				// 持锁方挡住的是这条 SELECT。
				waitLike := `%SELECT id FROM nodes%serving_status<>'retired'%`
				if scope == "pool" {
					ref = fx.pool
					waitLike = `%SELECT id FROM nodes%pool_id=$2::uuid%`
				}

				holder := beginNodeConfigPG18LockHolder(t, ctx, holderPool, fx)
				defer holder.cleanup()
				var locked string
				if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
					WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&locked); err != nil {
					t.Fatalf("hold LIFE-03 node: %v", err)
				}
				publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
				lifecycleResult := make(chan error, 1)
				startPublish := func() {
					go func() {
						out, err := publishService.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
							ActorID: fx.actor, Scope: scope, ScopeRef: ref,
							Payload: json.RawMessage(fmt.Sprintf(`{"winner":%q,"life03":1}`, scope)),
						})
						publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
					}()
				}
				startRetire := func() {
					go func() {
						lifecycleResult <- retireService.BatchAdminNodeLifecycle(ctx, fx.tenant,
							nodefabric.BatchNodeLifecycleInput{ActorID: fx.actor, ServingStatus: "retired",
								Reason: "pg18-life03-" + order,
								Items:  []nodefabric.BatchNodeLifecycleItem{{ID: nodeID, RowVersion: rowVersion}}})
					}()
				}

				var publishPID, retirePID int
				if publishFirst {
					startPublish()
					publishPID = waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid, waitLike)
					startRetire()
					retirePID = waitNodeConfigPG18BlockedPID(t, ctx, admin, publishPID,
						`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
				} else {
					startRetire()
					retirePID = waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
						`%SELECT n.serving_status,n.row_version%FOR UPDATE OF n%`)
					startPublish()
					publishPID = waitNodeConfigPG18BlockedPID(t, ctx, admin, retirePID,
						`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
				}
				assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, caseName+"/A")
				assertNodeConfigPG18ApplicationName(t, ctx, admin, retirePID, caseName+"/B")
				holder.release(t)
				var published nodeConfigPG18PublishCallResult
				if publishFirst {
					published = awaitNodeConfigPG18PublishCall(t, publishResult)
					if err := awaitNodeConfigPG18LifecycleCall(t, lifecycleResult); err != nil {
						t.Fatalf("LIFE-03 retirement: %v", err)
					}
				} else {
					if err := awaitNodeConfigPG18LifecycleCall(t, lifecycleResult); err != nil {
						t.Fatalf("LIFE-03 retirement: %v", err)
					}
					published = awaitNodeConfigPG18PublishCall(t, publishResult)
				}
				wantAffected := 0
				if publishFirst {
					wantAffected = 1
				}
				if published.err != nil || published.out == nil || published.out.AffectedNodes != wantAffected {
					t.Fatalf("LIFE-03 publish out=%+v err=%v want affected=%d",
						published.out, published.err, wantAffected)
				}
				assertNodeConfigPG18WaiterClean(t, ctx, admin, publishPID)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, retirePID)
				assertNodeConfigPG18RetiredState(t, ctx, admin, publishService, fx.tenant, nodeID, 1, 1)

				var exactConfigs, applications, lifecycleAudits int
				if err := admin.QueryRow(ctx, `SELECT
					(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND scope=$2
					 AND scope_ref IS NOT DISTINCT FROM nullif($3,'')::uuid
					 AND status='published' AND version=$4),
					(SELECT count(*) FROM node_config_applications WHERE tenant_id=$1 AND node_id=$5),
					(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.status_batch')`,
					fx.tenant, scope, ref, published.out.Version, nodeID).
					Scan(&exactConfigs, &applications, &lifecycleAudits); err != nil {
					t.Fatalf("inspect LIFE-03 evidence: %v", err)
				}
				if exactConfigs != 1 || applications != 0 || lifecycleAudits != 1 {
					t.Fatalf("LIFE-03 exact config/apps/lifecycle audits=%d/%d/%d",
						exactConfigs, applications, lifecycleAudits)
				}
				t.Logf("marker=node_config_pg18_life_%s_%s_ok", scope, order)
			})
		}
	}
}

func runNodeConfigPG18PoolDeleteRaceBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appDSN string, signer *platformcrypto.Signer) {
	t.Helper()

	serverState := func(t *testing.T, tenantID, serverID string) string {
		t.Helper()
		var state string
		if err := admin.QueryRow(ctx, `SELECT jsonb_build_object(
			'status',status,'deleted_at',deleted_at,'control_node_id',control_node_id,
			'row_version',row_version)::text FROM servers WHERE tenant_id=$1 AND id=$2`,
			tenantID, serverID).Scan(&state); err != nil {
			t.Fatalf("read pool delete server state: %v", err)
		}
		return state
	}
	assertState := func(t *testing.T, tenantID, poolID, serverID, wantServerState string,
		wantPools, wantConfigs, wantNodes, wantDeleteAudits, wantPublishAudits int) {
		t.Helper()
		var pools, configs, orphanConfigs, nodes, plans, templates, activeTokens, applications, identities int
		var deleteAudits, publishAudits int
		var gotServerState string
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_pools WHERE tenant_id=$1 AND id=$2),
			(SELECT count(*) FROM node_configs WHERE tenant_id=$1 AND scope='pool' AND scope_ref=$2),
			(SELECT count(*) FROM node_configs c LEFT JOIN node_pools p
			 ON p.tenant_id=c.tenant_id AND p.id=c.scope_ref
			 WHERE c.tenant_id=$1 AND c.scope='pool' AND c.scope_ref IS NOT NULL AND p.id IS NULL),
			(SELECT count(*) FROM nodes WHERE tenant_id=$1 AND pool_id=$2),
			(SELECT count(*) FROM plan_node_pools WHERE tenant_id=$1 AND pool_id=$2),
			(SELECT count(*) FROM node_templates WHERE tenant_id=$1 AND default_pool_id=$2),
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND pool_id=$2
			 AND consumed_at IS NULL AND expires_at>now() AND used_count<max_uses),
			(SELECT count(*) FROM node_config_applications a JOIN nodes n
			 ON n.tenant_id=a.tenant_id AND n.id=a.node_id WHERE n.tenant_id=$1 AND n.pool_id=$2),
			(SELECT count(*) FROM node_identities i JOIN nodes n
			 ON n.tenant_id=i.tenant_id AND n.id=i.node_id WHERE n.tenant_id=$1 AND n.pool_id=$2),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1
			 AND action='node_pool.deleted' AND resource_id=$2),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish'),
			(SELECT jsonb_build_object('status',status,'deleted_at',deleted_at,
			 'control_node_id',control_node_id,'row_version',row_version)::text
			 FROM servers WHERE tenant_id=$1 AND id=$3)`,
			tenantID, poolID, serverID).Scan(&pools, &configs, &orphanConfigs, &nodes, &plans,
			&templates, &activeTokens, &applications, &identities, &deleteAudits, &publishAudits,
			&gotServerState); err != nil {
			t.Fatalf("inspect pool delete race state: %v", err)
		}
		if pools != wantPools || configs != wantConfigs || orphanConfigs != 0 || nodes != wantNodes ||
			plans != 0 || templates != 0 || activeTokens != 0 || applications != 0 || identities != 0 ||
			deleteAudits != wantDeleteAudits || publishAudits != wantPublishAudits ||
			gotServerState != wantServerState {
			t.Fatalf("pool delete state pools/configs/orphans/nodes/plans/templates/tokens/apps/identities/"+
				"delete_audit/publish_audit/server=%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%q "+
				"want=%d/%d/0/%d/0/0/0/0/0/%d/%d/%q", pools, configs, orphanConfigs, nodes,
				plans, templates, activeTokens, applications, identities, deleteAudits, publishAudits,
				gotServerState, wantPools, wantConfigs, wantNodes, wantDeleteAudits, wantPublishAudits, wantServerState)
		}
	}
	publishGlobal := func(t *testing.T, opCtx context.Context, service *nodefabric.Service,
		fx nodeConfigPG18Fixture, label string) *nodefabric.PublishOutput {
		t.Helper()
		out, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global",
			Payload: json.RawMessage(`{"winner":"global","global_seq":1,"race":"` + label + `"}`),
		})
		if err != nil || out == nil {
			t.Fatalf("publish DEL-04 global baseline out=%+v err=%v", out, err)
		}
		return out
	}
	createInput := func(fx nodeConfigPG18Fixture, label string, port int) nodefabric.CreateAdminNodeInput {
		return nodefabric.CreateAdminNodeInput{
			ActorID: fx.actor, Name: label + "-" + fx.suffix, ServerID: fx.server, PoolID: fx.pool,
			NodeType: "shadowsocks", ServerHost: "edge.example.test", ServerPort: port,
			Kernel: "auto", TrafficRate: 1, DisplayName: label + " " + fx.suffix,
			ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`), SortOrder: port,
		}
	}
	assertAdminNode := func(t *testing.T, label string, out *nodefabric.AdminNode,
		fx nodeConfigPG18Fixture) {
		t.Helper()
		if out == nil || out.ID == "" || out.Name != label+"-"+fx.suffix || out.PoolID == nil ||
			*out.PoolID != fx.pool || out.ServerID == nil || *out.ServerID != fx.server ||
			out.Status != "draft" || out.ServingStatus != "draft" || out.RowVersion != 1 {
			t.Fatalf("DEL-04 node=%+v", out)
		}
	}
	assertAuditCount := func(t *testing.T, tenantID, action, resourceID string, want int) {
		t.Helper()
		var got int
		query := `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`
		args := []any{tenantID, action}
		if resourceID != "" {
			query += ` AND resource_id=$3::uuid`
			args = append(args, resourceID)
		}
		if err := admin.QueryRow(ctx, query, args...).Scan(&got); err != nil {
			t.Fatalf("inspect DEL-04 audit %s: %v", action, err)
		}
		if got != want {
			t.Fatalf("DEL-04 audit %s count=%d want=%d", action, got, want)
		}
	}

	t.Run("DEL-01 empty delete then publish refusal", func(t *testing.T) {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		wantServerState := serverState(t, fx.tenant, fx.server)
		deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, "nodecfg/del01/A")
		defer deletePool.Close()
		publishPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, "nodecfg/del01/B")
		defer publishPool.Close()
		deleted := callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool, "nodecfg-pg18-del01")
		assertNodeConfigPG18HTTPOK(t, deleted)
		service := nodefabric.NewService(publishPool, signer)
		out, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
			Payload: json.RawMessage(`{"winner":"pool","del01":1}`),
		})
		if out != nil {
			t.Fatalf("DEL-01 publish after delete returned %+v", out)
		}
		assertNodeConfigPG18NotFoundNeutral(t, "DEL-01 publish after delete", err)
		assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 0, 0, 0, 1, 0)
		assertNodeConfigPG18PoolsReleased(t, deletePool, publishPool)
		t.Log("marker=node_config_pg18_del01_empty_delete_publish_refusal_ok")
	})

	const del03Rounds = 25
	t.Run("DEL-03 delete serializes before publish", func(t *testing.T) {
		for round := 0; round < del03Rounds; round++ {
			t.Run(fmt.Sprintf("round-%02d", round), func(t *testing.T) {
				opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				fx := seedNodeConfigPG18Fixture(t, ctx, admin)
				wantServerState := serverState(t, fx.tenant, fx.server)
				caseName := fmt.Sprintf("nodecfg/del03-delete-first-%02d", round)
				holderPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/H")
				defer holderPool.Close()
				deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/A")
				defer deletePool.Close()
				publishPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/B")
				defer publishPool.Close()
				holder := beginNodeConfigPG18LockHolder(t, opCtx, holderPool, fx)
				defer holder.cleanup()
				var locked string
				if err := holder.tx.QueryRow(opCtx, `SELECT id::text FROM node_pools
			WHERE tenant_id=$1 AND id=$2::uuid FOR SHARE`, fx.tenant, fx.pool).Scan(&locked); err != nil {
					t.Fatalf("hold DEL-03 pool share lock: %v", err)
				}
				deleteResult := make(chan nodeConfigPG18HTTPCallResult, 1)
				go func() {
					deleteResult <- callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool,
						fmt.Sprintf("nodecfg-pg18-del03-delete-first-%02d", round))
				}()
				deletePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, holder.pid,
					`%SELECT id::text FROM node_pools%FOR UPDATE%`)
				publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
				service := nodefabric.NewService(publishPool, signer)
				go func() {
					out, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
						ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
						Payload: json.RawMessage(`{"winner":"pool","del03":1}`),
					})
					publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
				}()
				publishPID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, deletePID,
					`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
				assertNodeConfigPG18ApplicationName(t, opCtx, admin, holder.pid, caseName+"/H")
				assertNodeConfigPG18ApplicationName(t, opCtx, admin, deletePID, caseName+"/A")
				assertNodeConfigPG18ApplicationName(t, opCtx, admin, publishPID, caseName+"/B")
				holder.release(t)
				deleted := awaitNodeConfigPG18HTTPCall(t, deleteResult)
				assertNodeConfigPG18HTTPOK(t, deleted)
				published := awaitNodeConfigPG18PublishCall(t, publishResult)
				if published.out != nil {
					t.Fatalf("DEL-03 delete-first publish returned %+v", published.out)
				}
				assertNodeConfigPG18NotFoundNeutral(t, "DEL-03 delete-first publish", published.err)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, deletePID)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, publishPID)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, holder.pid)
				assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 0, 0, 0, 1, 0)
				assertNodeConfigPG18PoolsReleased(t, holderPool, deletePool, publishPool)
			})
		}
		t.Log("marker=node_config_pg18_del03_delete_then_publish_ok")
	})

	t.Run("DEL-03 publish serializes before delete", func(t *testing.T) {
		for round := 0; round < del03Rounds; round++ {
			t.Run(fmt.Sprintf("round-%02d", round), func(t *testing.T) {
				opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				fx := seedNodeConfigPG18Fixture(t, ctx, admin)
				wantServerState := serverState(t, fx.tenant, fx.server)
				caseName := fmt.Sprintf("nodecfg/del03-publish-first-%02d", round)
				holderPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/H")
				defer holderPool.Close()
				publishPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/A")
				defer publishPool.Close()
				deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/B")
				defer deletePool.Close()
				holder := beginNodeConfigPG18LockHolder(t, opCtx, holderPool, fx)
				defer holder.cleanup()
				var locked string
				if err := holder.tx.QueryRow(opCtx, `SELECT id::text FROM node_pools
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, fx.pool).Scan(&locked); err != nil {
					t.Fatalf("hold DEL-03 pool update lock: %v", err)
				}
				publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
				service := nodefabric.NewService(publishPool, signer)
				go func() {
					out, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
						ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
						Payload: json.RawMessage(`{"winner":"pool","del03":1}`),
					})
					publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
				}()
				publishPID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, holder.pid,
					`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
				deleteResult := make(chan nodeConfigPG18HTTPCallResult, 1)
				go func() {
					deleteResult <- callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool,
						fmt.Sprintf("nodecfg-pg18-del03-publish-first-%02d", round))
				}()
				deletePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, publishPID,
					`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
				assertNodeConfigPG18ApplicationName(t, opCtx, admin, holder.pid, caseName+"/H")
				assertNodeConfigPG18ApplicationName(t, opCtx, admin, publishPID, caseName+"/A")
				assertNodeConfigPG18ApplicationName(t, opCtx, admin, deletePID, caseName+"/B")
				holder.release(t)
				published := awaitNodeConfigPG18PublishCall(t, publishResult)
				if published.err != nil || published.out == nil || published.out.AffectedNodes != 0 {
					t.Fatalf("DEL-03 publish-first out=%+v err=%v", published.out, published.err)
				}
				deleted := awaitNodeConfigPG18HTTPCall(t, deleteResult)
				if deleted.code != http.StatusConflict {
					t.Fatalf("DEL-03 publish-first delete status=%d body=%s", deleted.code, string(deleted.body))
				}
				assertNodeConfigPG18HTTPErrorCode(t, deleted, httpx.CodeConflict)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, publishPID)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, deletePID)
				assertNodeConfigPG18WaiterClean(t, ctx, admin, holder.pid)
				assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 1, 1, 0, 0, 1)
				assertNodeConfigPG18PoolsReleased(t, holderPool, publishPool, deletePool)
			})
		}
		t.Log("marker=node_config_pg18_del03_publish_then_delete_ok")
	})

	t.Run("DEL-04 delete serializes before create", func(t *testing.T) {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		wantServerState := serverState(t, fx.tenant, fx.server)
		caseName := "nodecfg/del04-delete-create"
		holderPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/H")
		defer holderPool.Close()
		deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/A")
		defer deletePool.Close()
		createPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/B")
		defer createPool.Close()
		service := nodefabric.NewService(createPool, signer)
		global := publishGlobal(t, opCtx, service, fx, "delete-create")
		holder := beginNodeConfigPG18LockHolder(t, opCtx, holderPool, fx)
		defer holder.cleanup()
		var locked string
		if err := holder.tx.QueryRow(opCtx, `SELECT id::text FROM node_pools
			WHERE tenant_id=$1 AND id=$2::uuid FOR SHARE`, fx.tenant, fx.pool).Scan(&locked); err != nil {
			t.Fatalf("hold DEL-04 delete-create pool share lock: %v", err)
		}
		deleteResult := make(chan nodeConfigPG18HTTPCallResult, 1)
		go func() {
			deleteResult <- callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool,
				"nodecfg-pg18-del04-delete-create")
		}()
		deletePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, holder.pid,
			`%SELECT id::text FROM node_pools%FOR UPDATE%`)
		createResult := make(chan nodeConfigPG18AdminNodeCallResult, 1)
		go func() {
			out, err := service.CreateAdminNode(httpx.WithRequestID(opCtx, "nodecfg-pg18-del04-create-loser"),
				fx.tenant, createInput(fx, "del04-create-loser", 19401))
			createResult <- nodeConfigPG18AdminNodeCallResult{out: out, err: err}
		}()
		createPID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, deletePID,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, holder.pid, caseName+"/H")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, deletePID, caseName+"/A")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, createPID, caseName+"/B")
		holder.release(t)
		assertNodeConfigPG18HTTPOK(t, awaitNodeConfigPG18HTTPCall(t, deleteResult))
		assertNodeConfigPG18PoolValidation(t, "DEL-04 delete-create", awaitNodeConfigPG18AdminNodeCall(t, createResult))
		assertNodeConfigPG18WaiterClean(t, ctx, admin, holder.pid)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, deletePID)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, createPID)
		assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 0, 0, 0, 1, 1)
		assertAuditCount(t, fx.tenant, "node.create", "", 0)
		var loserNodes int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND name=$2`,
			fx.tenant, "del04-create-loser-"+fx.suffix).Scan(&loserNodes); err != nil || loserNodes != 0 {
			t.Fatalf("DEL-04 losing create nodes=%d err=%v", loserNodes, err)
		}
		if global.Version <= 0 {
			t.Fatal("DEL-04 delete-create lost global baseline")
		}
		assertNodeConfigPG18PoolsReleased(t, holderPool, deletePool, createPool)
	})

	t.Run("DEL-04 create serializes before delete", func(t *testing.T) {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		wantServerState := serverState(t, fx.tenant, fx.server)
		caseName := "nodecfg/del04-create-delete"
		holderPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/H")
		defer holderPool.Close()
		createPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/A")
		defer createPool.Close()
		deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/B")
		defer deletePool.Close()
		service := nodefabric.NewService(createPool, signer)
		global := publishGlobal(t, opCtx, service, fx, "create-delete")
		holder := beginNodeConfigPG18LockHolder(t, opCtx, holderPool, fx)
		defer holder.cleanup()
		var locked string
		if err := holder.tx.QueryRow(opCtx, `SELECT id::text FROM node_pools
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, fx.pool).Scan(&locked); err != nil {
			t.Fatalf("hold DEL-04 create-delete pool update lock: %v", err)
		}
		createResult := make(chan nodeConfigPG18AdminNodeCallResult, 1)
		go func() {
			out, err := service.CreateAdminNode(httpx.WithRequestID(opCtx, "nodecfg-pg18-del04-create-winner"),
				fx.tenant, createInput(fx, "del04-create-winner", 19402))
			createResult <- nodeConfigPG18AdminNodeCallResult{out: out, err: err}
		}()
		createPID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, holder.pid,
			`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
		deleteResult := make(chan nodeConfigPG18HTTPCallResult, 1)
		go func() {
			deleteResult <- callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool,
				"nodecfg-pg18-del04-create-delete")
		}()
		deletePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, createPID,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, holder.pid, caseName+"/H")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, createPID, caseName+"/A")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, deletePID, caseName+"/B")
		holder.release(t)
		created := awaitNodeConfigPG18AdminNodeCall(t, createResult)
		if created.err != nil {
			t.Fatalf("DEL-04 create winner: %v", created.err)
		}
		assertAdminNode(t, "del04-create-winner", created.out, fx)
		deleted := awaitNodeConfigPG18HTTPCall(t, deleteResult)
		if deleted.code != http.StatusConflict {
			t.Fatalf("DEL-04 create-delete status=%d body=%s", deleted.code, string(deleted.body))
		}
		assertNodeConfigPG18HTTPErrorCode(t, deleted, httpx.CodeConflict)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, holder.pid)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, createPID)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, deletePID)
		assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 1, 0, 1, 0, 1)
		assertAuditCount(t, fx.tenant, "node.create", created.out.ID, 1)
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, created.out.ID,
			global.Version, []string{fmt.Sprintf("global@v%d", global.Version)}, "global", map[string]int{"global_seq": 1})
		assertNodeConfigPG18PoolsReleased(t, holderPool, createPool, deletePool)
	})
	t.Log("marker=node_config_pg18_del04_create_delete_serialization_ok")

	t.Run("DEL-04 delete serializes before clone", func(t *testing.T) {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		wantServerState := serverState(t, fx.tenant, fx.server)
		caseName := "nodecfg/del04-delete-clone"
		holderPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/H")
		defer holderPool.Close()
		deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/A")
		defer deletePool.Close()
		clonePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/B")
		defer clonePool.Close()
		service := nodefabric.NewService(clonePool, signer)
		global := publishGlobal(t, opCtx, service, fx, "delete-clone")
		sourceID := createNodeConfigPG18Node(t, opCtx, service, fx, "", 19403, "del04-source-loser")
		source, err := service.GetAdminNode(opCtx, fx.tenant, sourceID)
		if err != nil || source == nil {
			t.Fatalf("read DEL-04 clone source out=%+v err=%v", source, err)
		}
		var sourceBefore string
		if err := admin.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM nodes n WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, sourceID).Scan(&sourceBefore); err != nil {
			t.Fatalf("snapshot DEL-04 clone source: %v", err)
		}
		holder := beginNodeConfigPG18LockHolder(t, opCtx, holderPool, fx)
		defer holder.cleanup()
		var locked string
		if err := holder.tx.QueryRow(opCtx, `SELECT id::text FROM node_pools
			WHERE tenant_id=$1 AND id=$2::uuid FOR SHARE`, fx.tenant, fx.pool).Scan(&locked); err != nil {
			t.Fatalf("hold DEL-04 delete-clone pool share lock: %v", err)
		}
		deleteResult := make(chan nodeConfigPG18HTTPCallResult, 1)
		go func() {
			deleteResult <- callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool,
				"nodecfg-pg18-del04-delete-clone")
		}()
		deletePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, holder.pid,
			`%SELECT id::text FROM node_pools%FOR UPDATE%`)
		cloneResult := make(chan nodeConfigPG18AdminNodeCallResult, 1)
		go func() {
			out, err := service.CloneAdminNode(httpx.WithRequestID(opCtx, "nodecfg-pg18-del04-clone-loser"),
				fx.tenant, sourceID, nodefabric.CloneAdminNodeInput{
					ActorID: fx.actor, Name: "del04-clone-loser-" + fx.suffix,
					RowVersion: source.RowVersion, PoolID: fx.pool,
				})
			cloneResult <- nodeConfigPG18AdminNodeCallResult{out: out, err: err}
		}()
		clonePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, deletePID,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, holder.pid, caseName+"/H")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, deletePID, caseName+"/A")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, clonePID, caseName+"/B")
		holder.release(t)
		assertNodeConfigPG18HTTPOK(t, awaitNodeConfigPG18HTTPCall(t, deleteResult))
		assertNodeConfigPG18PoolValidation(t, "DEL-04 delete-clone", awaitNodeConfigPG18AdminNodeCall(t, cloneResult))
		assertNodeConfigPG18WaiterClean(t, ctx, admin, holder.pid)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, deletePID)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, clonePID)
		assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 0, 0, 0, 1, 1)
		assertAuditCount(t, fx.tenant, "node.copy", "", 0)
		var sourceAfter string
		if err := admin.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM nodes n WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, sourceID).Scan(&sourceAfter); err != nil || sourceAfter != sourceBefore {
			t.Fatalf("DEL-04 failed clone changed source equal=%t err=%v", sourceAfter == sourceBefore, err)
		}
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, sourceID,
			global.Version, []string{fmt.Sprintf("global@v%d", global.Version)}, "global", map[string]int{"global_seq": 1})
		assertNodeConfigPG18PoolsReleased(t, holderPool, deletePool, clonePool)
	})

	t.Run("DEL-04 clone serializes before delete", func(t *testing.T) {
		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		wantServerState := serverState(t, fx.tenant, fx.server)
		caseName := "nodecfg/del04-clone-delete"
		holderPool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/H")
		defer holderPool.Close()
		clonePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/A")
		defer clonePool.Close()
		deletePool := openNodeConfigPG18NamedPool(t, opCtx, appDSN, caseName+"/B")
		defer deletePool.Close()
		service := nodefabric.NewService(clonePool, signer)
		global := publishGlobal(t, opCtx, service, fx, "clone-delete")
		sourceID := createNodeConfigPG18Node(t, opCtx, service, fx, "", 19404, "del04-source-winner")
		source, err := service.GetAdminNode(opCtx, fx.tenant, sourceID)
		if err != nil || source == nil {
			t.Fatalf("read DEL-04 clone source out=%+v err=%v", source, err)
		}
		var sourceBefore string
		if err := admin.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM nodes n WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, sourceID).Scan(&sourceBefore); err != nil {
			t.Fatalf("snapshot DEL-04 clone source: %v", err)
		}
		holder := beginNodeConfigPG18LockHolder(t, opCtx, holderPool, fx)
		defer holder.cleanup()
		var locked string
		if err := holder.tx.QueryRow(opCtx, `SELECT id::text FROM node_pools
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, fx.pool).Scan(&locked); err != nil {
			t.Fatalf("hold DEL-04 clone-delete pool update lock: %v", err)
		}
		cloneResult := make(chan nodeConfigPG18AdminNodeCallResult, 1)
		go func() {
			out, err := service.CloneAdminNode(httpx.WithRequestID(opCtx, "nodecfg-pg18-del04-clone-winner"),
				fx.tenant, sourceID, nodefabric.CloneAdminNodeInput{
					ActorID: fx.actor, Name: "del04-clone-winner-" + fx.suffix,
					RowVersion: source.RowVersion, PoolID: fx.pool,
				})
			cloneResult <- nodeConfigPG18AdminNodeCallResult{out: out, err: err}
		}()
		clonePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, holder.pid,
			`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
		deleteResult := make(chan nodeConfigPG18HTTPCallResult, 1)
		go func() {
			deleteResult <- callNodeConfigPG18DeletePool(opCtx, deletePool, fx, fx.pool,
				"nodecfg-pg18-del04-clone-delete")
		}()
		deletePID := waitNodeConfigPG18BlockedPID(t, opCtx, admin, clonePID,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, holder.pid, caseName+"/H")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, clonePID, caseName+"/A")
		assertNodeConfigPG18ApplicationName(t, opCtx, admin, deletePID, caseName+"/B")
		holder.release(t)
		cloned := awaitNodeConfigPG18AdminNodeCall(t, cloneResult)
		if cloned.err != nil {
			t.Fatalf("DEL-04 clone winner: %v", cloned.err)
		}
		assertAdminNode(t, "del04-clone-winner", cloned.out, fx)
		deleted := awaitNodeConfigPG18HTTPCall(t, deleteResult)
		if deleted.code != http.StatusConflict {
			t.Fatalf("DEL-04 clone-delete status=%d body=%s", deleted.code, string(deleted.body))
		}
		assertNodeConfigPG18HTTPErrorCode(t, deleted, httpx.CodeConflict)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, holder.pid)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, clonePID)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, deletePID)
		assertState(t, fx.tenant, fx.pool, fx.server, wantServerState, 1, 0, 1, 0, 1)
		assertAuditCount(t, fx.tenant, "node.copy", cloned.out.ID, 1)
		var sourceAfter string
		if err := admin.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM nodes n WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, sourceID).Scan(&sourceAfter); err != nil || sourceAfter != sourceBefore {
			t.Fatalf("DEL-04 successful clone changed source equal=%t err=%v", sourceAfter == sourceBefore, err)
		}
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, cloned.out.ID,
			global.Version, []string{fmt.Sprintf("global@v%d", global.Version)}, "global", map[string]int{"global_seq": 1})
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, sourceID,
			global.Version, []string{fmt.Sprintf("global@v%d", global.Version)}, "global", map[string]int{"global_seq": 1})
		assertNodeConfigPG18PoolsReleased(t, holderPool, clonePool, deletePool)
	})
	t.Log("marker=node_config_pg18_del04_clone_delete_serialization_ok")
}

func runNodeConfigPG18NewMaterializationRaceBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appDSN string, signer *platformcrypto.Signer) {
	t.Helper()

	t.Run("NEW-02 create and publish serialize in both orders", func(t *testing.T) {
		for _, createFirst := range []bool{true, false} {
			order := "publish-first"
			if createFirst {
				order = "create-first"
			}
			holderPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, "nodecfg/new02/"+order+"/H")
			createPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, "nodecfg/new02/"+order+"/A")
			publishPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, "nodecfg/new02/"+order+"/B")
			holderName, createName, publishName := "nodecfg/new02/"+order+"/H", "nodecfg/new02/"+order+"/A", "nodecfg/new02/"+order+"/B"
			createService := nodefabric.NewService(createPool, signer)
			publishService := nodefabric.NewService(publishPool, signer)

			for round := 1; round <= 50; round++ {
				fx := seedNodeConfigPG18Fixture(t, ctx, admin)
				holder := beginNodeConfigPG18LockHolder(t, ctx, holderPool, fx)
				func() {
					defer holder.cleanup()
					if createFirst {
						if _, err := holder.tx.Exec(ctx, `SELECT id FROM servers
							WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, fx.tenant, fx.server); err != nil {
							t.Fatalf("NEW-02 create-first hold server round %d: %v", round, err)
						}
					} else if _, err := holder.tx.Exec(ctx, `SELECT id FROM node_pools
						WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, fx.tenant, fx.pool); err != nil {
						t.Fatalf("NEW-02 publish-first hold pool round %d: %v", round, err)
					}
					assertNodeConfigPG18ApplicationName(t, ctx, admin, holder.pid, holderName)

					createResult := make(chan nodeConfigPG18AdminNodeCallResult, 1)
					publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
					create := func() {
						out, err := createService.CreateAdminNode(ctx, fx.tenant, nodefabric.CreateAdminNodeInput{
							ActorID: fx.actor, Name: fmt.Sprintf("new02-%s-%02d-%s", order, round, fx.suffix),
							ServerID: fx.server, PoolID: fx.pool, NodeType: "shadowsocks",
							ServerHost: "edge.example.test", ServerPort: 21000 + round,
							Kernel: "auto", TrafficRate: 1, DisplayName: "NEW-02 " + order,
							ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`), SortOrder: round,
						})
						createResult <- nodeConfigPG18AdminNodeCallResult{out: out, err: err}
					}
					publish := func() {
						out, err := publishService.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
							ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
							Payload: json.RawMessage(fmt.Sprintf(`{"winner":"pool","new02_round":%d}`, round)),
						})
						publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
					}

					if createFirst {
						go create()
						createPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
							`%SELECT s.status,s.capacity_nodes%FOR UPDATE%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, createPID, createName)
						go publish()
						publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, createPID,
							`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, publishName)
					} else {
						go publish()
						publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
							`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, publishName)
						go create()
						createPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, publishPID,
							`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, createPID, createName)
					}
					holder.release(t)
					created := awaitNodeConfigPG18AdminNodeCall(t, createResult)
					published := awaitNodeConfigPG18PublishCall(t, publishResult)
					if created.err != nil || created.out == nil {
						t.Fatalf("NEW-02 %s round %d create out=%+v err=%v", order, round, created.out, created.err)
					}
					if published.err != nil || published.out == nil {
						t.Fatalf("NEW-02 %s round %d publish out=%+v err=%v", order, round, published.out, published.err)
					}
					wantAffected := 0
					if createFirst {
						wantAffected = 1
					}
					if published.out.AffectedNodes != wantAffected {
						t.Fatalf("NEW-02 %s round %d affected=%d want=%d", order, round, published.out.AffectedNodes, wantAffected)
					}
					assertNodeConfigPG18DesiredFetch(t, ctx, admin, publishService, signer, fx.tenant, created.out.ID,
						published.out.Version, []string{fmt.Sprintf("pool@v%d", published.out.Version)}, "pool",
						map[string]int{"new02_round": round})
					var creates, publishes int
					if err := admin.QueryRow(ctx, `SELECT
						count(*) FILTER (WHERE action='node.create'),
						count(*) FILTER (WHERE action='node.config.publish')
						FROM audit_events WHERE tenant_id=$1`, fx.tenant).Scan(&creates, &publishes); err != nil {
						t.Fatalf("NEW-02 %s round %d audits: %v", order, round, err)
					}
					if creates != 1 || publishes != 1 {
						t.Fatalf("NEW-02 %s round %d create/publish audits=%d/%d", order, round, creates, publishes)
					}
				}()
			}
			assertNodeConfigPG18PoolsReleased(t, holderPool, createPool, publishPool)
			holderPool.Close()
			createPool.Close()
			publishPool.Close()
		}
		t.Log("marker=node_config_pg18_new02_create_publish_serialization_ok")
	})

	t.Run("NEW-03 clone and publish serialize without mutating source", func(t *testing.T) {
		for _, cloneFirst := range []bool{true, false} {
			order := "publish-first"
			if cloneFirst {
				order = "clone-first"
			}
			holderPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, "nodecfg/new03/"+order+"/H")
			clonePool := openNodeConfigPG18NamedPool(t, ctx, appDSN, "nodecfg/new03/"+order+"/A")
			publishPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, "nodecfg/new03/"+order+"/B")
			holderName, cloneName, publishName := "nodecfg/new03/"+order+"/H", "nodecfg/new03/"+order+"/A", "nodecfg/new03/"+order+"/B"
			cloneService := nodefabric.NewService(clonePool, signer)
			publishService := nodefabric.NewService(publishPool, signer)

			for round := 1; round <= 50; round++ {
				fx := seedNodeConfigPG18Fixture(t, ctx, admin)
				source, err := cloneService.CreateAdminNode(ctx, fx.tenant, nodefabric.CreateAdminNodeInput{
					ActorID: fx.actor, Name: fmt.Sprintf("new03-source-%02d-%s", round, fx.suffix),
					ServerID: fx.server, NodeType: "shadowsocks", ServerHost: "edge.example.test",
					ServerPort: 22000 + round, Kernel: "auto", TrafficRate: 1, DisplayName: "NEW-03 source",
					ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`), SortOrder: round,
				})
				if err != nil || source == nil {
					t.Fatalf("NEW-03 %s round %d seed source=%+v err=%v", order, round, source, err)
				}
				var sourceBefore string
				if err := admin.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM nodes n
					WHERE tenant_id=$1 AND id=$2`, fx.tenant, source.ID).Scan(&sourceBefore); err != nil {
					t.Fatalf("NEW-03 %s round %d snapshot source: %v", order, round, err)
				}

				holder := beginNodeConfigPG18LockHolder(t, ctx, holderPool, fx)
				func() {
					defer holder.cleanup()
					if cloneFirst {
						if _, err := holder.tx.Exec(ctx, `SELECT id FROM nodes
							WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, fx.tenant, source.ID); err != nil {
							t.Fatalf("NEW-03 clone-first hold source round %d: %v", round, err)
						}
					} else if _, err := holder.tx.Exec(ctx, `SELECT id FROM node_pools
						WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, fx.tenant, fx.pool); err != nil {
						t.Fatalf("NEW-03 publish-first hold pool round %d: %v", round, err)
					}
					assertNodeConfigPG18ApplicationName(t, ctx, admin, holder.pid, holderName)

					cloneResult := make(chan nodeConfigPG18AdminNodeCallResult, 1)
					publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
					clone := func() {
						out, err := cloneService.CloneAdminNode(ctx, fx.tenant, source.ID, nodefabric.CloneAdminNodeInput{
							ActorID: fx.actor, Name: fmt.Sprintf("new03-clone-%s-%02d-%s", order, round, fx.suffix),
							RowVersion: source.RowVersion, TargetServerID: fx.server, PoolID: fx.pool,
						})
						cloneResult <- nodeConfigPG18AdminNodeCallResult{out: out, err: err}
					}
					publish := func() {
						out, err := publishService.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
							ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
							Payload: json.RawMessage(fmt.Sprintf(`{"winner":"pool","new03_round":%d}`, round)),
						})
						publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
					}

					if cloneFirst {
						go clone()
						clonePID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
							`%FROM nodes%FOR UPDATE%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, clonePID, cloneName)
						go publish()
						publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, clonePID,
							`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, publishName)
					} else {
						go publish()
						publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
							`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, publishName)
						go clone()
						clonePID := waitNodeConfigPG18BlockedPID(t, ctx, admin, publishPID,
							`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
						assertNodeConfigPG18ApplicationName(t, ctx, admin, clonePID, cloneName)
					}
					holder.release(t)
					cloned := awaitNodeConfigPG18AdminNodeCall(t, cloneResult)
					published := awaitNodeConfigPG18PublishCall(t, publishResult)
					if cloned.err != nil || cloned.out == nil || published.err != nil || published.out == nil {
						t.Fatalf("NEW-03 %s round %d clone=%+v/%v publish=%+v/%v",
							order, round, cloned.out, cloned.err, published.out, published.err)
					}
					wantAffected := 0
					if cloneFirst {
						wantAffected = 1
					}
					if published.out.AffectedNodes != wantAffected {
						t.Fatalf("NEW-03 %s round %d affected=%d want=%d", order, round, published.out.AffectedNodes, wantAffected)
					}
					assertNodeConfigPG18DesiredFetch(t, ctx, admin, publishService, signer, fx.tenant, cloned.out.ID,
						published.out.Version, []string{fmt.Sprintf("pool@v%d", published.out.Version)}, "pool",
						map[string]int{"new03_round": round})
					var sourceAfter string
					if err := admin.QueryRow(ctx, `SELECT to_jsonb(n)::text FROM nodes n
						WHERE tenant_id=$1 AND id=$2`, fx.tenant, source.ID).Scan(&sourceAfter); err != nil {
						t.Fatalf("NEW-03 %s round %d read source after clone: %v", order, round, err)
					}
					if sourceAfter != sourceBefore {
						t.Fatalf("NEW-03 %s round %d mutated source node", order, round)
					}
					var copies, publishes int
					if err := admin.QueryRow(ctx, `SELECT
						count(*) FILTER (WHERE action='node.copy'),
						count(*) FILTER (WHERE action='node.config.publish')
						FROM audit_events WHERE tenant_id=$1`, fx.tenant).Scan(&copies, &publishes); err != nil {
						t.Fatalf("NEW-03 %s round %d audits: %v", order, round, err)
					}
					if copies != 1 || publishes != 1 {
						t.Fatalf("NEW-03 %s round %d copy/publish audits=%d/%d", order, round, copies, publishes)
					}
				}()
			}
			assertNodeConfigPG18PoolsReleased(t, holderPool, clonePool, publishPool)
			holderPool.Close()
			clonePool.Close()
			publishPool.Close()
		}
		t.Log("marker=node_config_pg18_new03_clone_publish_serialization_ok")
	})

	runNodeConfigPG18BootstrapPublicationRaces(t, ctx, admin, appDSN, signer)
}

func runNodeConfigPG18BootstrapPublicationRaces(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appDSN string, signer *platformcrypto.Signer) {
	t.Helper()
	for _, existing := range []bool{false, true} {
		kind := "new"
		if existing {
			kind = "existing"
		}
		t.Run("NEW-04 "+kind+" bootstrap and publish serialize", func(t *testing.T) {
			for _, bootstrapFirst := range []bool{true, false} {
				order := "publish-first"
				if bootstrapFirst {
					order = "bootstrap-first"
				}
				holderName := "nodecfg/new04/" + kind + "/" + order + "/H"
				bootstrapName := "nodecfg/new04/" + kind + "/" + order + "/A"
				publishName := "nodecfg/new04/" + kind + "/" + order + "/B"
				holderPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, holderName)
				bootstrapPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, bootstrapName)
				publishPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, publishName)
				bootstrapService := nodefabric.NewService(bootstrapPool, signer)
				publishService := nodefabric.NewService(publishPool, signer)
				for round := 1; round <= 25; round++ {
					fx := seedNodeConfigPG18Fixture(t, ctx, admin)
					nodeName := fmt.Sprintf("new04-%s-%s-%02d-%s", kind, order, round, fx.suffix)
					serial := 1
					if existing {
						initial, err := bootstrapService.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
							ActorID: fx.actor, NodeName: nodeName, PoolID: fx.pool, TTLMinutes: 20,
						})
						if err != nil {
							t.Fatalf("NEW-04 %s issue initial token: %v", order, err)
						}
						out, err := bootstrapService.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
							Token: initial.Token, NodeName: nodeName,
							PublicKey: nodeConfigPG18BootstrapPublicKey(nodeName + "/serial-1"), AgentVer: "pg18-new04",
						})
						if err != nil || out == nil || out.Serial != 1 {
							t.Fatalf("NEW-04 %s initial bootstrap out=%+v err=%v", order, out, err)
						}
						serial = 2
					}
					issued, err := bootstrapService.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
						ActorID: fx.actor, NodeName: nodeName, PoolID: fx.pool, TTLMinutes: 20,
					})
					if err != nil {
						t.Fatalf("NEW-04 %s issue race token: %v", order, err)
					}
					holder := beginNodeConfigPG18LockHolder(t, ctx, holderPool, fx)
					func() {
						defer holder.cleanup()
						if bootstrapFirst {
							if _, err := holder.tx.Exec(ctx, `SELECT id FROM bootstrap_tokens
							WHERE tenant_id=$1 AND consumed_at IS NULL ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE`, fx.tenant); err != nil {
								t.Fatalf("NEW-04 %s hold token: %v", order, err)
							}
						} else if _, err := holder.tx.Exec(ctx, `SELECT id FROM node_pools
						WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, fx.tenant, fx.pool); err != nil {
							t.Fatalf("NEW-04 %s hold pool: %v", order, err)
						}
						assertNodeConfigPG18ApplicationName(t, ctx, admin, holder.pid, holderName)
						bootstrapResult := make(chan nodeConfigPG18BootstrapCallResult, 1)
						publishResult := make(chan nodeConfigPG18PublishCallResult, 1)
						bootstrap := func() {
							out, err := bootstrapService.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
								Token: issued.Token, NodeName: nodeName,
								PublicKey: nodeConfigPG18BootstrapPublicKey(fmt.Sprintf("%s/serial-%d", nodeName, serial)),
								AgentVer:  "pg18-new04", Hostname: nodeName + ".example.invalid",
							})
							bootstrapResult <- nodeConfigPG18BootstrapCallResult{out: out, err: err}
						}
						publish := func() {
							out, err := publishService.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
								ActorID: fx.actor, Scope: "pool", ScopeRef: fx.pool,
								Payload: json.RawMessage(fmt.Sprintf(`{"winner":"pool","new04_round":%d}`, serial)),
							})
							publishResult <- nodeConfigPG18PublishCallResult{out: out, err: err}
						}
						if bootstrapFirst {
							go bootstrap()
							bootstrapPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
								`%FROM bootstrap_tokens%FOR UPDATE%`)
							assertNodeConfigPG18ApplicationName(t, ctx, admin, bootstrapPID, bootstrapName)
							go publish()
							publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, bootstrapPID,
								`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
							assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, publishName)
						} else {
							go publish()
							publishPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
								`%SELECT id::text FROM node_pools%status<>'disabled'%FOR SHARE%`)
							assertNodeConfigPG18ApplicationName(t, ctx, admin, publishPID, publishName)
							go bootstrap()
							bootstrapPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, publishPID,
								`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
							assertNodeConfigPG18ApplicationName(t, ctx, admin, bootstrapPID, bootstrapName)
						}
						holder.release(t)
						bootstrapped := awaitNodeConfigPG18BootstrapCall(t, bootstrapResult)
						published := awaitNodeConfigPG18PublishCall(t, publishResult)
						if bootstrapped.err != nil || bootstrapped.out == nil || bootstrapped.out.Serial != serial ||
							published.err != nil || published.out == nil {
							t.Fatalf("NEW-04 %s %s bootstrap=%+v/%v publish=%+v/%v", kind, order,
								bootstrapped.out, bootstrapped.err, published.out, published.err)
						}
						wantAffected := 0
						if existing || bootstrapFirst {
							wantAffected = 1
						}
						if published.out.AffectedNodes != wantAffected {
							t.Fatalf("NEW-04 %s %s affected=%d want=%d", kind, order, published.out.AffectedNodes, wantAffected)
						}
						assertNodeConfigPG18DesiredFetch(t, ctx, admin, publishService, signer, fx.tenant, bootstrapped.out.NodeID,
							published.out.Version, []string{fmt.Sprintf("pool@v%d", published.out.Version)}, "pool",
							map[string]int{"new04_round": serial})
						var tokens, consumed, bound, active, revoked, nodes, servers, bootAudits, publishAudits int
						if err := admin.QueryRow(ctx, `SELECT
						(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1),
						(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND used_count=1 AND consumed_at IS NOT NULL),
						(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND node_id=$2),
						(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active' AND serial=$3),
						(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked'),
						(SELECT count(*) FROM nodes WHERE tenant_id=$1 AND id=$2),
						(SELECT count(*) FROM servers WHERE tenant_id=$1 AND id=$2),
						(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap'),
						(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.config.publish')`,
							fx.tenant, bootstrapped.out.NodeID, serial).Scan(&tokens, &consumed, &bound, &active, &revoked,
							&nodes, &servers, &bootAudits, &publishAudits); err != nil {
							t.Fatalf("NEW-04 %s %s atomic state: %v", kind, order, err)
						}
						wantTokens, wantRevoked := serial, serial-1
						if tokens != wantTokens || consumed != wantTokens || bound != wantTokens || active != 1 || revoked != wantRevoked ||
							nodes != 1 || servers != 1 || bootAudits != serial || publishAudits != 1 {
							t.Fatalf("NEW-04 %s %s tokens/consumed/bound/active/revoked/nodes/servers/bootstrap_audits/publish_audits=%d/%d/%d/%d/%d/%d/%d/%d/%d",
								kind, order, tokens, consumed, bound, active, revoked, nodes, servers, bootAudits, publishAudits)
						}
					}()
				}
				assertNodeConfigPG18PoolsReleased(t, holderPool, bootstrapPool, publishPool)
				holderPool.Close()
				bootstrapPool.Close()
				publishPool.Close()
			}
			marker := "node_config_pg18_new04_new_bootstrap_publish_serialization_ok"
			if existing {
				marker = "node_config_pg18_new04_existing_bootstrap_publish_serialization_ok"
			}
			t.Log("marker=" + marker)
		})
	}

	for _, existing := range []bool{false, true} {
		kind := "new"
		if existing {
			kind = "existing"
		}
		t.Run("NEW-04 "+kind+" bootstrap cancellation rolls back late writes", func(t *testing.T) {
			fx := seedNodeConfigPG18Fixture(t, ctx, admin)
			workerName := "nodecfg/new04/" + kind + "/cancel/A"
			holderName := "nodecfg/new04/" + kind + "/cancel/H"
			workerPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, workerName)
			holderPool := openNodeConfigPG18NamedPool(t, ctx, appDSN, holderName)
			service := nodefabric.NewService(workerPool, signer)
			nodeName := "new04-cancel-" + kind + "-" + fx.suffix
			if existing {
				initial, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
					ActorID: fx.actor, NodeName: nodeName, PoolID: fx.pool, TTLMinutes: 20,
				})
				if err != nil {
					t.Fatalf("NEW-04 existing cancel issue initial token: %v", err)
				}
				if out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
					Token: initial.Token, NodeName: nodeName,
					PublicKey: nodeConfigPG18BootstrapPublicKey(nodeName + "/serial-1"), AgentVer: "pg18-new04-cancel",
				}); err != nil || out == nil || out.Serial != 1 {
					t.Fatalf("NEW-04 existing cancel initial bootstrap=%+v err=%v", out, err)
				}
			}
			issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
				ActorID: fx.actor, NodeName: nodeName, PoolID: fx.pool, TTLMinutes: 20,
			})
			if err != nil {
				t.Fatalf("NEW-04 %s cancel issue token: %v", kind, err)
			}
			before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
			holder := beginNodeConfigPG18LockHolder(t, ctx, holderPool, fx)
			defer holder.cleanup()
			sum := sha256.Sum256([]byte(fx.tenant))
			lockKey := int64(binary.BigEndian.Uint64(sum[:8]) >> 1)
			if _, err := holder.tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock($1)`, lockKey); err != nil {
				t.Fatalf("NEW-04 %s cancel hold audit lock: %v", kind, err)
			}
			assertNodeConfigPG18ApplicationName(t, ctx, admin, holder.pid, holderName)
			opCtx, cancel := context.WithCancel(ctx)
			result := make(chan error, 1)
			go func() {
				_, err := service.Bootstrap(opCtx, fx.tenant, nodefabric.BootstrapInput{
					Token: issued.Token, NodeName: nodeName,
					PublicKey: nodeConfigPG18BootstrapPublicKey(nodeName + "/cancel-at-audit"),
					AgentVer:  "pg18-new04-cancel", Hostname: "cancel.example.invalid",
				})
				result <- err
			}()
			waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid, `%SELECT pg_advisory_xact_lock($1)%`)
			assertNodeConfigPG18ApplicationName(t, ctx, admin, waiterPID, workerName)
			cancel()
			awaitNodeConfigPG18Cancellation(t, opCtx, result)
			assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
			holder.release(t)
			if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
				t.Fatalf("NEW-04 %s late cancellation changed business state", kind)
			}
			assertNodeConfigPG18PoolsReleased(t, holderPool, workerPool)
			holderPool.Close()
			workerPool.Close()
			marker := "node_config_pg18_new04_new_bootstrap_cancel_rollback_ok"
			if existing {
				marker = "node_config_pg18_new04_existing_bootstrap_cancel_rollback_ok"
			}
			t.Log("marker=" + marker)
		})
	}
}

func nodeConfigPG18BootstrapPublicKey(label string) string {
	seed := sha256.Sum256([]byte("node-config-pg18-bootstrap-key/" + label))
	pub := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

func runNodeConfigPG18BootstrapSecurityBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	service := nodefabric.NewService(appPool, signer)
	t.Run("bootstrap token is bound to its issued node name", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		targetName := "bootstrap-target-" + fx.suffix
		victimName := "bootstrap-victim-" + fx.suffix
		bootstrapNode := func(name string, keyByte byte) *nodefabric.BootstrapOutput {
			t.Helper()
			issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
				ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
			})
			if err != nil {
				t.Fatalf("issue initial token for %s: %v", name, err)
			}
			out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
				Token: issued.Token, NodeName: name,
				PublicKey: nodeConfigPG18BootstrapPublicKey(fmt.Sprintf("%s/%d", name, keyByte)),
				AgentVer:  "pg18-binding", Hostname: name + ".example.invalid",
			})
			if err != nil || out == nil || out.Serial != 1 {
				t.Fatalf("initial bootstrap for %s out=%+v err=%v", name, out, err)
			}
			return out
		}
		target := bootstrapNode(targetName, 0x51)
		victim := bootstrapNode(victimName, 0x52)
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: targetName, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue target rotation token: %v", err)
		}
		publicKey := nodeConfigPG18BootstrapPublicKey(targetName + "/rotation")
		beforeWrongName := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		wrongOut, wrongErr := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: victimName, PublicKey: publicKey,
			AgentVer: "pg18-binding", Hostname: "wrong.example.invalid",
		})
		if wrongOut != nil {
			t.Fatalf("wrong-name bootstrap returned output %+v", wrongOut)
		}
		var wrongHTTP *httpx.Error
		if !errors.As(wrongErr, &wrongHTTP) || wrongHTTP.Code != httpx.CodeUnauthorized {
			t.Fatalf("wrong-name bootstrap error=%v, want unauthorized", wrongErr)
		}
		if afterWrongName := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); afterWrongName != beforeWrongName {
			t.Fatal("wrong-name bootstrap mutated token, node, identity, server or audit state")
		}

		correctOut, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: targetName, PublicKey: publicKey,
			AgentVer: "pg18-binding", Hostname: "bound.example.invalid",
		})
		if err != nil || correctOut == nil || correctOut.NodeID != target.NodeID || correctOut.Serial != 2 {
			t.Fatalf("correct-name bootstrap out=%+v err=%v", correctOut, err)
		}
		var used, consumed, targetActive, targetRevoked, victimActive, nodes, audits int
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND used_count=1),
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND consumed_at IS NOT NULL),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active' AND serial=2),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked' AND serial=1),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$3 AND status='active' AND serial=1),
			(SELECT count(*) FROM nodes WHERE tenant_id=$1 AND id IN ($2,$3)),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap')`,
			fx.tenant, target.NodeID, victim.NodeID).Scan(&used, &consumed, &targetActive, &targetRevoked,
			&victimActive, &nodes, &audits); err != nil {
			t.Fatalf("verify name-bound bootstrap: %v", err)
		}
		if used != 3 || consumed != 3 || targetActive != 1 || targetRevoked != 1 ||
			victimActive != 1 || nodes != 2 || audits != 3 {
			t.Fatalf("name-bound bootstrap used/consumed/target_active/target_revoked/victim_active/nodes/audits=%d/%d/%d/%d/%d/%d/%d",
				used, consumed, targetActive, targetRevoked, victimActive, nodes, audits)
		}
		t.Log("marker=node_config_pg18_bootstrap_token_name_binding_ok")
	})

	t.Run("legacy unbound bootstrap token fails closed", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		rawToken := "legacy-unbound-" + fx.suffix
		if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens
			(tenant_id,token_hash,pool_id,expires_at,created_by)
			VALUES($1,$2,$3,now()+interval '20 minutes',$4)`,
			fx.tenant, platformcrypto.HashToken(rawToken), fx.pool, fx.actor); err != nil {
			t.Fatalf("seed legacy unbound bootstrap token: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: rawToken, NodeName: "legacy-target-" + fx.suffix,
			PublicKey: nodeConfigPG18BootstrapPublicKey(fx.suffix + "/legacy-unbound"),
			AgentVer:  "pg18-binding",
		})
		if out != nil {
			t.Fatalf("legacy unbound token returned output %+v", out)
		}
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeUnauthorized {
			t.Fatalf("legacy unbound token error=%v, want unauthorized", err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("legacy unbound token changed tenant business state")
		}
		t.Log("marker=node_config_pg18_bootstrap_legacy_token_refusal_ok")
	})

	t.Run("bootstrap node name canonicalization is stable", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		canonical := "canonical-" + fx.suffix
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: "  " + canonical + "  ", PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue canonical bootstrap token: %v", err)
		}
		out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: "\t" + canonical + "\n",
			PublicKey: nodeConfigPG18BootstrapPublicKey(fx.suffix + "/canonical"),
			AgentVer:  "pg18-binding",
		})
		if err != nil || out == nil {
			t.Fatalf("canonical bootstrap out=%+v err=%v", out, err)
		}
		var storedName string
		if err := admin.QueryRow(ctx, `SELECT name FROM nodes WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, out.NodeID).Scan(&storedName); err != nil {
			t.Fatalf("read canonical bootstrap node: %v", err)
		}
		if storedName != canonical {
			t.Fatalf("canonical bootstrap stored name=%q want=%q", storedName, canonical)
		}
		t.Log("marker=node_config_pg18_bootstrap_name_canonical_ok")
	})

	t.Run("disabled pool refuses bootstrap token issue", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable bootstrap issue pool: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		out, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: "disabled-issue-" + fx.suffix, PoolID: fx.pool, TTLMinutes: 20,
		})
		var he *httpx.Error
		if out != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled pool token issue out=%+v err=%v", out, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("disabled pool token issue changed tenant business state")
		}
		t.Log("marker=node_config_pg18_bootstrap_disabled_pool_issue_refusal_ok")
	})

	t.Run("disabled pool refuses token use without consuming it", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		name := "disabled-use-" + fx.suffix
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue token before disabling pool: %v", err)
		}
		var auditPool, auditNode string
		if err := admin.QueryRow(ctx, `SELECT after_digest->>'pool_id',after_digest->>'node_id'
			FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap_token.issue'
			ORDER BY occurred_at DESC,id DESC LIMIT 1`, fx.tenant).Scan(&auditPool, &auditNode); err != nil {
			t.Fatalf("read bootstrap token issue attribution: %v", err)
		}
		if auditPool != fx.pool || auditNode != "" {
			t.Fatalf("bootstrap token issue attribution pool/node=%q/%q", auditPool, auditNode)
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable bootstrap use pool: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		bootstrap := nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/disabled"),
			AgentVer:  "pg18-disabled-pool",
		}
		refused, err := service.Bootstrap(ctx, fx.tenant, bootstrap)
		var he *httpx.Error
		if refused != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled pool bootstrap out=%+v err=%v", refused, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("disabled pool bootstrap consumed token or changed business state")
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='active' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("reactivate bootstrap pool: %v", err)
		}
		accepted, err := service.Bootstrap(ctx, fx.tenant, bootstrap)
		if err != nil || accepted == nil {
			t.Fatalf("reactivated pool bootstrap out=%+v err=%v", accepted, err)
		}
		var storedPool string
		var used int
		var consumed bool
		if err := admin.QueryRow(ctx, `SELECT n.pool_id::text,t.used_count,t.consumed_at IS NOT NULL
			FROM nodes n JOIN bootstrap_tokens t ON t.tenant_id=n.tenant_id AND t.node_id=n.id
			WHERE n.tenant_id=$1 AND n.id=$2`, fx.tenant, accepted.NodeID).
			Scan(&storedPool, &used, &consumed); err != nil {
			t.Fatalf("read reactivated bootstrap state: %v", err)
		}
		if storedPool != fx.pool || used != 1 || !consumed {
			t.Fatalf("reactivated bootstrap pool/used/consumed=%s/%d/%t", storedPool, used, consumed)
		}
		t.Log("marker=node_config_pg18_bootstrap_disabled_pool_use_refusal_ok")
	})

	t.Run("existing node bootstrap is bound to its actual pool", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		name := "existing-pool-" + fx.suffix
		firstToken, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue initial existing-pool token: %v", err)
		}
		first, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: firstToken.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/serial-1"), AgentVer: "pg18-existing-pool",
		})
		if err != nil || first == nil || first.Serial != 1 {
			t.Fatalf("initial existing-pool bootstrap out=%+v err=%v", first, err)
		}

		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable existing-node actual pool: %v", err)
		}
		beforeDisabledIssue := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		refusedIssue, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, TTLMinutes: 20,
		})
		var he *httpx.Error
		if refusedIssue != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled actual pool issue out=%+v err=%v", refusedIssue, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeDisabledIssue {
			t.Fatal("disabled existing-node token issue changed business state")
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='active' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("reactivate existing-node actual pool: %v", err)
		}

		otherPool := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,status)
			VALUES($1,$2,$3,$4,'active')`, otherPool, fx.tenant, "other-"+fx.suffix, "Other "+fx.suffix); err != nil {
			t.Fatalf("seed mismatched active pool: %v", err)
		}
		beforeMismatch := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		mismatched, mismatchErr := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: otherPool, TTLMinutes: 20,
		})
		if mismatched != nil || !nodeConfigPG18IsConflict(mismatchErr) {
			t.Fatalf("mismatched existing-node pool issue out=%+v err=%v", mismatched, mismatchErr)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeMismatch {
			t.Fatal("mismatched existing-node pool issue changed business state")
		}

		rotation, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue derived-pool rotation token: %v", err)
		}
		var auditPool, auditNode string
		if err := admin.QueryRow(ctx, `SELECT after_digest->>'pool_id',after_digest->>'node_id'
			FROM audit_events WHERE tenant_id=$1 AND action='node.bootstrap_token.issue'
			ORDER BY occurred_at DESC,id DESC LIMIT 1`, fx.tenant).Scan(&auditPool, &auditNode); err != nil {
			t.Fatalf("read existing-node token attribution: %v", err)
		}
		if auditPool != fx.pool || auditNode != first.NodeID {
			t.Fatalf("existing-node token attribution pool/node=%q/%q", auditPool, auditNode)
		}
		rotationHash := platformcrypto.HashToken("node-bootstrap-v2\x00" + name + "\x00" + rotation.Token)
		if _, err := admin.Exec(ctx, `UPDATE bootstrap_tokens SET pool_id=$3
			WHERE tenant_id=$1 AND token_hash=$2`, fx.tenant, rotationHash, otherPool); err != nil {
			t.Fatalf("tamper rotation token pool for mismatch test: %v", err)
		}
		rotationInput := nodefabric.BootstrapInput{
			Token: rotation.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/serial-2"), AgentVer: "pg18-existing-pool",
		}
		beforeTokenMismatch := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		mismatchUse, mismatchUseErr := service.Bootstrap(ctx, fx.tenant, rotationInput)
		if mismatchUse != nil || !nodeConfigPG18IsConflict(mismatchUseErr) {
			t.Fatalf("mismatched token/actual pool bootstrap out=%+v err=%v", mismatchUse, mismatchUseErr)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeTokenMismatch {
			t.Fatal("mismatched token/actual pool bootstrap changed business state")
		}
		if _, err := admin.Exec(ctx, `UPDATE bootstrap_tokens SET pool_id=$3
			WHERE tenant_id=$1 AND token_hash=$2`, fx.tenant, rotationHash, fx.pool); err != nil {
			t.Fatalf("restore rotation token actual pool: %v", err)
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='disabled' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("disable pool before identity rotation: %v", err)
		}
		beforeRotation := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		refusedRotation, err := service.Bootstrap(ctx, fx.tenant, rotationInput)
		if refusedRotation != nil || !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Fatalf("disabled pool identity rotation out=%+v err=%v", refusedRotation, err)
		}
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != beforeRotation {
			t.Fatal("disabled pool identity rotation consumed token or changed identity")
		}
		if _, err := admin.Exec(ctx, `UPDATE node_pools SET status='active' WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, fx.pool); err != nil {
			t.Fatalf("reactivate pool before identity rotation: %v", err)
		}
		second, err := service.Bootstrap(ctx, fx.tenant, rotationInput)
		if err != nil || second == nil || second.NodeID != first.NodeID || second.Serial != 2 {
			t.Fatalf("reactivated existing-node rotation out=%+v err=%v", second, err)
		}
		var active, revoked, used int
		var consumed bool
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active'),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked'),
			used_count,consumed_at IS NOT NULL FROM bootstrap_tokens
			WHERE tenant_id=$1 AND token_hash=$3`, fx.tenant, first.NodeID,
			rotationHash).
			Scan(&active, &revoked, &used, &consumed); err != nil {
			t.Fatalf("read existing-node rotation state: %v", err)
		}
		if active != 1 || revoked != 1 || used != 1 || !consumed {
			t.Fatalf("existing-node rotation active/revoked/used/consumed=%d/%d/%d/%t",
				active, revoked, used, consumed)
		}
		t.Log("marker=node_config_pg18_bootstrap_existing_pool_binding_ok")
	})

	t.Run("terminal nodes refuse a valid bound bootstrap token", func(t *testing.T) {
		for _, tc := range []struct {
			status  string
			serving string
		}{
			{status: "retired", serving: "retired"},
			{status: "destroyed", serving: "retired"},
			{status: "standby", serving: "retired"},
		} {
			fx := seedNodeConfigPG18Fixture(t, ctx, admin)
			name := fmt.Sprintf("terminal-%s-%s", tc.status, fx.suffix)
			if _, err := admin.Exec(ctx, `INSERT INTO nodes
				(tenant_id,name,pool_id,status,serving_status) VALUES($1,$2,$3,$4,$5)`,
				fx.tenant, name, fx.pool, tc.status, tc.serving); err != nil {
				t.Fatalf("seed %s/%s terminal node: %v", tc.status, tc.serving, err)
			}
			refusedIssue, issueErr := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
				ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
			})
			if refusedIssue != nil || !nodeConfigPG18IsConflict(issueErr) {
				t.Fatalf("terminal %s/%s token issue out=%+v err=%v", tc.status, tc.serving, refusedIssue, issueErr)
			}
			// Issue refuses terminal targets before a usable token exists. To exercise
			// Bootstrap's independent fail-closed guard, seed a correctly v2-hashed,
			// node-ID-bound row exactly as a previously issued token would look.
			rawToken := "terminal-bound-" + tc.status + "-" + fx.suffix
			var nodeID string
			if err := admin.QueryRow(ctx, `SELECT id::text FROM nodes WHERE tenant_id=$1 AND name=$2`,
				fx.tenant, name).Scan(&nodeID); err != nil {
				t.Fatalf("read %s terminal node id: %v", tc.status, err)
			}
			if _, err := admin.Exec(ctx, `INSERT INTO bootstrap_tokens
				(tenant_id,token_hash,pool_id,node_id,expires_at,created_by)
				VALUES($1,$2,$3,$4,now()+interval '20 minutes',$5)`,
				fx.tenant, platformcrypto.HashToken("node-bootstrap-v2\x00"+name+"\x00"+rawToken),
				fx.pool, nodeID, fx.actor); err != nil {
				t.Fatalf("seed %s terminal bound token: %v", tc.status, err)
			}
			before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
			out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
				Token: rawToken, NodeName: name,
				PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/terminal"),
				AgentVer:  "pg18-binding",
			})
			if out != nil || !nodeConfigPG18IsConflict(err) {
				t.Fatalf("terminal %s/%s bootstrap out=%+v err=%v", tc.status, tc.serving, out, err)
			}
			if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
				t.Fatalf("terminal %s/%s bootstrap changed business state", tc.status, tc.serving)
			}
		}
		t.Log("marker=node_config_pg18_bootstrap_terminal_refusal_ok")
	})

	t.Run("terminal lifecycle closes identity and config lookup", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		name := "terminal-identity-" + fx.suffix
		issued, err := service.IssueBootstrapToken(ctx, fx.tenant, nodefabric.IssueTokenInput{
			ActorID: fx.actor, NodeName: name, PoolID: fx.pool, TTLMinutes: 20,
		})
		if err != nil {
			t.Fatalf("issue terminal identity token: %v", err)
		}
		out, err := service.Bootstrap(ctx, fx.tenant, nodefabric.BootstrapInput{
			Token: issued.Token, NodeName: name,
			PublicKey: nodeConfigPG18BootstrapPublicKey(name + "/active"), AgentVer: "pg18-binding",
		})
		if err != nil || out == nil {
			t.Fatalf("bootstrap terminal identity node out=%+v err=%v", out, err)
		}
		h := &handlers{d: Deps{Pool: appPool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
		setStatus := func(next string) {
			t.Helper()
			var rowVersion int64
			if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE tenant_id=$1 AND id=$2`,
				fx.tenant, out.NodeID).Scan(&rowVersion); err != nil {
				t.Fatalf("read %s transition row version: %v", next, err)
			}
			requestBody, err := json.Marshal(nodeStatusReq{
				RowVersion: rowVersion, Status: next, Reason: "pg18 lifecycle " + next,
			})
			if err != nil {
				t.Fatalf("marshal %s transition request: %v", next, err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/nodes/"+out.NodeID+"/status", bytes.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			route := chi.NewRouteContext()
			route.URLParams.Add("id", out.NodeID)
			reqCtx := context.WithValue(ctx, chi.RouteCtxKey, route)
			reqCtx = httpx.WithTenantID(reqCtx, fx.tenant)
			reqCtx = httpx.WithRequestID(reqCtx, "nodecfg-pg18-lifecycle-"+next)
			reqCtx = httpx.WithPrincipal(reqCtx, &httpx.Principal{
				Kind: "admin", Audience: "admin", UserID: fx.actor, TenantID: fx.tenant,
			})
			recorder := httptest.NewRecorder()
			h.nodeSetStatus(recorder, req.WithContext(reqCtx))
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s lifecycle status=%d body=%s", next, recorder.Code, recorder.Body.String())
			}
		}
		for _, next := range []string{"attesting", "installing", "validating", "standby", "retired"} {
			setStatus(next)
		}
		identity, identityErr := service.LookupIdentity(ctx, fx.tenant, out.NodeID)
		if identity != nil {
			t.Fatalf("terminal LookupIdentity returned %+v", identity)
		}
		var identityHTTP *httpx.Error
		if !errors.As(identityErr, &identityHTTP) || identityHTTP.Code != httpx.CodeUnauthorized {
			t.Fatalf("terminal LookupIdentity error=%v, want unauthorized", identityErr)
		}
		_, fetchErr := service.FetchConfig(ctx, fx.tenant, out.NodeID)
		assertNodeConfigPG18NotFoundNeutral(t, "terminal identity FetchConfig", fetchErr)
		var active, revoked int
		var status, serving string
		var desiredIsNull bool
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='active'),
			(SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2 AND status='revoked'),
			n.status,n.serving_status,n.desired_config_version IS NULL
			FROM nodes n WHERE n.tenant_id=$1 AND n.id=$2`, fx.tenant, out.NodeID).
			Scan(&active, &revoked, &status, &serving, &desiredIsNull); err != nil {
			t.Fatalf("read terminal active identity fixture: %v", err)
		}
		if active != 0 || revoked != 1 || status != "retired" || serving != "retired" || !desiredIsNull {
			t.Fatalf("terminal identity state active/revoked/status/serving/desired_null=%d/%d/%s/%s/%t",
				active, revoked, status, serving, desiredIsNull)
		}
		t.Log("marker=node_config_pg18_terminal_identity_refusal_ok")
	})
}

func runNodeConfigPG18Publishes(ctx context.Context, service *nodefabric.Service,
	jobs []nodeConfigPG18PublishJob) []nodeConfigPG18PublishResult {
	start := make(chan struct{})
	results := make(chan nodeConfigPG18PublishResult, len(jobs))
	var wg sync.WaitGroup
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := service.PublishConfig(ctx, job.tenant, job.input)
			results <- nodeConfigPG18PublishResult{job: job, out: out, err: err}
		}()
	}
	close(start)
	go func() { wg.Wait(); close(results) }()
	out := make([]nodeConfigPG18PublishResult, 0, len(jobs))
	for result := range results {
		out = append(out, result)
	}
	return out
}

func createNodeConfigPG18Node(t *testing.T, ctx context.Context, service *nodefabric.Service,
	fx nodeConfigPG18Fixture, poolID string, port int, label string) string {
	t.Helper()
	node, err := service.CreateAdminNode(ctx, fx.tenant, nodefabric.CreateAdminNodeInput{
		ActorID: fx.actor, Name: label + "-" + fx.suffix, ServerID: fx.server, PoolID: poolID,
		NodeType: "shadowsocks", ServerHost: "edge.example.test", ServerPort: port,
		Kernel: "auto", TrafficRate: 1, DisplayName: label + " " + fx.suffix,
		ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`), SortOrder: port,
	})
	if err != nil {
		t.Fatalf("create %s node: %v", label, err)
	}
	return node.ID
}

func assertNodeConfigPG18DesiredFetch(t *testing.T, ctx context.Context, admin *pgx.Conn,
	service *nodefabric.Service, signer *platformcrypto.Signer, tenantID, nodeID string,
	wantVersion int, wantSources []string, wantWinner string, wantInts map[string]int) {
	t.Helper()
	var desired int
	if err := admin.QueryRow(ctx, `SELECT coalesce(desired_config_version,0) FROM nodes
		WHERE tenant_id=$1 AND id=$2`, tenantID, nodeID).Scan(&desired); err != nil {
		t.Fatalf("read desired config for %s: %v", nodeID, err)
	}
	cfg, err := service.FetchConfig(ctx, tenantID, nodeID)
	if err != nil {
		t.Fatalf("fetch config for %s: %v", nodeID, err)
	}
	if desired != wantVersion || cfg.Version != wantVersion || fmt.Sprint(cfg.Sources) != fmt.Sprint(wantSources) {
		t.Fatalf("node %s desired/fetch/sources=%d/%d/%v want=%d/%d/%v",
			nodeID, desired, cfg.Version, cfg.Sources, wantVersion, wantVersion, wantSources)
	}
	if !nodefabric.VerifyConfigSignature(signer.PublicKey(), cfg.Hash, cfg.Signature, cfg.ExpiresAt) {
		t.Fatalf("node %s merged config signature did not verify", nodeID)
	}
	var payload map[string]any
	if err := json.Unmarshal(cfg.Payload, &payload); err != nil {
		t.Fatalf("decode node %s payload: %v", nodeID, err)
	}
	if payload["winner"] != wantWinner {
		t.Fatalf("node %s winner=%v want=%s", nodeID, payload["winner"], wantWinner)
	}
	for key, value := range wantInts {
		if payload[key] != float64(value) {
			t.Fatalf("node %s payload[%s]=%v want=%d", nodeID, key, payload[key], value)
		}
	}
	for _, key := range []string{"global_seq", "pool_seq", "node_seq"} {
		if _, expected := wantInts[key]; !expected {
			if _, exists := payload[key]; exists {
				t.Fatalf("node %s unexpectedly contains %s", nodeID, key)
			}
		}
	}
}

func assertNodeConfigPG18CrossTenantInvisible(t *testing.T, ctx context.Context,
	appPool *platformdb.Pool, viewer nodeConfigPG18Fixture, foreignNode, foreignConfig string) {
	t.Helper()
	var nodes, configs int
	if err := appPool.InTx(ctx, platformdb.Scope{TenantID: viewer.tenant, ActorID: viewer.actor}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM nodes WHERE id=$1),
			(SELECT count(*) FROM node_configs WHERE id=$2)`, foreignNode, foreignConfig).Scan(&nodes, &configs)
	}); err != nil {
		t.Fatalf("cross-tenant RLS probe for %s: %v", viewer.suffix, err)
	}
	if nodes != 0 || configs != 0 {
		t.Fatalf("cross-tenant rows visible to %s: nodes=%d configs=%d", viewer.suffix, nodes, configs)
	}
}

func assertNodeConfigPG18NotFoundNeutral(t *testing.T, label string, err error) {
	t.Helper()
	want := httpx.NotFoundOrForbidden()
	var gotHE, wantHE *httpx.Error
	if !errors.As(err, &gotHE) || !errors.As(want, &wantHE) ||
		gotHE.Code != wantHE.Code || gotHE.Message != wantHE.Message {
		t.Fatalf("%s error=%v, want neutral %s/%q", label, err, wantHE.Code, wantHE.Message)
	}
}

type nodeConfigPG18Connection interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func assertNodeConfigPG18ConnectionUnscoped(t *testing.T, ctx context.Context, conn nodeConfigPG18Connection) {
	t.Helper()
	var tenant, actor string
	var pools, servers, nodes, configs, applications int
	if err := conn.QueryRow(ctx, `SELECT
		coalesce(current_setting('app.tenant_id',true),''),
		coalesce(current_setting('app.actor_id',true),''),
		(SELECT count(*) FROM node_pools),(SELECT count(*) FROM servers),
		(SELECT count(*) FROM nodes),(SELECT count(*) FROM node_configs),
		(SELECT count(*) FROM node_config_applications)`).
		Scan(&tenant, &actor, &pools, &servers, &nodes, &configs, &applications); err != nil {
		t.Fatalf("inspect unscoped reused connection: %v", err)
	}
	if tenant != "" || actor != "" || pools != 0 || servers != 0 || nodes != 0 || configs != 0 || applications != 0 {
		t.Fatalf("connection retained scope tenant=%q actor=%q rows=%d/%d/%d/%d/%d",
			tenant, actor, pools, servers, nodes, configs, applications)
	}
}

func assertNodeConfigPG18SameConflict(t *testing.T, errs ...error) {
	t.Helper()
	var want string
	for i, err := range errs {
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeConflict {
			t.Fatalf("conflict[%d]=%v", i, err)
		}
		if i == 0 {
			want = he.Message
		} else if he.Message != want {
			t.Fatalf("conflict[%d] message=%q want=%q", i, he.Message, want)
		}
	}
}

func assertNodeConfigPG18LatestApplication(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID, nodeID, configID, phase, message string) {
	t.Helper()
	var gotConfig, gotPhase, gotContract, gotMessage string
	if err := admin.QueryRow(ctx, `SELECT config_id::text,phase,detail->>'contract',detail->>'message'
		FROM node_config_applications WHERE tenant_id=$1 AND node_id=$2
		ORDER BY occurred_at DESC,id DESC LIMIT 1`, tenantID, nodeID).
		Scan(&gotConfig, &gotPhase, &gotContract, &gotMessage); err != nil {
		t.Fatalf("read latest application: %v", err)
	}
	if gotConfig != configID || gotPhase != phase || gotContract != "legacy_layer_attribution" || gotMessage != message {
		t.Fatalf("latest application config=%s phase=%s contract=%s message=%s", gotConfig, gotPhase, gotContract, gotMessage)
	}
}

func seedNodeConfigPG18History(t *testing.T, ctx context.Context, admin *pgx.Conn,
	fx nodeConfigPG18Fixture, scope, scopeRef, status, payload string) (string, int) {
	t.Helper()
	var version int64
	if err := admin.QueryRow(ctx, `SELECT coalesce(max(version)::bigint,0)+1 FROM node_configs WHERE tenant_id=$1`, fx.tenant).Scan(&version); err != nil {
		t.Fatalf("allocate history fixture version: %v", err)
	}
	if version <= 0 || version > math.MaxInt32 {
		t.Fatalf("refusing history fixture version %d", version)
	}
	id := uuid.NewString()
	sum := sha256.Sum256([]byte(payload))
	if _, err := admin.Exec(ctx, `INSERT INTO node_configs
		(id,tenant_id,scope,scope_ref,version,payload,content_hash,status,published_at,created_by)
		VALUES($1,$2,$3,nullif($4,'')::uuid,$5,$6::jsonb,$7,$8,now(),$9)`,
		id, fx.tenant, scope, scopeRef, version, payload, sum[:], status, fx.actor); err != nil {
		t.Fatalf("seed %s %s history: %v", status, scope, err)
	}
	return id, int(version)
}

func seedNodeConfigPG18Draft(t *testing.T, ctx context.Context, admin *pgx.Conn,
	fx nodeConfigPG18Fixture) string {
	t.Helper()
	id := uuid.NewString()
	payload := `{"rls":"draft"}`
	sum := sha256.Sum256([]byte(payload))
	if _, err := admin.Exec(ctx, `INSERT INTO node_configs
		(id,tenant_id,scope,version,payload,content_hash,status,created_by)
		VALUES($1,$2,'global',424242,$3::jsonb,$4,'draft',$5)`, id, fx.tenant, payload, sum[:], fx.actor); err != nil {
		t.Fatalf("seed RLS draft config: %v", err)
	}
	return id
}

func nodeConfigPG18IsConflict(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == httpx.CodeConflict
}

func seedNodeConfigPG18LegacyVersion(t *testing.T, ctx context.Context, admin *pgx.Conn,
	fx nodeConfigPG18Fixture, version int64) string {
	t.Helper()
	id := uuid.NewString()
	payload := fmt.Sprintf(`{"legacy_version":%d}`, version)
	sum := sha256.Sum256([]byte(payload))
	if _, err := admin.Exec(ctx, `INSERT INTO node_configs
		(id,tenant_id,scope,scope_ref,version,payload,content_hash,status,published_at,created_by)
		VALUES($1,$2,'global',NULL,$3,$4::jsonb,$5,'rolled_back',now(),$6)`,
		id, fx.tenant, version, payload, sum[:], fx.actor); err != nil {
		t.Fatalf("seed legacy version %d: %v", version, err)
	}
	return id
}

func nodeConfigPG18BusinessSnapshot(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID string) string {
	t.Helper()
	var snapshot string
	if err := admin.QueryRow(ctx, `SELECT jsonb_build_object(
		'configs',COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM node_configs c WHERE c.tenant_id=$1),'[]'::jsonb),
		'nodes',COALESCE((SELECT jsonb_agg(to_jsonb(n) ORDER BY n.id) FROM nodes n WHERE n.tenant_id=$1),'[]'::jsonb),
		'applications',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM node_config_applications a WHERE a.tenant_id=$1),'[]'::jsonb),
		'audit',COALESCE((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.occurred_at,a.id) FROM audit_events a WHERE a.tenant_id=$1),'[]'::jsonb),
		'pools',COALESCE((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM node_pools p WHERE p.tenant_id=$1),'[]'::jsonb),
		'servers',COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM servers s WHERE s.tenant_id=$1),'[]'::jsonb),
		'bootstrap_tokens',COALESCE((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.id) FROM bootstrap_tokens b WHERE b.tenant_id=$1),'[]'::jsonb),
		'identities',COALESCE((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM node_identities i WHERE i.tenant_id=$1),'[]'::jsonb)
	)::text`, tenantID).Scan(&snapshot); err != nil {
		t.Fatalf("snapshot tenant business state: %v", err)
	}
	return snapshot
}

func assertNodeConfigPG18Target(t *testing.T, ctx context.Context, admin *pgx.Conn,
	appPool *platformdb.Pool, expectedDatabase, expectedDatabaseOID, expectedSystemID, runID string) {
	t.Helper()
	var adminDB, adminAddr string
	var adminPort, version int
	if err := admin.QueryRow(ctx, `SELECT current_database(),
		coalesce(inet_server_addr()::text,'local'), inet_server_port(),
		current_setting('server_version_num')::int`).Scan(&adminDB, &adminAddr, &adminPort, &version); err != nil {
		t.Fatalf("inspect fixture administrator target: %v", err)
	}
	if version/10000 != 18 || adminDB != expectedDatabase {
		t.Fatalf("refusing administrator target db=%q version=%d", adminDB, version)
	}
	if _, err := strconv.ParseUint(expectedDatabaseOID, 10, 32); err != nil {
		t.Fatalf("refusing malformed expected database OID %q", expectedDatabaseOID)
	}
	if _, err := strconv.ParseUint(expectedSystemID, 10, 64); err != nil {
		t.Fatalf("refusing malformed expected PostgreSQL system identifier %q", expectedSystemID)
	}
	var databaseOID, systemID string
	if err := admin.QueryRow(ctx, `SELECT d.oid::text,
		(pg_catalog.pg_control_system()).system_identifier::text
		FROM pg_catalog.pg_database d WHERE d.datname=current_database()`).Scan(&databaseOID, &systemID); err != nil {
		t.Fatalf("read database OID/system identifier: %v", err)
	}
	if databaseOID != expectedDatabaseOID || systemID != expectedSystemID {
		t.Fatalf("refusing unexpected database identity oid=%s system_id=%s", databaseOID, systemID)
	}
	var databaseMarker *string
	if err := admin.QueryRow(ctx, `SELECT pg_catalog.shobj_description(oid,'pg_database')
		FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&databaseMarker); err != nil {
		t.Fatalf("read disposable database marker: %v", err)
	}
	wantMarker := "pandora-nodecfg-disposable:" + runID
	if databaseMarker == nil || *databaseMarker != wantMarker {
		t.Fatalf("refusing database without exact disposable marker %q", wantMarker)
	}
	// 确认这是一个干净的一次性库，而不是误指向了真库。
	//
	// 判据不能是「tenants 为零」——00010_seed_rbac.sql 会建一个基线租户，
	// 跑完整迁移的库里它必然存在，那样这道检查在任何真实的迁移栈上都过不去。
	// 真正要挡的是「库里已经有别人的业务数据」，所以基线租户放行，用户、
	// 订单、节点这些必须一个都没有。
	var tenantRows, businessRows int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM tenants),
		(SELECT count(*) FROM users)+(SELECT count(*) FROM orders)+(SELECT count(*) FROM nodes)`).
		Scan(&tenantRows, &businessRows); err != nil {
		t.Fatalf("inspect disposable database baseline: %v", err)
	}
	if tenantRows > 1 || businessRows != 0 {
		t.Fatalf("refusing non-empty disposable database: tenants=%d 业务行=%d", tenantRows, businessRows)
	}
	var appDB, appAddr string
	var appPort int
	if err := appPool.QueryRow(ctx, `SELECT current_database(),
		coalesce(inet_server_addr()::text,'local'), inet_server_port()`).Scan(&appDB, &appAddr, &appPort); err != nil {
		t.Fatalf("inspect aegis_app target: %v", err)
	}
	if appDB != adminDB || appAddr != adminAddr || appPort != adminPort {
		t.Fatalf("administrator/app targets differ: admin=%s@%s:%d app=%s@%s:%d",
			adminDB, adminAddr, adminPort, appDB, appAddr, appPort)
	}
}

func seedNodeConfigPG18Fixture(t *testing.T, ctx context.Context, admin *pgx.Conn) nodeConfigPG18Fixture {
	t.Helper()
	compact := strings.ReplaceAll(uuid.NewString(), "-", "")
	fx := nodeConfigPG18Fixture{
		tenant: uuid.NewString(), actor: uuid.NewString(), pool: uuid.NewString(), server: uuid.NewString(), suffix: compact[:12],
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin node config fixture: %v", err)
	}
	defer tx.Rollback(ctx)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed node config fixture: %v\nSQL: %s", err, query)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,$3)`, fx.tenant, "nodecfg-"+fx.suffix, "Node Config PG18")
	must(`INSERT INTO users(id,tenant_id,email,display_name,status)
		VALUES($1,$2,$3,'Node Config Admin','active')`, fx.actor, fx.tenant, "nodecfg-"+fx.suffix+"@example.invalid")
	must(`INSERT INTO node_pools(id,tenant_id,code,name,region,status)
		VALUES($1,$2,$3,$4,'test','active')`, fx.pool, fx.tenant, "pool-"+fx.suffix, "Pool "+fx.suffix)
	must(`INSERT INTO servers(id,tenant_id,name,status,capacity_nodes)
		VALUES($1,$2,$3,'ready',32)`, fx.server, fx.tenant, "server-"+fx.suffix)
	must(`SET CONSTRAINTS ALL IMMEDIATE`)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit node config fixture: %v", err)
	}
	return fx
}

func assertNodeConfigPG18RuntimeRoleAndRLS(t *testing.T, ctx context.Context,
	appPool *platformdb.Pool, fx nodeConfigPG18Fixture) {
	t.Helper()
	var currentRole, sessionRole, rowSecurity string
	var superuser, bypassRLS, inherit bool
	var memberships int
	if err := appPool.QueryRow(ctx, `SELECT current_user,session_user,current_setting('row_security'),
		r.rolsuper,r.rolbypassrls,r.rolinherit,
		(SELECT count(*) FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid)
		FROM pg_catalog.pg_roles r WHERE r.rolname=current_user`).
		Scan(&currentRole, &sessionRole, &rowSecurity, &superuser, &bypassRLS, &inherit, &memberships); err != nil {
		t.Fatalf("inspect aegis_app role: %v", err)
	}
	if currentRole != "aegis_app" || sessionRole != currentRole || rowSecurity != "on" || superuser || bypassRLS || inherit || memberships != 0 {
		t.Fatalf("unsafe runtime role: current=%s session=%s row_security=%s super=%t bypass=%t inherit=%t memberships=%d",
			currentRole, sessionRole, rowSecurity, superuser, bypassRLS, inherit, memberships)
	}
	roleProbe, err := appPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire role-probe connection: %v", err)
	}
	if _, roleErr := roleProbe.Exec(ctx, `SET ROLE postgres`); roleErr == nil {
		_, resetErr := roleProbe.Exec(ctx, `RESET ROLE`)
		roleProbe.Release()
		if resetErr != nil {
			t.Fatalf("aegis_app changed role and RESET ROLE failed: %v", resetErr)
		}
		t.Fatal("aegis_app unexpectedly changed role to postgres")
	}
	roleProbe.Release()
	var enabled int
	if err := appPool.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class
		WHERE oid IN ('node_pools'::regclass,'servers'::regclass,'nodes'::regclass,
		              'node_configs'::regclass,'node_config_applications'::regclass)
		  AND relrowsecurity AND relforcerowsecurity`).Scan(&enabled); err != nil || enabled != 5 {
		t.Fatalf("node RLS table flags enabled=%d err=%v", enabled, err)
	}
	var unscoped int
	if err := appPool.QueryRow(ctx, `SELECT count(*) FROM node_pools`).Scan(&unscoped); err != nil || unscoped != 0 {
		t.Fatalf("unscoped RLS query count=%d err=%v", unscoped, err)
	}
	var scopedPools, scopedServers int
	if err := appPool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.actor}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_pools WHERE id=$1),
			(SELECT count(*) FROM servers WHERE id=$2)`, fx.pool, fx.server).Scan(&scopedPools, &scopedServers)
	}); err != nil {
		t.Fatalf("aegis_app scoped fixture visibility: %v", err)
	}
	if scopedPools != 1 || scopedServers != 1 {
		t.Fatalf("aegis_app cannot see seeded fixture: pools=%d servers=%d", scopedPools, scopedServers)
	}
	t.Log("marker=node_config_pg18_runtime_role_rls_ok")
}

func assertNodeConfigPG18Conflict(t *testing.T, err error) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeConflict {
		t.Fatalf("got error %v, want conflict", err)
	}
}

func nodeConfigPG18ApplicationCount(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID, nodeID string) int {
	t.Helper()
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_config_applications
		WHERE tenant_id=$1 AND node_id=$2`, tenantID, nodeID).Scan(&count); err != nil {
		t.Fatalf("count config applications: %v", err)
	}
	return count
}

func assertNodeConfigPG18AppVisible(t *testing.T, ctx context.Context, appPool *platformdb.Pool,
	tenantID, poolID, configID string) {
	t.Helper()
	var pools, configs int
	if err := appPool.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM node_pools WHERE id=$1),
			(SELECT count(*) FROM node_configs WHERE id=$2)`, poolID, configID).Scan(&pools, &configs)
	}); err != nil {
		t.Fatalf("aegis_app dependency visibility: %v", err)
	}
	if pools != 1 || configs != 1 {
		t.Fatalf("aegis_app cannot see delete dependencies: pools=%d configs=%d", pools, configs)
	}
}
