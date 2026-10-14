package service

import (
	"slices"
	"testing"
	"time"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

func TestDeliverableNodesXBPG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)

	// 1. 基础环境设置：服务器、池、套餐、用户、订阅
	g := e.dGroup(t, ctx)
	p := e.dPool(t, ctx, PoolActive, g)
	srv := e.dServer(t, ctx, "ready", ptrDur(time.Minute))
	nodeID := e.dNode(t, ctx, &p, srv, "active", 25000, "香港主节点")
	v := e.dVersion(t, ctx, nil, nil, p)
	user := e.dUser(t, ctx, "active", &g)
	sub := e.dSub(t, ctx, user, v, subOpts{})

	// 用 SQL 给该节点插两条中转（一条 enabled = false），设 tags = '["香港","IPLC"]'
	e.mustExec(t, ctx, `UPDATE nodes SET tags = '["香港","IPLC"]'::jsonb WHERE id = ?`, nodeID)
	e.mustExec(t, ctx, `INSERT INTO node_relays (parent_node_id, name, server_host, server_port, enabled, sort_order)
		VALUES (?, '香港中转-启用', 'relay-on.example.test', 25001, true, 1)`, nodeID)
	e.mustExec(t, ctx, `INSERT INTO node_relays (parent_node_id, name, server_host, server_port, enabled, sort_order)
		VALUES (?, '香港中转-停用', 'relay-off.example.test', 25002, false, 2)`, nodeID)

	q := subsvc.NodeQuery{
		SubscriptionID: sub.ID,
		UserID:         user,
		PlanVersionID:  v,
		UserGroupID:    &g,
	}

	// 验证：DeliverableNodes 结果 = 父节点 + 1 条启用中转，中转 Name/Host/Port 对，Tags 两者都是 [香港 IPLC]
	nodes, err := e.svc.loadDeliverableNodes(ctx, q.PlanVersionID, q.UserGroupID)
	if err != nil {
		t.Fatalf("loadDeliverableNodes 失败: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("应有 2 条线路（1个父节点 + 1条启用中转），实际 %d: %+v", len(nodes), nodes)
	}

	if nodes[0].Name != "香港主节点" || nodes[0].Host != "node.example.test" || nodes[0].Port != 25000 {
		t.Fatalf("父节点字段不符: %+v", nodes[0])
	}
	if !slices.Equal(nodes[0].Tags, []string{"香港", "IPLC"}) {
		t.Fatalf("父节点 Tags 不符: %v", nodes[0].Tags)
	}

	if nodes[1].Name != "香港中转-启用" || nodes[1].Host != "relay-on.example.test" || nodes[1].Port != 25001 {
		t.Fatalf("中转节点字段不符: %+v", nodes[1])
	}
	if !slices.Equal(nodes[1].Tags, []string{"香港", "IPLC"}) {
		t.Fatalf("中转节点 Tags 未继承: %v", nodes[1].Tags)
	}

	// 2. 用 SQL 设流量超限：父节点与中转都不在结果里
	e.mustExec(t, ctx, `UPDATE nodes SET traffic_limit_bytes = 100, traffic_used_bytes = 100, traffic_exceeded_at = now() WHERE id = ?`, nodeID)
	nodesAfterExceeded, err := e.svc.loadDeliverableNodes(ctx, q.PlanVersionID, q.UserGroupID)
	if err != nil {
		t.Fatalf("loadDeliverableNodes 失败: %v", err)
	}
	if len(nodesAfterExceeded) != 0 {
		t.Fatalf("超限后节点应被排除，实际有 %d 个节点: %+v", len(nodesAfterExceeded), nodesAfterExceeded)
	}

	// 恢复节点流量上限以便继续测试 ECH
	e.mustExec(t, ctx, `UPDATE nodes SET traffic_limit_bytes = NULL, traffic_used_bytes = 0, traffic_exceeded_at = NULL WHERE id = ?`, nodeID)

	// 3. 用 SQL 插一把当前 ECH 钥
	e.mustExec(t, ctx, `INSERT INTO node_ech_keys (node_id, public_name, config_list, private_key_sealed)
		VALUES (?, 'ech.example.com', '\x0004fe0d0000'::bytea, '\x01'::bytea)`, nodeID)

	// 节点协议若不支持 ECH（夹具的 protocol_config 是 {"flow":"xtls-rprx-vision"}，默认为 vless 无 tls 或非真实 TLS 握手时 protocol.ECHCapable 为 false）
	// 断言结果 ECHConfigList 为 nil
	nodesWithECH, err := e.svc.loadDeliverableNodes(ctx, q.PlanVersionID, q.UserGroupID)
	if err != nil {
		t.Fatalf("loadDeliverableNodes 失败: %v", err)
	}
	if len(nodesWithECH) == 0 {
		t.Fatalf("应能查出节点")
	}
	if nodesWithECH[0].ECHConfigList != nil {
		t.Fatalf("协议不支持 ECH 时，ECHConfigList 应为 nil，实际: %v", nodesWithECH[0].ECHConfigList)
	}
}
