// [INPUT]: 依赖 domain/nodefabric 的 CreateAdminNode / CloneAdminNode / IssueBootstrapToken / Bootstrap / PublishConfig，依赖 node_config_legacy_pg18_bootstrap_test.go 的 nodeConfigPG18BootstrapPublicKey、_lifecycle 的调用结果、_cancel 的持锁工具、主文件的夹具
// [OUTPUT]: 包内提供 runNodeConfigPG18NewMaterializationRaceBatch 与它收尾调用的 runNodeConfigPG18BootstrapPublicationRaces
// [POS]: TestNodeConfigLegacyPG18 的新节点物化竞态批次：建节点 / 克隆与发布的串行化（NEW-02、NEW-03）、引导注册与发布交错
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
)

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
