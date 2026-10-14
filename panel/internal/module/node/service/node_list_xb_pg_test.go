package service

// 后台节点列表的 XB 扩展字段 PG 测试：
// 标签筛选与去空格、流量上限与重置日、中转条数、ECH 启用状态。

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestQueryNodesXBFields(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	srv := e.server(t, ctx, "list-xb")

	nodeA := e.ssNode(t, ctx, srv.ID, "xb-node-a", 23001)
	nodeB := e.ssNode(t, ctx, srv.ID, "xb-node-b", 23002)

	// 1. 两个节点，用 SQL 给一个设 tags = '["香港","IPLC"]'
	if err := e.exec(ctx, `UPDATE nodes SET tags = '["香港","IPLC"]'::jsonb WHERE id = ?`, nodeA.ID); err != nil {
		t.Fatalf("设 nodeA tags: %v", err)
	}

	// QueryNodes(NodeQuery{Tag: "香港", Limit: 50}) 只回它，Tags 两个
	rows, total, err := e.svc.QueryNodes(ctx, NodeQuery{Search: e.tag, Tag: "香港", Limit: 50})
	if err != nil {
		t.Fatalf("QueryNodes Tag 香港: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("按香港筛选总数 = %d, 行数 = %d, want 1", total, len(rows))
	}
	if rows[0].ID != nodeA.ID {
		t.Fatalf("命中节点 ID = %v, want %v", rows[0].ID, nodeA.ID)
	}
	if len(rows[0].Tags) != 2 || rows[0].Tags[0] != "香港" || rows[0].Tags[1] != "IPLC" {
		t.Fatalf("Tags 不符合: %+v", rows[0].Tags)
	}

	// Tag: " 香港 " 同样结果
	rowsTrim, totalTrim, err := e.svc.QueryNodes(ctx, NodeQuery{Search: e.tag, Tag: " 香港 ", Limit: 50})
	if err != nil {
		t.Fatalf("QueryNodes Tag ' 香港 ': %v", err)
	}
	if totalTrim != 1 || len(rowsTrim) != 1 || rowsTrim[0].ID != nodeA.ID {
		t.Fatalf("带空格筛选不符合: total=%d rows=%d", totalTrim, len(rowsTrim))
	}

	// Tag 17 个字 → 422 且 Fields 有 tag
	longTag := strings.Repeat("字", 17)
	_, _, err = e.svc.QueryNodes(ctx, NodeQuery{Search: e.tag, Tag: longTag, Limit: 50})
	_ = invWantCode(t, err, errs.ValidationFailed)
	if eErr := errs.As(err); eErr != nil {
		if _, ok := eErr.Fields["tag"]; !ok {
			t.Errorf("Fields 应包含 tag, got: %v", eErr.Fields)
		}
	}

	// 2. SQL 设 traffic_limit_bytes = 1000, traffic_used_bytes = 1000, traffic_exceeded_at = now(), traffic_reset_day = 5
	if err := e.exec(ctx, `UPDATE nodes SET
		traffic_limit_bytes = 1000,
		traffic_used_bytes = 1000,
		traffic_exceeded_at = now(),
		traffic_reset_day = 5
		WHERE id = ?`, nodeA.ID); err != nil {
		t.Fatalf("设 nodeA 流量字段: %v", err)
	}
	rowA := invFindRow(t, e, ctx, nodeA.ID)
	if rowA.Traffic.LimitBytes == nil || *rowA.Traffic.LimitBytes != 1000 {
		t.Errorf("Traffic.LimitBytes = %v, want 1000", rowA.Traffic.LimitBytes)
	}
	if rowA.Traffic.UsedBytes != 1000 {
		t.Errorf("Traffic.UsedBytes = %d, want 1000", rowA.Traffic.UsedBytes)
	}
	if rowA.Traffic.ExceededAt == nil {
		t.Errorf("Traffic.ExceededAt 不应为 nil")
	}
	if rowA.Traffic.ResetDay == nil || *rowA.Traffic.ResetDay != 5 {
		t.Errorf("Traffic.ResetDay = %v, want 5", rowA.Traffic.ResetDay)
	}

	// 3. SQL 给它插两条 node_relays（name, server_host, server_port 用 relay-1.example.com 等）→ RelayCount == 2；另一节点 RelayCount == 0
	if err := e.exec(ctx, `INSERT INTO node_relays (parent_node_id, name, server_host, server_port) VALUES
		(?, '中转-1', 'relay-1.example.com', 8001),
		(?, '中转-2', 'relay-2.example.com', 8002)`, nodeA.ID, nodeA.ID); err != nil {
		t.Fatalf("插 node_relays: %v", err)
	}
	rowA = invFindRow(t, e, ctx, nodeA.ID)
	if rowA.RelayCount != 2 {
		t.Errorf("nodeA RelayCount = %d, want 2", rowA.RelayCount)
	}
	rowB := invFindRow(t, e, ctx, nodeB.ID)
	if rowB.RelayCount != 0 {
		t.Errorf("nodeB RelayCount = %d, want 0", rowB.RelayCount)
	}

	// 4. SQL 插一把当前 ECH 钥 → ECHEnabled == true，另一节点 false
	if err := e.exec(ctx, `INSERT INTO node_ech_keys (node_id, public_name, config_list, private_key_sealed)
		VALUES (?, 'ech.example.com', '\x0004fe0d0000'::bytea, '\x01'::bytea)`, nodeA.ID); err != nil {
		t.Fatalf("插 node_ech_keys: %v", err)
	}
	rowA = invFindRow(t, e, ctx, nodeA.ID)
	if !rowA.ECHEnabled {
		t.Errorf("nodeA ECHEnabled = %v, want true", rowA.ECHEnabled)
	}
	rowB = invFindRow(t, e, ctx, nodeB.ID)
	if rowB.ECHEnabled {
		t.Errorf("nodeB ECHEnabled = %v, want false", rowB.ECHEnabled)
	}
}
