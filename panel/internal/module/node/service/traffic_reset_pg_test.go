package service

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestResetNodeTrafficPG(t *testing.T) {
	env, ctx := newInvEnv(t)
	srv := env.readyServer(t, ctx, "rt-srv")
	node := env.ssNode(t, ctx, srv.ID, "rt-node", 8388)

	// 设置超限与用量
	var oldResetAt time.Time
	err := env.row(ctx, `
		UPDATE nodes
		SET traffic_limit_bytes = 100,
		    traffic_used_bytes = 150,
		    traffic_exceeded_at = now() - interval '1 hour',
		    traffic_reset_at = now() - interval '2 days'
		WHERE id = ?
		RETURNING traffic_reset_at
	`, node.ID).Scan(&oldResetAt)
	if err != nil {
		t.Fatalf("设置超限: %v", err)
	}

	reason := "测试手动重置"
	if err := env.svc.ResetNodeTraffic(ctx, node.ID, reason); err != nil {
		t.Fatalf("ResetNodeTraffic: %v", err)
	}

	var used int64
	var exceededAt sql.NullTime
	var newResetAt time.Time
	err = env.row(ctx, `
		SELECT traffic_used_bytes, traffic_exceeded_at, traffic_reset_at
		FROM nodes
		WHERE id = ?
	`, node.ID).Scan(&used, &exceededAt, &newResetAt)
	if err != nil {
		t.Fatalf("查询重置后节点: %v", err)
	}
	if used != 0 {
		t.Errorf("traffic_used_bytes = %d want 0", used)
	}
	if exceededAt.Valid {
		t.Errorf("traffic_exceeded_at = %v want NULL", exceededAt.Time)
	}
	if !newResetAt.After(oldResetAt) {
		t.Errorf("traffic_reset_at %v 应该晚于旧时刻 %v", newResetAt, oldResetAt)
	}

	// 验证审计事件
	var auditAction string
	var auditData string
	err = env.row(ctx, `
		SELECT action, data::text
		FROM audit_events
		WHERE resource_type = 'node' AND resource_id = ? AND action = 'node.traffic_reset'
	`, node.ID).Scan(&auditAction, &auditData)
	if err != nil {
		t.Fatalf("查询审计事件: %v", err)
	}
	if auditAction != "node.traffic_reset" {
		t.Errorf("audit action = %q want node.traffic_reset", auditAction)
	}

	// 验证不存在的节点返回 404
	err = env.svc.ResetNodeTraffic(ctx, uuid.New(), "reason")
	invWantCode(t, err, errs.NotFound)
}

func TestResetDueNodeTrafficPG(t *testing.T) {
	env, ctx := newInvEnv(t)
	srv := env.readyServer(t, ctx, "rd-srv")

	// 节点 1：设了重置日 1 号，reset_at 在 40 天前，已用 500
	n1 := env.ssNode(t, ctx, srv.ID, "rd-n1", 8389)
	if err := env.exec(ctx, `
		UPDATE nodes
		SET traffic_reset_day = 1,
		    traffic_reset_at = now() - interval '40 days',
		    traffic_used_bytes = 500,
		    traffic_limit_bytes = 400,
		    traffic_exceeded_at = now() - interval '39 days'
		WHERE id = ?
	`, n1.ID); err != nil {
		t.Fatalf("配置节点 1: %v", err)
	}

	// 节点 2：设了重置日 1 号，reset_at 是当前时刻，已用 200（本周期尚未到期）
	n2 := env.ssNode(t, ctx, srv.ID, "rd-n2", 8390)
	if err := env.exec(ctx, `
		UPDATE nodes
		SET traffic_reset_day = 1,
		    traffic_reset_at = now(),
		    traffic_used_bytes = 200
		WHERE id = ?
	`, n2.ID); err != nil {
		t.Fatalf("配置节点 2: %v", err)
	}

	// 节点 3：未设重置日（NULL），已用 300
	n3 := env.ssNode(t, ctx, srv.ID, "rd-n3", 8391)
	if err := env.exec(ctx, `
		UPDATE nodes
		SET traffic_reset_day = NULL,
		    traffic_reset_at = now() - interval '40 days',
		    traffic_used_bytes = 300
		WHERE id = ?
	`, n3.ID); err != nil {
		t.Fatalf("配置节点 3: %v", err)
	}

	count, err := env.svc.ResetDueNodeTraffic(ctx)
	if err != nil {
		t.Fatalf("ResetDueNodeTraffic: %v", err)
	}
	if count < 1 {
		t.Errorf("count = %d want >= 1", count)
	}

	// 校验节点 1：用量清零，超限标记清除
	var used1 int64
	var exceeded1 sql.NullTime
	if err := env.row(ctx, `SELECT traffic_used_bytes, traffic_exceeded_at FROM nodes WHERE id = ?`, n1.ID).Scan(&used1, &exceeded1); err != nil {
		t.Fatalf("查节点 1: %v", err)
	}
	if used1 != 0 {
		t.Errorf("节点 1 traffic_used_bytes = %d want 0", used1)
	}
	if exceeded1.Valid {
		t.Errorf("节点 1 traffic_exceeded_at = %v want NULL", exceeded1.Time)
	}

	// 校验节点 2：用量未被清零
	var used2 int64
	if err := env.row(ctx, `SELECT traffic_used_bytes FROM nodes WHERE id = ?`, n2.ID).Scan(&used2); err != nil {
		t.Fatalf("查节点 2: %v", err)
	}
	if used2 != 200 {
		t.Errorf("节点 2 traffic_used_bytes = %d want 200", used2)
	}

	// 校验节点 3：未设重置日，用量未动
	var used3 int64
	if err := env.row(ctx, `SELECT traffic_used_bytes FROM nodes WHERE id = ?`, n3.ID).Scan(&used3); err != nil {
		t.Fatalf("查节点 3: %v", err)
	}
	if used3 != 300 {
		t.Errorf("节点 3 traffic_used_bytes = %d want 300", used3)
	}
}
