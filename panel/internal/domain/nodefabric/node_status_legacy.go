package nodefabric

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// LegacyNodeStatusInput 是旧状态接口（POST v1/nodes/{id}/status）的请求。
type LegacyNodeStatusInput struct {
	ActorID    string
	RowVersion int64
	Status     string
	Reason     string
}

func nodeStatusLockSQL() string {
	return `SELECT status,row_version,
		COALESCE(node_type IS NOT NULL
		AND server_port BETWEEN 1 AND 65535
		AND ` + StableProtocolReadySQL("") + `, false)
	FROM nodes WHERE tenant_id=$1 AND id=$2 FOR UPDATE`
}

// SetLegacyNodeStatus 推进节点状态。合法性由数据库的 node_transitions 表强制 ——
// 这里不重复实现一遍状态机，避免两处规则漂移（NODE-010）。
func (s *Service) SetLegacyNodeStatus(ctx context.Context, tenantID, id string, in LegacyNodeStatusInput) error {
	actor := in.ActorID
	terminal := in.Status == "retired" || in.Status == "destroyed"

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			if terminal {
				if _, err := tx.Exec(ctx,
					`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
					"node-config-release/"+tenantID); err != nil {
					return err
				}
			}
			var before string
			var currentVersion int64
			var protocolReady bool
			if err := tx.QueryRow(ctx, nodeStatusLockSQL(),
				tenantID, id).Scan(&before, &currentVersion, &protocolReady); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeNotFound, "节点不存在")
				}
				return err
			}
			if in.RowVersion <= 0 {
				return httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
			}
			if currentVersion != in.RowVersion {
				return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
					Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
			}
			servingStatus, serverStatus := ProjectNodeLifecycle(in.Status, protocolReady)
			if _, err := tx.Exec(ctx,
				`UPDATE nodes SET status=$3,serving_status=$4,
				 desired_config_version=CASE WHEN $4='retired' THEN NULL ELSE desired_config_version END,
				 row_version=row_version+1
				 WHERE tenant_id=$1 AND id=$2 AND row_version=$5`,
				tenantID, id, in.Status, servingStatus, currentVersion); err != nil {
				// 违反状态机的跳转由触发器抛 check_violation；约束的英文原句不透给页面
				if db.IsCheckViolation(err) {
					return NodeStatusRefusal(err)
				}
				return err
			}
			// Server 与 Node 的状态词不同；第二条更新单独写入正确的宿主状态。
			if _, err := tx.Exec(ctx, `
				UPDATE servers SET status=$3, status_reason=nullif($4,''),
				       entered_status_at=now(), row_version=row_version+1,
				       retired_at=CASE WHEN $3='retired' THEN now() ELSE retired_at END
				 WHERE tenant_id=$1 AND id=(SELECT server_id FROM nodes WHERE tenant_id=$1 AND id=$2)
				   AND control_node_id=$2 AND deleted_at IS NULL`,
				tenantID, id, serverStatus, in.Reason); err != nil {
				return err
			}
			if terminal {
				if _, err := tx.Exec(ctx, `UPDATE node_identities
					SET status='revoked',revoked_at=now(),revoked_reason='节点已退役'
					WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenantID, id); err != nil {
					return err
				}
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.status_change", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID:    httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{"status": before},
				AfterDigest:  map[string]any{"status": in.Status, "reason": in.Reason},
			})
		})
}

// RevokeNodeIdentity 吊销节点身份（NODE-014）。
// 吊销后该节点的 Agent 下一次请求就会被拒，必须重新引导。
func (s *Service) RevokeNodeIdentity(ctx context.Context, tenantID, actor, id string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, `
				UPDATE node_identities
				   SET status='revoked', revoked_at=now(), revoked_reason='管理员手工吊销'
				 WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenantID, id)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return httpx.New(httpx.CodeNotFound, "该节点没有有效身份")
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.identity.revoke", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID: httpx.RequestIDFrom(ctx),
			})
		})
}
