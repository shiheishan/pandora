// [INPUT]: 依赖 domain/nodefabric 的发布与节点生命周期，依赖 node_config_legacy_pg18_cancel_test.go 的持锁与开池工具、_lifecycle 的删池调用与结果类型、_bootstrap 的 nodeConfigPG18BootstrapPublicKey、主文件的夹具
// [OUTPUT]: 对外提供 TestNodeConfigPG18LockSchedule（确定性两两调度表的纯单测），包内提供 runNodeConfigPG18LockStressBatch
// [POS]: 节点配置发布链的锁压力批次（LOCK-01：七种操作两两并发、504 轮），由 TestNodeConfigLegacyPG18 最后调用；调度表单测不需要数据库
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const nodeConfigPG18LockSeed uint64 = 0x4c4f434b3031

type nodeConfigPG18LockOp string

const (
	nodeConfigPG18LockPublish   nodeConfigPG18LockOp = "publish"
	nodeConfigPG18LockCreate    nodeConfigPG18LockOp = "create"
	nodeConfigPG18LockClone     nodeConfigPG18LockOp = "clone"
	nodeConfigPG18LockBootstrap nodeConfigPG18LockOp = "bootstrap"
	nodeConfigPG18LockDelete    nodeConfigPG18LockOp = "delete"
	nodeConfigPG18LockDisable   nodeConfigPG18LockOp = "disable"
	nodeConfigPG18LockRetire    nodeConfigPG18LockOp = "retire"
)

var nodeConfigPG18LockOps = []nodeConfigPG18LockOp{
	nodeConfigPG18LockPublish, nodeConfigPG18LockCreate, nodeConfigPG18LockClone,
	nodeConfigPG18LockBootstrap, nodeConfigPG18LockDelete, nodeConfigPG18LockDisable,
	nodeConfigPG18LockRetire,
}

type nodeConfigPG18LockPair struct{ a, b nodeConfigPG18LockOp }

type nodeConfigPG18LockFixture struct {
	base        nodeConfigPG18Fixture
	sourceID    string
	retireID    string
	bootstrap   *nodefabric.IssueTokenOutput
	requestBase string
}

type nodeConfigPG18LockResult struct {
	op        nodeConfigPG18LockOp
	requestID string
	publish   *nodefabric.PublishOutput
	node      *nodefabric.AdminNode
	bootstrap *nodefabric.BootstrapOutput
	http      nodeConfigPG18HTTPCallResult
	err       error
}

func nodeConfigPG18LockContains(pair nodeConfigPG18LockPair, op nodeConfigPG18LockOp) bool {
	return pair.a == op || pair.b == op
}

func nodeConfigPG18LockNext(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func nodeConfigPG18LockSchedule() []nodeConfigPG18LockPair {
	pairs := make([]nodeConfigPG18LockPair, 0, 504)
	for repeat := 0; repeat < 24; repeat++ {
		for i := 0; i < len(nodeConfigPG18LockOps); i++ {
			for j := i + 1; j < len(nodeConfigPG18LockOps); j++ {
				pairs = append(pairs, nodeConfigPG18LockPair{nodeConfigPG18LockOps[i], nodeConfigPG18LockOps[j]})
			}
		}
	}
	state := nodeConfigPG18LockSeed
	for i := len(pairs) - 1; i > 0; i-- {
		j := int(nodeConfigPG18LockNext(&state) % uint64(i+1))
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}
	return pairs
}

func TestNodeConfigPG18LockSchedule(t *testing.T) {
	first, second := nodeConfigPG18LockSchedule(), nodeConfigPG18LockSchedule()
	if len(first) != 504 || len(second) != len(first) {
		t.Fatalf("LOCK-01 schedule lengths=%d/%d want=504/504", len(first), len(second))
	}
	pairs := map[string]int{}
	ops := map[nodeConfigPG18LockOp]int{}
	for i := range first {
		if first[i] != second[i] || first[i].a == first[i].b {
			t.Fatalf("LOCK-01 schedule non-deterministic or self-paired at %d: %+v/%+v", i, first[i], second[i])
		}
		pairs[string(first[i].a)+"/"+string(first[i].b)]++
		ops[first[i].a]++
		ops[first[i].b]++
	}
	if len(pairs) != 21 {
		t.Fatalf("LOCK-01 distinct pairs=%d want=21", len(pairs))
	}
	for pair, count := range pairs {
		if count != 24 {
			t.Fatalf("LOCK-01 pair %s count=%d want=24", pair, count)
		}
	}
	for _, op := range nodeConfigPG18LockOps {
		if ops[op] != 144 {
			t.Fatalf("LOCK-01 op %s count=%d want=144", op, ops[op])
		}
	}
}

func seedNodeConfigPG18LockFixture(t *testing.T, ctx context.Context, admin *pgx.Conn,
	setup, bootstrapService *nodefabric.Service, pair nodeConfigPG18LockPair, index int) nodeConfigPG18LockFixture {
	t.Helper()
	fx := seedNodeConfigPG18Fixture(t, ctx, admin)
	controlPool := uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,region,status)
		VALUES($1,$2,$3,$4,'test','active')`, controlPool, fx.tenant,
		"lock-control-"+fx.suffix, "LOCK control "+fx.suffix); err != nil {
		t.Fatalf("LOCK-01 round %d seed control pool: %v", index, err)
	}
	out := nodeConfigPG18LockFixture{
		base: fx, requestBase: fmt.Sprintf("nodecfg-lock01-%03d", index),
		sourceID: createNodeConfigPG18Node(t, ctx, setup, fx, controlPool, 21001, "lock-source"),
		retireID: createNodeConfigPG18Node(t, ctx, setup, fx, controlPool, 21002, "lock-retire"),
	}
	if nodeConfigPG18LockContains(pair, nodeConfigPG18LockBootstrap) {
		var err error
		out.bootstrap, err = bootstrapService.IssueBootstrapToken(
			httpx.WithRequestID(ctx, out.requestBase+"-issue"), fx.tenant,
			nodefabric.IssueTokenInput{ActorID: fx.actor, NodeName: "lock-bootstrap-" + fx.suffix, PoolID: fx.pool, TTLMinutes: 20})
		if err != nil || out.bootstrap == nil {
			t.Fatalf("LOCK-01 round %d issue bootstrap token: out=%+v err=%v", index, out.bootstrap, err)
		}
	}
	return out
}

func callNodeConfigPG18DisablePool(ctx context.Context, pool *platformdb.Pool,
	fx nodeConfigPG18Fixture, poolID, requestID string) nodeConfigPG18HTTPCallResult {
	h := &handlers{d: Deps{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	req := httptest.NewRequest(http.MethodPost, "/v1/node-pools/"+poolID, strings.NewReader(`{"status":"disabled"}`))
	req.Header.Set("Content-Type", "application/json")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", poolID)
	reqCtx := context.WithValue(ctx, chi.RouteCtxKey, route)
	reqCtx = httpx.WithTenantID(reqCtx, fx.tenant)
	reqCtx = httpx.WithRequestID(reqCtx, requestID)
	reqCtx = httpx.WithPrincipal(reqCtx, &httpx.Principal{Kind: "admin", Audience: "admin", UserID: fx.actor, TenantID: fx.tenant})
	rr := httptest.NewRecorder()
	h.updateNodePool(rr, req.WithContext(reqCtx))
	return nodeConfigPG18HTTPCallResult{code: rr.Code, body: append([]byte(nil), rr.Body.Bytes()...)}
}

func runNodeConfigPG18LockOp(ctx context.Context, op, other nodeConfigPG18LockOp,
	pool *platformdb.Pool, service *nodefabric.Service, fx nodeConfigPG18LockFixture, index int) nodeConfigPG18LockResult {
	requestID := fx.requestBase + "-" + string(op)
	callCtx := httpx.WithRequestID(ctx, requestID)
	result := nodeConfigPG18LockResult{op: op, requestID: requestID}
	switch op {
	case nodeConfigPG18LockPublish:
		in := nodefabric.PublishInput{ActorID: fx.base.actor, Scope: "pool", ScopeRef: fx.base.pool,
			Payload: json.RawMessage(fmt.Sprintf(`{"lock01":%d,"scope":"pool"}`, index))}
		if other == nodeConfigPG18LockRetire {
			in.Scope, in.ScopeRef = "node", fx.retireID
			in.Payload = json.RawMessage(fmt.Sprintf(`{"lock01":%d,"scope":"node"}`, index))
		}
		result.publish, result.err = service.PublishConfig(callCtx, fx.base.tenant, in)
	case nodeConfigPG18LockCreate:
		result.node, result.err = service.CreateAdminNode(callCtx, fx.base.tenant, nodefabric.CreateAdminNodeInput{
			ActorID: fx.base.actor, Name: "lock-create-" + fx.base.suffix, ServerID: fx.base.server, PoolID: fx.base.pool,
			NodeType: "shadowsocks", ServerHost: "edge.example.test", ServerPort: 22001, Kernel: "auto",
			TrafficRate: 1, DisplayName: "LOCK create", ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`), SortOrder: index})
	case nodeConfigPG18LockClone:
		result.node, result.err = service.CloneAdminNode(callCtx, fx.base.tenant, fx.sourceID, nodefabric.CloneAdminNodeInput{
			ActorID: fx.base.actor, Name: "lock-clone-" + fx.base.suffix, RowVersion: 1,
			TargetServerID: fx.base.server, PoolID: fx.base.pool})
	case nodeConfigPG18LockBootstrap:
		result.bootstrap, result.err = service.Bootstrap(callCtx, fx.base.tenant, nodefabric.BootstrapInput{
			Token: fx.bootstrap.Token, NodeName: "lock-bootstrap-" + fx.base.suffix,
			PublicKey: nodeConfigPG18BootstrapPublicKey(fx.base.suffix + "/lock01"), AgentVer: "lock01",
			Hostname: "lock01.example.test", CPUCores: 2, MemoryMB: 2048, DiskGB: 20, PublicIP: "192.0.2.10"})
	case nodeConfigPG18LockDelete:
		result.http = callNodeConfigPG18DeletePool(callCtx, pool, fx.base, fx.base.pool, requestID)
	case nodeConfigPG18LockDisable:
		result.http = callNodeConfigPG18DisablePool(callCtx, pool, fx.base, fx.base.pool, requestID)
	case nodeConfigPG18LockRetire:
		result.err = service.BatchAdminNodeLifecycle(callCtx, fx.base.tenant, nodefabric.BatchNodeLifecycleInput{
			ActorID: fx.base.actor, Items: []nodefabric.BatchNodeLifecycleItem{{ID: fx.retireID, RowVersion: 1}},
			ServingStatus: "retired", Reason: "LOCK-01"})
	}
	return result
}

func waitNodeConfigPG18LockRound(t *testing.T, ctx context.Context, admin *pgx.Conn,
	holderPID int, pair nodeConfigPG18LockPair) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	wantAdvisory := 2
	if nodeConfigPG18LockContains(pair, nodeConfigPG18LockDisable) {
		wantAdvisory = 1
	}
	for {
		var names, pids, advisory int
		err := admin.QueryRow(ctx, `SELECT count(DISTINCT a.application_name),count(DISTINCT a.pid),
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM pg_catalog.pg_locks l
			 WHERE l.pid=a.pid AND NOT l.granted AND l.locktype='advisory'))
			FROM pg_catalog.pg_stat_activity a WHERE a.datname=current_database() AND a.usename='aegis_app'
			 AND a.application_name IN ($2,$3) AND a.state='active' AND a.wait_event_type='Lock'
			 AND $1=ANY(pg_catalog.pg_blocking_pids(a.pid))`, holderPID,
			"nodecfg/lock01/"+string(pair.a), "nodecfg/lock01/"+string(pair.b)).Scan(&names, &pids, &advisory)
		if err != nil {
			t.Fatalf("observe LOCK-01 waiters: %v", err)
		}
		if names == 2 && pids == 2 && advisory == wantAdvisory {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("LOCK-01 pair %s/%s wait evidence names/pids/advisory=%d/%d/%d want=2/2/%d",
				pair.a, pair.b, names, pids, advisory, wantAdvisory)
		case <-ctx.Done():
			t.Fatalf("LOCK-01 wait observation ended: %v", ctx.Err())
		}
	}
}

func nodeConfigPG18LockHTTPCode(result nodeConfigPG18HTTPCallResult) httpx.Code {
	if result.code < 400 {
		return ""
	}
	var body struct {
		Error struct {
			Code httpx.Code `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(result.body, &body) != nil {
		return httpx.CodeInternal
	}
	return body.Error.Code
}

func (result nodeConfigPG18LockResult) succeeded() bool {
	switch result.op {
	case nodeConfigPG18LockPublish:
		return result.err == nil && result.publish != nil
	case nodeConfigPG18LockCreate, nodeConfigPG18LockClone:
		return result.err == nil && result.node != nil
	case nodeConfigPG18LockBootstrap:
		return result.err == nil && result.bootstrap != nil
	case nodeConfigPG18LockDelete, nodeConfigPG18LockDisable:
		return result.http.code == http.StatusOK
	case nodeConfigPG18LockRetire:
		return result.err == nil
	}
	return false
}

func assertNodeConfigPG18LockOutcome(t *testing.T, result nodeConfigPG18LockResult) {
	t.Helper()
	if result.err != nil {
		var pe *pgconn.PgError
		if errors.As(result.err, &pe) && (pe.Code == "40P01" || pe.Code == "57014") {
			t.Fatalf("LOCK-01 %s PostgreSQL failure %s: %v", result.op, pe.Code, result.err)
		}
		if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("LOCK-01 %s canceled or timed out: %v", result.op, result.err)
		}
	}
	if result.succeeded() {
		return
	}
	var he *httpx.Error
	switch result.op {
	case nodeConfigPG18LockPublish:
		if !errors.As(result.err, &he) || he.Code != httpx.CodeNotFound {
			t.Fatalf("LOCK-01 publish out=%+v err=%v", result.publish, result.err)
		}
	case nodeConfigPG18LockCreate, nodeConfigPG18LockClone, nodeConfigPG18LockBootstrap:
		if !errors.As(result.err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["pool_id"] == "" {
			t.Fatalf("LOCK-01 %s err=%v, want pool validation failure", result.op, result.err)
		}
	case nodeConfigPG18LockDelete:
		if code := nodeConfigPG18LockHTTPCode(result.http); code != httpx.CodeConflict {
			t.Fatalf("LOCK-01 delete status=%d code=%s body=%s", result.http.code, code, result.http.body)
		}
	case nodeConfigPG18LockDisable:
		if code := nodeConfigPG18LockHTTPCode(result.http); code != httpx.CodeNotFound {
			t.Fatalf("LOCK-01 disable status=%d code=%s body=%s", result.http.code, code, result.http.body)
		}
	case nodeConfigPG18LockRetire:
		t.Fatalf("LOCK-01 retire failed: %v", result.err)
	}
}

func assertNodeConfigPG18LockState(t *testing.T, ctx context.Context, admin *pgx.Conn,
	fetch *nodefabric.Service, signer *platformcrypto.Signer, fx nodeConfigPG18LockFixture,
	results map[nodeConfigPG18LockOp]nodeConfigPG18LockResult) {
	t.Helper()
	actions := map[nodeConfigPG18LockOp]string{
		nodeConfigPG18LockPublish: "node.config.publish", nodeConfigPG18LockCreate: "node.create",
		nodeConfigPG18LockClone: "node.copy", nodeConfigPG18LockBootstrap: "node.bootstrap",
		nodeConfigPG18LockDelete: "node_pool.deleted", nodeConfigPG18LockDisable: "node_pool.updated",
		nodeConfigPG18LockRetire: "node.status_batch",
	}
	for op, result := range results {
		want := 0
		if result.succeeded() {
			want = 1
		}
		var got int
		if err := admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action=$3)
			+1000*count(*) FILTER (WHERE action<>$3) FROM audit_events
			WHERE tenant_id=$1 AND request_id=$2`, fx.base.tenant, result.requestID, actions[op]).Scan(&got); err != nil || got != want {
			t.Fatalf("LOCK-01 %s audit count=%d want=%d err=%v", op, got, want, err)
		}
	}
	wantConfigs := 0
	if result, ok := results[nodeConfigPG18LockPublish]; ok && result.succeeded() {
		wantConfigs = 1
	}
	var configs, orphanPools, orphanNodes, desiredMismatch, retiredDesired, applications int
	if err := admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM node_configs WHERE tenant_id=$1),
		(SELECT count(*) FROM node_configs c LEFT JOIN node_pools p
		 ON p.tenant_id=c.tenant_id AND p.id=c.scope_ref WHERE c.tenant_id=$1
		 AND c.scope='pool' AND c.scope_ref IS NOT NULL AND p.id IS NULL),
		(SELECT count(*) FROM node_configs c LEFT JOIN nodes n
		 ON n.tenant_id=c.tenant_id AND n.id=c.scope_ref WHERE c.tenant_id=$1
		 AND c.scope='node' AND c.scope_ref IS NOT NULL AND n.id IS NULL),
		(SELECT count(*) FROM nodes n WHERE n.tenant_id=$1 AND n.status NOT IN ('destroyed','retired')
		 AND n.serving_status<>'retired' AND n.desired_config_version IS DISTINCT FROM
		 (SELECT max(c.version) FROM node_configs c WHERE c.tenant_id=n.tenant_id AND c.status='published'
		  AND (c.scope='global' OR (c.scope='pool' AND c.scope_ref=n.pool_id) OR (c.scope='node' AND c.scope_ref=n.id)))),
		(SELECT count(*) FROM nodes WHERE tenant_id=$1 AND (status='retired' OR serving_status='retired')
		 AND desired_config_version IS NOT NULL),
		(SELECT count(*) FROM node_config_applications WHERE tenant_id=$1)`, fx.base.tenant).
		Scan(&configs, &orphanPools, &orphanNodes, &desiredMismatch, &retiredDesired, &applications); err != nil {
		t.Fatalf("inspect LOCK-01 state: %v", err)
	}
	if configs != wantConfigs || orphanPools != 0 || orphanNodes != 0 || desiredMismatch != 0 || retiredDesired != 0 || applications != 0 {
		t.Fatalf("LOCK-01 state configs/orphan_pool/orphan_node/desired/retired/apps=%d/%d/%d/%d/%d/%d want=%d/0/0/0/0/0",
			configs, orphanPools, orphanNodes, desiredMismatch, retiredDesired, applications, wantConfigs)
	}
	names := map[nodeConfigPG18LockOp]string{
		nodeConfigPG18LockCreate:    "lock-create-" + fx.base.suffix,
		nodeConfigPG18LockClone:     "lock-clone-" + fx.base.suffix,
		nodeConfigPG18LockBootstrap: "lock-bootstrap-" + fx.base.suffix,
	}
	for op, name := range names {
		if result, ok := results[op]; ok {
			want := 0
			if result.succeeded() {
				want = 1
			}
			var got int
			if err := admin.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND name=$2`,
				fx.base.tenant, name).Scan(&got); err != nil || got != want {
				t.Fatalf("LOCK-01 %s node count=%d want=%d err=%v", op, got, want, err)
			}
		}
	}
	if result, ok := results[nodeConfigPG18LockBootstrap]; ok {
		wantUsed := 0
		if result.succeeded() {
			wantUsed = 1
		}
		var tokens, used, consumed, identities, servers int
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1),
			(SELECT coalesce(sum(used_count),0)::int FROM bootstrap_tokens WHERE tenant_id=$1),
			(SELECT count(*) FROM bootstrap_tokens WHERE tenant_id=$1 AND consumed_at IS NOT NULL),
			(SELECT count(*) FILTER (WHERE status='active' AND serial=1)
			 +1000*count(*) FILTER (WHERE status<>'active' OR serial<>1) FROM node_identities WHERE tenant_id=$1),
			(SELECT count(*) FROM servers WHERE tenant_id=$1 AND name=$2)`,
			fx.base.tenant, "lock-bootstrap-"+fx.base.suffix).Scan(&tokens, &used, &consumed, &identities, &servers); err != nil ||
			tokens != 1 || used != wantUsed || consumed != wantUsed || identities != wantUsed || servers != wantUsed {
			t.Fatalf("LOCK-01 bootstrap tokens/used/consumed/identities/servers=%d/%d/%d/%d/%d want=1/%d/%d/%d/%d err=%v",
				tokens, used, consumed, identities, servers, wantUsed, wantUsed, wantUsed, wantUsed, err)
		}
	}
	deleteResult, deletePresent := results[nodeConfigPG18LockDelete]
	deleteSucceeded := deletePresent && deleteResult.succeeded()
	for _, op := range []nodeConfigPG18LockOp{nodeConfigPG18LockPublish, nodeConfigPG18LockCreate, nodeConfigPG18LockClone, nodeConfigPG18LockBootstrap} {
		if deleteSucceeded {
			if result, ok := results[op]; ok && result.succeeded() {
				t.Fatal("LOCK-01 delete bypassed a completed pool dependency")
			}
		}
	}
	var poolCount int
	var poolStatus string
	if err := admin.QueryRow(ctx, `SELECT count(*),coalesce(max(status),'') FROM node_pools
		WHERE tenant_id=$1 AND id=$2`, fx.base.tenant, fx.base.pool).Scan(&poolCount, &poolStatus); err != nil {
		t.Fatalf("LOCK-01 inspect target pool: %v", err)
	}
	if (deleteSucceeded && poolCount != 0) || (!deleteSucceeded && poolCount != 1) {
		t.Fatalf("LOCK-01 target pool count=%d delete_succeeded=%t", poolCount, deleteSucceeded)
	}
	if result, ok := results[nodeConfigPG18LockDisable]; ok && result.succeeded() && poolCount == 1 && poolStatus != "disabled" {
		t.Fatalf("LOCK-01 disabled pool status=%q", poolStatus)
	}
	if result, ok := results[nodeConfigPG18LockRetire]; ok && result.succeeded() {
		var serving string
		var desiredNull bool
		if err := admin.QueryRow(ctx, `SELECT serving_status,desired_config_version IS NULL FROM nodes
			WHERE tenant_id=$1 AND id=$2`, fx.base.tenant, fx.retireID).Scan(&serving, &desiredNull); err != nil ||
			serving != "retired" || !desiredNull {
			t.Fatalf("LOCK-01 retire state serving=%q desired_null=%t err=%v", serving, desiredNull, err)
		}
	}
	rows, err := admin.Query(ctx, `SELECT n.id::text,x.version FROM nodes n CROSS JOIN LATERAL
		(SELECT max(c.version) AS version FROM node_configs c WHERE c.tenant_id=n.tenant_id AND c.status='published'
		 AND (c.scope='global' OR (c.scope='pool' AND c.scope_ref=n.pool_id) OR (c.scope='node' AND c.scope_ref=n.id))) x
		WHERE n.tenant_id=$1 AND n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'
		AND x.version IS NOT NULL`, fx.base.tenant)
	if err != nil {
		t.Fatalf("LOCK-01 list Fetch projections: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var version int
		if err := rows.Scan(&id, &version); err != nil {
			t.Fatalf("LOCK-01 scan Fetch projection: %v", err)
		}
		cfg, err := fetch.FetchConfig(ctx, fx.base.tenant, id)
		if err != nil || cfg.Version != version || !nodefabric.VerifyConfigSignature(signer.PublicKey(), cfg.Hash, cfg.Signature, cfg.ExpiresAt) {
			t.Fatalf("LOCK-01 Fetch node=%s version=%d cfg=%+v err=%v", id, version, cfg, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("LOCK-01 projection rows: %v", err)
	}
}

func assertNodeConfigPG18LockResidue(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var named, active, advisory int
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname=current_database()
			 AND application_name LIKE 'nodecfg/lock01/%'),
			(SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname=current_database()
			 AND usename='aegis_app' AND (xact_start IS NOT NULL OR wait_event_type='Lock')),
			(SELECT count(*) FROM pg_catalog.pg_locks l JOIN pg_catalog.pg_stat_activity a ON a.pid=l.pid
			 WHERE a.datname=current_database() AND a.usename='aegis_app' AND l.locktype='advisory')`).
			Scan(&named, &active, &advisory); err != nil {
			t.Fatalf("inspect LOCK-01 residue: %v", err)
		}
		if named == 0 && active == 0 && advisory == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("LOCK-01 residue named/active/advisory=%d/%d/%d", named, active, advisory)
		case <-ctx.Done():
			t.Fatalf("LOCK-01 residue context: %v", ctx.Err())
		}
	}
}

func runNodeConfigPG18LockStressBatch(t *testing.T, ctx context.Context, admin *pgx.Conn,
	appDSN string, signer *platformcrypto.Signer) {
	t.Helper()
	lockCtx, cancel := context.WithTimeout(ctx, 1500*time.Second)
	defer cancel()
	pools := make(map[nodeConfigPG18LockOp]*platformdb.Pool, len(nodeConfigPG18LockOps))
	services := make(map[nodeConfigPG18LockOp]*nodefabric.Service, len(nodeConfigPG18LockOps))
	allPools := make([]*platformdb.Pool, 0, len(nodeConfigPG18LockOps)+1)
	for _, op := range nodeConfigPG18LockOps {
		pool := openNodeConfigPG18NamedPool(t, lockCtx, appDSN, "nodecfg/lock01/"+string(op))
		pools[op] = pool
		services[op] = nodefabric.NewService(pool, signer)
		allPools = append(allPools, pool)
	}
	gatePool := openNodeConfigPG18NamedPool(t, lockCtx, appDSN, "nodecfg/lock01/gate")
	allPools = append(allPools, gatePool)
	closed := false
	defer func() {
		if !closed {
			for _, pool := range allPools {
				pool.Close()
			}
		}
	}()
	schedule := nodeConfigPG18LockSchedule()
	if len(schedule) != 504 {
		t.Fatalf("LOCK-01 schedule rounds=%d want=504", len(schedule))
	}
	for index, pair := range schedule {
		func() {
			roundCtx, roundCancel := context.WithTimeout(lockCtx, 4*time.Second)
			defer roundCancel()
			fx := seedNodeConfigPG18LockFixture(t, roundCtx, admin, services[nodeConfigPG18LockCreate],
				services[nodeConfigPG18LockBootstrap], pair, index)
			holder := beginNodeConfigPG18LockHolder(t, roundCtx, gatePool, fx.base)
			defer holder.cleanup()
			if _, err := holder.tx.Exec(roundCtx, `SELECT pg_catalog.pg_advisory_xact_lock(
				pg_catalog.hashtextextended($1,0))`, "node-config-release/"+fx.base.tenant); err != nil {
				t.Fatalf("LOCK-01 round %d hold release lock: %v", index, err)
			}
			if nodeConfigPG18LockContains(pair, nodeConfigPG18LockDisable) {
				var poolID string
				if err := holder.tx.QueryRow(roundCtx, `SELECT id::text FROM node_pools
					WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, fx.base.tenant, fx.base.pool).Scan(&poolID); err != nil {
					t.Fatalf("LOCK-01 round %d hold pool lock: %v", index, err)
				}
			}
			start := make(chan struct{})
			resultCh := make(chan nodeConfigPG18LockResult, 2)
			for _, job := range []struct{ op, other nodeConfigPG18LockOp }{{pair.a, pair.b}, {pair.b, pair.a}} {
				job := job
				go func() {
					<-start
					resultCh <- runNodeConfigPG18LockOp(roundCtx, job.op, job.other, pools[job.op], services[job.op], fx, index)
				}()
			}
			close(start)
			waitNodeConfigPG18LockRound(t, roundCtx, admin, holder.pid, pair)
			holder.release(t)
			results := make(map[nodeConfigPG18LockOp]nodeConfigPG18LockResult, 2)
			for len(results) < 2 {
				select {
				case result := <-resultCh:
					assertNodeConfigPG18LockOutcome(t, result)
					results[result.op] = result
				case <-roundCtx.Done():
					t.Fatalf("LOCK-01 round %d pair=%s/%s did not finish: %v", index, pair.a, pair.b, roundCtx.Err())
				}
			}
			assertNodeConfigPG18LockState(t, roundCtx, admin, services[nodeConfigPG18LockPublish], signer, fx, results)
		}()
	}
	assertNodeConfigPG18PoolsReleased(t, allPools...)
	for _, pool := range allPools {
		pool.Close()
	}
	closed = true
	assertNodeConfigPG18LockResidue(t, ctx, admin)
	t.Logf("LOCK-01 seed=%#x rounds=%d operations=%d", nodeConfigPG18LockSeed, len(schedule), len(schedule)*2)
	t.Log("marker=node_config_pg18_lock01_stress_504_ok")
}
