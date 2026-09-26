// [INPUT]: 依赖 domain/nodefabric 的 PublishConfig / BatchAdminNodeLifecycle，依赖 pools.go 的 deleteNodePool 处理器，依赖 node_config_legacy_pg18_cancel_test.go 的持锁工具、node_config_legacy_pg18_test.go 的夹具与共用断言
// [OUTPUT]: 包内提供并发调用结果类型（nodeConfigPG18Publish / HTTP / AdminNode / BootstrapCallResult）、callNodeConfigPG18DeletePool 与 await / assert 工具，以及 runNodeConfigPG18LifecycleRaceBatch、runNodeConfigPG18PoolLifecycleRaceBatch、runNodeConfigPG18GlobalPoolRetirementRaceBatch
// [POS]: TestNodeConfigLegacyPG18 的生命周期竞态批次（发布与节点退役、池停用、全局与池配置遇退役），调用结果类型与删池调用被 _pool_delete / _materialize / _lock 复用
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
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
