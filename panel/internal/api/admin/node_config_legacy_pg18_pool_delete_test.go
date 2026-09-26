// [INPUT]: 依赖 domain/nodefabric 的 PublishConfig / CreateAdminNode / CloneAdminNode，依赖 node_config_legacy_pg18_lifecycle_test.go 的调用结果与删池调用、node_config_legacy_pg18_cancel_test.go 的持锁工具、node_config_legacy_pg18_test.go 的夹具
// [OUTPUT]: 包内提供 runNodeConfigPG18PoolDeleteRaceBatch
// [POS]: TestNodeConfigLegacyPG18 的删池竞态批次（DEL-01 空池删除与发布、DEL-03 删除与发布先后、DEL-04 删除与建节点 / 克隆的串行化）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
