package adminops

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// PlanPoolOption 是绑池选择器里的一个分组。
//
// Active 是生命周期 active 且定了协议的节点数，与节点池列表的 active_nodes 同口径；
// Deliverable 是其中真能写进订阅的节点数（subscription.DeliverableNodeSQL：服务器就绪、
// 服务状态 active、见过心跳、协议稳定、有可连地址），套餐页按它提示「0 节点」。
// 池限定用户组时，组外用户实际拿到的比 Deliverable 少，这一半与用户有关，不在这里算。
type PlanPoolOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Active      int    `json:"active_nodes"`
	Deliverable int    `json:"deliverable_nodes"`
	Bound       bool   `json:"bound"`
}

// PlanPoolBindings 是一个套餐当前可编辑（或只读展示）的版本及其绑池情况。
// 没有任何版本时 VersionID 为空串，Pools 仍列出全部可选分组且都未绑定。
type PlanPoolBindings struct {
	VersionID     string
	VersionStatus string
	RowVersion    int64
	Editable      bool
	Pools         []PlanPoolOption
}

// SetPlanPoolsInput 是已校验的绑池替换请求：PoolIDs 已排序去重。
type SetPlanPoolsInput struct {
	ActorID                   string
	VersionID                 string
	ExpectedVersionRowVersion int64
	PoolIDs                   []string
}

// PlanPools returns the mutable draft bindings when a draft exists. Without a
// draft it returns the current published snapshot as explicitly read-only.
func (s *Service) PlanPools(ctx context.Context, tenantID, planID string) (*PlanPoolBindings, error) {
	out := []PlanPoolOption{}
	var versionID, versionStatus string
	var versionRowVersion int64
	var editable bool

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var currentVersionID string
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(current_version_id::text, '')
			  FROM plans WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, planID).
			Scan(&currentVersionID); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		err := tx.QueryRow(ctx, `
			SELECT id::text, status, row_version
			  FROM plan_versions
			 WHERE tenant_id=$1 AND plan_id=$2::uuid
			   AND status='draft' AND frozen_at IS NULL
			 ORDER BY version DESC LIMIT 1`, tenantID, planID).
			Scan(&versionID, &versionStatus, &versionRowVersion)
		if err == nil {
			editable = true
		} else if err != pgx.ErrNoRows {
			return err
		} else if currentVersionID != "" {
			if err := tx.QueryRow(ctx, `
				SELECT id::text, status, row_version
				  FROM plan_versions
				 WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid`,
				tenantID, planID, currentVersionID).
				Scan(&versionID, &versionStatus, &versionRowVersion); err != nil {
				return err
			}
		}

		var versionArg any
		if versionID != "" {
			versionArg = versionID
		}
		rows, err := tx.Query(ctx, `
			SELECT p.id::text, p.name,
			       (SELECT count(*) FROM nodes n
			         WHERE n.pool_id=p.id AND n.status='active' AND n.node_type IS NOT NULL),
			       (SELECT count(*) FROM nodes n
			          JOIN servers s ON s.id = n.server_id AND s.tenant_id = n.tenant_id
			         WHERE n.tenant_id=p.tenant_id AND n.pool_id=p.id
			           AND `+subscription.DeliverableNodeSQL()+`),
			       EXISTS (SELECT 1 FROM plan_node_pools pnp
			                WHERE pnp.tenant_id=$1 AND pnp.pool_id=p.id
			                  AND pnp.plan_version_id=$2::uuid)
			  FROM node_pools p
			 WHERE p.tenant_id=$1 AND p.status<>'disabled'
			 ORDER BY p.name`, tenantID, versionArg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o PlanPoolOption
			if err := rows.Scan(&o.ID, &o.Name, &o.Active, &o.Deliverable, &o.Bound); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &PlanPoolBindings{VersionID: versionID, VersionStatus: versionStatus,
		RowVersion: versionRowVersion, Editable: editable, Pools: out}, nil
}

// ValidateEditablePlanPoolVersion 判定锁住的版本能不能改绑池：只许未冻结的草稿，且版本号对得上。
func ValidateEditablePlanPoolVersion(status string, frozen bool, current, expected int64) error {
	if status != "draft" || frozen {
		return httpx.New(httpx.CodeConflict, "只有未发布的草稿版本可以修改节点分组")
	}
	if current != expected {
		return &httpx.Error{
			Code: httpx.CodeConflict, Message: "套餐版本已被其他管理员修改，请刷新后重试",
			Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
		}
	}
	return nil
}

// SetPlanPools atomically replaces a draft plan version's pool bindings and
// returns the version's new row_version.
func (s *Service) SetPlanPools(ctx context.Context, tenantID, planID string, in SetPlanPoolsInput) (int64, error) {
	actorID := in.ActorID
	poolIDs := in.PoolIDs
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status string
		var frozen bool
		var current int64
		if err := tx.QueryRow(ctx, `
			SELECT pv.status, pv.frozen_at IS NOT NULL, pv.row_version
			  FROM plan_versions pv
			  JOIN plans p ON p.tenant_id=pv.tenant_id AND p.id=pv.plan_id
			 WHERE pv.tenant_id=$1 AND pv.plan_id=$2::uuid AND pv.id=$3::uuid
			 FOR UPDATE OF pv`, tenantID, planID, in.VersionID).
			Scan(&status, &frozen, &current); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if err := ValidateEditablePlanPoolVersion(status, frozen, current, in.ExpectedVersionRowVersion); err != nil {
			return err
		}

		before := []string{}
		rows, err := tx.Query(ctx, `
			SELECT pool_id::text FROM plan_node_pools
			 WHERE tenant_id=$1 AND plan_version_id=$2::uuid ORDER BY pool_id`,
			tenantID, in.VersionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			before = append(before, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// Deterministic locking prevents opposite request orders from deadlocking.
		for _, pid := range poolIDs {
			var locked string
			if err := tx.QueryRow(ctx, `
				SELECT id::text FROM node_pools
				 WHERE tenant_id=$1 AND id=$2::uuid AND status<>'disabled'
				 FOR KEY SHARE`, tenantID, pid).Scan(&locked); err != nil {
				if err == pgx.ErrNoRows {
					return httpx.Invalid(map[string]string{
						"pool_ids": "包含不存在或已禁用的节点分组",
					})
				}
				return err
			}
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM plan_node_pools
			 WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, in.VersionID); err != nil {
			return err
		}
		for _, pid := range poolIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id)
				VALUES ($1, $2::uuid, $3::uuid)`, tenantID, in.VersionID, pid); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE plan_versions SET row_version=row_version+1
			 WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid AND row_version=$4`,
			tenantID, planID, in.VersionID, in.ExpectedVersionRowVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return &httpx.Error{
				Code: httpx.CodeConflict, Message: "套餐版本已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
			}
		}
		next = current + 1
		versionID := in.VersionID
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "plan_version.pools_changed", ResourceType: "plan_version", ResourceID: &versionID,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"row_version": current, "pool_ids": before},
			AfterDigest:  map[string]any{"row_version": next, "pool_ids": poolIDs, "plan_id": planID},
		})
	})
	return next, err
}
