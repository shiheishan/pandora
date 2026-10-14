package service

// 路由与路由组 PG 测试的夹具（依赖 node_inventory_helpers_pg_test 的 invEnv）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func rtRandSuffix(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// rtServer 用 SQL 插一台服务器。
func rtServer(t *testing.T, ctx context.Context, e *invEnv, status string) uuid.UUID {
	t.Helper()
	name := e.name("rt-srv-" + rtRandSuffix(t))
	var id uuid.UUID
	q := `INSERT INTO servers (name, status, retired_at)
		VALUES (?, ?, CASE WHEN ? = 'retired' THEN now() END) RETURNING id`
	if err := e.row(ctx, q, name, status, status).Scan(&id); err != nil {
		t.Fatalf("rtServer: %v", err)
	}
	return id
}

// rtNode 用 SQL 插一个节点（vless / tcp）。
func rtNode(t *testing.T, ctx context.Context, e *invEnv, serverID uuid.UUID, serving string, port int) uuid.UUID {
	t.Helper()
	name := e.name("rt-node-" + rtRandSuffix(t))
	var id uuid.UUID
	q := `INSERT INTO nodes (name, server_id, serving_status, node_type, server_host,
		server_port, listen_l4, protocol_schema_version, protocol_config, retired_at)
		VALUES (?, ?, ?, 'vless', 'node.example.test', ?, 'tcp', 1,
		'{"flow":"xtls-rprx-vision","network":"tcp"}'::jsonb,
		CASE WHEN ? = 'retired' THEN now() END) RETURNING id`
	if err := e.row(ctx, q, name, serverID, serving, port, serving).Scan(&id); err != nil {
		t.Fatalf("rtNode: %v", err)
	}
	return id
}

// rtNodeVersion 读 nodes.row_version。
func rtNodeVersion(t *testing.T, ctx context.Context, e *invEnv, id uuid.UUID) int64 {
	t.Helper()
	var v int64
	if err := e.row(ctx, `SELECT row_version FROM nodes WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("rtNodeVersion: %v", err)
	}
	return v
}

// rtGroup 调 CreateRouteGroup，名字带 e.name。
func rtGroup(t *testing.T, ctx context.Context, e *invEnv, name string, sortOrder int) RouteGroup {
	t.Helper()
	g, err := e.svc.CreateRouteGroup(ctx, RouteGroupCreate{
		Name: e.name(name), SortOrder: sortOrder,
	})
	if err != nil {
		t.Fatalf("rtGroup: %v", err)
	}
	return g
}

// rtCleanupGroups 真提交环境用：按 tag 删本用例建的路由组。
func rtCleanupGroups(t *testing.T, e *invEnv) {
	t.Helper()
	if e.mr == nil {
		t.Fatal("rtCleanupGroups 只能在真提交环境里用")
	}
	t.Cleanup(func() {
		ctx := context.Background()
		like := "%" + e.tag + "%"
		if err := e.exec(ctx, `DELETE FROM route_groups WHERE name LIKE ?`, like); err != nil {
			t.Errorf("rtCleanupGroups: %v", err)
		}
	})
}

// ob 构造出站（settings 为 {}）。
func ob(tag, typ string) RoutingOutbound {
	return RoutingOutbound{
		Tag: tag, Type: typ, Settings: json.RawMessage(`{}`),
	}
}

// rule 构造启用中的规则。
func rule(matcher string, out string) RoutingRule {
	return RoutingRule{
		Matcher: json.RawMessage(matcher), OutboundTag: out, Enabled: true,
	}
}

// routeGroupRow 从 ListRouteGroups 里按 id 取一行（含 row_version）。
func routeGroupRow(t *testing.T, ctx context.Context, e *invEnv, id uuid.UUID) RouteGroup {
	t.Helper()
	list, err := e.svc.ListRouteGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range list {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("路由组 %s 不在列表里", id)
	return RouteGroup{}
}
