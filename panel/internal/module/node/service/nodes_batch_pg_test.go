package service

// 批量修改节点的 PG 测试。

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestBatchEditNodesSuccess(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "batch-edit-srv1")
	n1 := e.ssNode(t, ctx, srv.ID, "n1", 22001)
	n2 := e.ssNode(t, ctx, srv.ID, "n2", 22002)

	rate := decimal.RequireFromString("1.5")
	limit := int64(1 << 30)

	n, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items: []NodeVersionRef{
			{ID: n1.ID, RowVersion: n1.RowVersion},
			{ID: n2.ID, RowVersion: n2.RowVersion},
		},
		Set: NodeBatchFields{
			Tags:              &[]string{"香港"},
			TrafficRate:       &rate,
			TrafficLimitBytes: OptionalInt64{Set: true, Value: &limit},
		},
		Reason: "批量调整标签与倍率",
	})
	if err != nil {
		t.Fatalf("BatchEditNodes 失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("返回数量 %d want 2", n)
	}

	got1, err := e.svc.GetAdminNode(ctx, n1.ID)
	if err != nil {
		t.Fatalf("GetAdminNode n1: %v", err)
	}
	if len(got1.Tags) != 1 || got1.Tags[0] != "香港" {
		t.Errorf("n1 tags = %v want [香港]", got1.Tags)
	}
	if !got1.TrafficRate.Equal(rate) {
		t.Errorf("n1 rate = %v want 1.5", got1.TrafficRate)
	}
	if got1.Traffic.LimitBytes == nil || *got1.Traffic.LimitBytes != limit {
		t.Errorf("n1 limit = %v want %d", got1.Traffic.LimitBytes, limit)
	}

	got2, err := e.svc.GetAdminNode(ctx, n2.ID)
	if err != nil {
		t.Fatalf("GetAdminNode n2: %v", err)
	}
	if len(got2.Tags) != 1 || got2.Tags[0] != "香港" {
		t.Errorf("n2 tags = %v want [香港]", got2.Tags)
	}
	if !got2.TrafficRate.Equal(rate) {
		t.Errorf("n2 rate = %v want 1.5", got2.TrafficRate)
	}
	if got2.Traffic.LimitBytes == nil || *got2.Traffic.LimitBytes != limit {
		t.Errorf("n2 limit = %v want %d", got2.Traffic.LimitBytes, limit)
	}

	// 审计：node.batch_update 一条、node.update 两条（限定到本用例：库里可能留有别的用例提交的审计行）
	var batchAuditCount int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events
		 WHERE action = 'node.batch_update' AND resource_type = 'node_set' AND data->>'reason' = ?`, "批量调整标签与倍率").Scan(&batchAuditCount); err != nil {
		t.Fatal(err)
	}
	if batchAuditCount != 1 {
		t.Errorf("node.batch_update count = %d want 1", batchAuditCount)
	}

	var updateAuditCount int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events
		 WHERE action = 'node.update' AND resource_type = 'node' AND resource_id IN (?, ?)`, n1.ID, n2.ID).Scan(&updateAuditCount); err != nil {
		t.Fatal(err)
	}
	if updateAuditCount != 2 {
		t.Errorf("node.update count = %d want 2", updateAuditCount)
	}
}

func TestBatchEditNodesVersionConflictRollback(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "batch-edit-srv2")
	n1 := e.ssNode(t, ctx, srv.ID, "n1", 22003)
	n2 := e.ssNode(t, ctx, srv.ID, "n2", 22004)

	rate := decimal.RequireFromString("2.0")
	_, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items: []NodeVersionRef{
			{ID: n1.ID, RowVersion: n1.RowVersion},
			{ID: n2.ID, RowVersion: n2.RowVersion + 10}, // 版本冲突
		},
		Set: NodeBatchFields{
			TrafficRate: &rate,
		},
		Reason: "测试版本冲突回滚",
	})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["node_id"] != n2.ID.String() {
		t.Errorf("node_id = %q want %s", ee.Fields["node_id"], n2.ID.String())
	}

	// 整批回滚，两个节点都没变
	got1, err := e.svc.GetAdminNode(ctx, n1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.RowVersion != n1.RowVersion || got1.TrafficRate.Equal(rate) {
		t.Errorf("n1 被意外修改: ver=%d rate=%v", got1.RowVersion, got1.TrafficRate)
	}

	got2, err := e.svc.GetAdminNode(ctx, n2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.RowVersion != n2.RowVersion || got2.TrafficRate.Equal(rate) {
		t.Errorf("n2 被意外修改: ver=%d rate=%v", got2.RowVersion, got2.TrafficRate)
	}
}

func TestBatchEditNodesNotFound(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "batch-edit-srv3")
	n1 := e.ssNode(t, ctx, srv.ID, "n1", 22005)

	rate := decimal.RequireFromString("1.5")
	_, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items: []NodeVersionRef{
			{ID: n1.ID, RowVersion: n1.RowVersion},
			{ID: uuid.New(), RowVersion: 1},
		},
		Set: NodeBatchFields{
			TrafficRate: &rate,
		},
		Reason: "测试404",
	})
	_ = invWantCode(t, err, errs.NotFound)
}

func TestBatchEditNodesValidation(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "batch-edit-srv4")
	n1 := e.ssNode(t, ctx, srv.ID, "n1", 22006)

	// Set 为空
	_, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items:  []NodeVersionRef{{ID: n1.ID, RowVersion: n1.RowVersion}},
		Set:    NodeBatchFields{},
		Reason: "无字段",
	})
	ee := invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["set"] == "" {
		t.Errorf("want field set in %v", ee.Fields)
	}

	// 101 个条目
	items101 := make([]NodeVersionRef, 101)
	for i := range items101 {
		items101[i] = NodeVersionRef{ID: uuid.New(), RowVersion: 1}
	}
	rate := decimal.RequireFromString("1.5")
	_, err = e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items:  items101,
		Set:    NodeBatchFields{TrafficRate: &rate},
		Reason: "101个条目",
	})
	ee = invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["items"] == "" {
		t.Errorf("want field items in %v", ee.Fields)
	}

	// 字段不合法（Tags: &[]string{""}）
	_, err = e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items:  []NodeVersionRef{{ID: n1.ID, RowVersion: n1.RowVersion}},
		Set:    NodeBatchFields{Tags: &[]string{""}},
		Reason: "非法标签",
	})
	ee = invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["tags"] == "" {
		t.Errorf("want field tags in %v", ee.Fields)
	}
}

func TestBatchEditNodesStatusAndFields(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "batch-edit-srv5")
	poolID := e.pool(t, ctx, "active")
	e.enroll(t, ctx, srv.ID)
	n1 := e.ssNode(t, ctx, srv.ID, "n1", 22007)
	n2 := e.ssNode(t, ctx, srv.ID, "n2", 22008)

	// 草稿只能转在役或退役：先用 SQL 把两个节点挂池并置为在役，再批量转停用（在役 -> 停用是允许的转换）
	if err := e.exec(ctx, `UPDATE nodes SET serving_status = 'active', pool_id = ? WHERE id IN (?, ?)`, poolID, n1.ID, n2.ID); err != nil {
		t.Fatal(err)
	}
	cur1, err := e.svc.GetAdminNode(ctx, n1.ID)
	if err != nil {
		t.Fatal(err)
	}
	cur2, err := e.svc.GetAdminNode(ctx, n2.ID)
	if err != nil {
		t.Fatal(err)
	}

	rate := decimal.RequireFromString("2.0")
	n, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
		Items: []NodeVersionRef{
			{ID: n1.ID, RowVersion: cur1.RowVersion},
			{ID: n2.ID, RowVersion: cur2.RowVersion},
		},
		Set: NodeBatchFields{
			ServingStatus: NodeDisabled,
			TrafficRate:   &rate,
		},
		Reason: "批量转停用并改倍率",
	})
	if err != nil {
		t.Fatalf("BatchEditNodes status+fields: %v", err)
	}
	if n != 2 {
		t.Fatalf("返回数量 %d want 2", n)
	}

	got1, err := e.svc.GetAdminNode(ctx, n1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.ServingStatus != NodeDisabled || !got1.TrafficRate.Equal(rate) {
		t.Errorf("n1: status=%s rate=%v", got1.ServingStatus, got1.TrafficRate)
	}

	got2, err := e.svc.GetAdminNode(ctx, n2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.ServingStatus != NodeDisabled || !got2.TrafficRate.Equal(rate) {
		t.Errorf("n2: status=%s rate=%v", got2.ServingStatus, got2.TrafficRate)
	}
}

func TestBatchEditNodesPatchFailureRollback(t *testing.T) {
	e, ctx := newInvEnv(t)
	poolID := e.pool(t, ctx, "active")
	srv := e.readyServer(t, ctx, "batch-edit-srv6")
	e.enroll(t, ctx, srv.ID)

	n1 := e.ssNode(t, ctx, srv.ID, "n1", 22009)
	n2 := e.ssNode(t, ctx, srv.ID, "n2", 22010)

	// 把 n1 置为 active 并归入池
	if err := e.exec(ctx, `UPDATE nodes SET serving_status = 'active', pool_id = ? WHERE id = ?`, poolID, n1.ID); err != nil {
		t.Fatal(err)
	}
	// 把 n2 归入同一个池，serving_status 仍为 draft
	if err := e.exec(ctx, `UPDATE nodes SET pool_id = ? WHERE id = ?`, poolID, n2.ID); err != nil {
		t.Fatal(err)
	}

	// 重新读取两者的当前版本
	gotN1, err := e.svc.GetAdminNode(ctx, n1.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotN2, err := e.svc.GetAdminNode(ctx, n2.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 尝试批量将 PoolID 置空：n1 作为 active 节点移出会失败（422 pool_id），n2 的池也不得被清空
	// 回滚环境里服务的事务加入外层测试事务，用保存点模拟整批回滚（按 id 升序处理，n2 可能先被改过）
	err = e.invSavepoint(t, ctx, func() error {
		_, err := e.svc.BatchEditNodes(ctx, NodeBatchEdit{
			Items: []NodeVersionRef{
				{ID: n1.ID, RowVersion: gotN1.RowVersion},
				{ID: n2.ID, RowVersion: gotN2.RowVersion},
			},
			Set: NodeBatchFields{
				PoolID: OptionalUUID{Set: true, Value: nil},
			},
			Reason: "测试将活跃节点移出池报错整批回滚",
		})
		return err
	})
	ee := invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["pool_id"] == "" {
		t.Errorf("want field pool_id in %v", ee.Fields)
	}
	if ee.Fields["node_id"] != n1.ID.String() {
		t.Errorf("node_id = %q want %s", ee.Fields["node_id"], n1.ID.String())
	}

	// 检查 n2 的池没有变
	freshN2, err := e.svc.GetAdminNode(ctx, n2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshN2.PoolID == nil || *freshN2.PoolID != poolID {
		t.Errorf("n2 的池被意外修改: %v", freshN2.PoolID)
	}
}
