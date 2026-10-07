package admin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// runNodeConfigPG18PoolMoveBatch 证明 PATCH 换池在配置发布锁内按新池重新物化 desired 版本，
// 以及在役节点不许被清空池：
//
//  1. 草稿节点从池 A 换到池 B，desired 从 A 层的版本变成 B 层的版本（改前停在 A 层）；
//  2. 换回 A、再清空池，desired 都按当时适用的层重算；
//  3. 节点在役（serving_status=active）后清空池回 422 pool_id，库里什么都没变。
func runNodeConfigPG18PoolMoveBatch(t *testing.T, ctx context.Context, admin *pgx.Conn,
	appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	// 自己的租户：前面几批会在共用租户里造异常版本、并发发布，这里只看换池
	fx := seedNodeConfigPG18Fixture(t, ctx, admin)
	service := nodefabric.NewService(appPool, signer)
	poolA, poolB, server := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, p := range []struct{ id, code string }{{poolA, "move-a-" + fx.suffix}, {poolB, "move-b-" + fx.suffix}} {
		if _, err := admin.Exec(ctx, `INSERT INTO node_pools(id,tenant_id,code,name,region,status)
			VALUES($1,$2,$3,$3,'test','active')`, p.id, fx.tenant, p.code); err != nil {
			t.Fatalf("seed move pool: %v", err)
		}
	}
	if _, err := admin.Exec(ctx, `INSERT INTO servers(id,tenant_id,name,status,capacity_nodes)
		VALUES($1,$2,$3,'ready',4)`, server, fx.tenant, "move-server-"+fx.suffix); err != nil {
		t.Fatalf("seed move server: %v", err)
	}
	publish := func(pool, label string) int {
		t.Helper()
		out, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "pool", ScopeRef: pool,
			Payload: json.RawMessage(`{"fixture":"` + label + `"}`),
		})
		if err != nil {
			t.Fatalf("publish %s pool config: %v", label, err)
		}
		return out.Version
	}
	state := func() (rowVersion int64, desired *int, pool *string) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT row_version, desired_config_version, pool_id::text
			FROM nodes WHERE tenant_id=$1 AND name=$2`, fx.tenant, "move-"+fx.suffix).
			Scan(&rowVersion, &desired, &pool); err != nil {
			t.Fatalf("read moving node: %v", err)
		}
		return
	}
	patchPool := func(id string, pool *string) error {
		rv, _, _ := state()
		_, err := service.PatchAdminNode(ctx, fx.tenant, id, nodefabric.PatchAdminNodeInput{
			ActorID: fx.actor, RowVersion: rv, PoolID: nodefabric.OptionalNullableString{Set: true, Value: pool},
		})
		return err
	}
	wantDesired := func(label string, want *int) {
		t.Helper()
		_, got, _ := state()
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Fatalf("%s: desired=%v, want %v", label, poolMoveDeref(got), poolMoveDeref(want))
		}
	}

	versionA := publish(poolA, "move-a")
	node, err := service.CreateAdminNode(ctx, fx.tenant, nodefabric.CreateAdminNodeInput{
		ActorID: fx.actor, Name: "move-" + fx.suffix, ServerID: server, PoolID: poolA,
		NodeType: "shadowsocks", ServerHost: "move.example.test", ServerPort: 8390,
		Kernel: "auto", TrafficRate: 1, ProtocolConfig: json.RawMessage(`{"method":"aes-256-gcm"}`),
	})
	if err != nil {
		t.Fatalf("create moving node: %v", err)
	}
	wantDesired("created in pool A", &versionA)
	versionB := publish(poolB, "move-b")
	if versionB <= versionA {
		t.Fatalf("pool B version %d must be newer than pool A %d", versionB, versionA)
	}
	wantDesired("pool B publish leaves a pool A node alone", &versionA)

	if err := patchPool(node.ID, &poolB); err != nil {
		t.Fatalf("move node to pool B: %v", err)
	}
	wantDesired("moved to pool B", &versionB)

	if err := patchPool(node.ID, &poolA); err != nil {
		t.Fatalf("move node back to pool A: %v", err)
	}
	wantDesired("moved back to pool A", &versionA)

	// 草稿节点可以无池；desired 退到只剩 global 层（可能为空）
	if err := patchPool(node.ID, nil); err != nil {
		t.Fatalf("clear pool of a draft node: %v", err)
	}
	var global *int
	if err := admin.QueryRow(ctx, `SELECT max(version) FROM node_configs
		WHERE tenant_id=$1 AND status='published' AND scope='global'`, fx.tenant).Scan(&global); err != nil {
		t.Fatalf("read global version: %v", err)
	}
	wantDesired("cleared pool", global)

	// 在役节点不许清空池
	if err := patchPool(node.ID, &poolA); err != nil {
		t.Fatalf("re-pool draft node: %v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='active' WHERE tenant_id=$1 AND id=$2`, fx.tenant, node.ID); err != nil {
		t.Fatalf("mark node serving: %v", err)
	}
	beforeRV, _, _ := state()
	err = patchPool(node.ID, nil)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["pool_id"] == "" {
		t.Fatalf("clearing pool of a serving node err=%v, want 422 pool_id", err)
	}
	afterRV, desired, pool := state()
	if afterRV != beforeRV || pool == nil || *pool != poolA || desired == nil || *desired != versionA {
		t.Fatalf("refused clear changed the node: row_version %d→%d pool=%v desired=%v", beforeRV, afterRV, pool, poolMoveDeref(desired))
	}
	t.Log("marker=node_config_pg18_pool_move_materializes_ok")
}

func poolMoveDeref(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
