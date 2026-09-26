// [INPUT]: 依赖 domain/nodefabric 的 NewService / PublishConfig / FetchConfig / ReportConfigApplied，依赖 platform/db 与 pgx 的双连接（夹具管理员 + aegis_app），依赖 platform/crypto 的确定性签名器
// [OUTPUT]: 对外提供 TestNodeConfigLegacyPG18（run-pg18-gates.sh 的 node_config 域），包内提供一次性库夹具 nodeConfigPG18Fixture / seedNodeConfigPG18Fixture、库身份与运行角色护栏，以及各批次共用的发布、种数据与断言工具（runNodeConfigPG18Publishes、seedNodeConfigPG18*、assertNodeConfigPG18*）
// [POS]: 节点配置发布链的 PG18 集成门禁主入口：先验库身份与 RLS，跑几个基础子测试，再按序调用 _publication / _cancel / _lifecycle / _bootstrap / _pool_delete / _materialize 与 _lock 各文件的批次
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
