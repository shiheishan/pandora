// [INPUT]: 依赖 node_admin.go 的 AdminNode、输入类型、校验与 lockServerCapacity，依赖 config_publish.go 的 lockLegacyConfigRelease / syncLegacyDesiredConfigVersion，依赖 platform 的 audit/db/httpx
// [OUTPUT]: 对外提供 Service 的 CloneAdminNode、MoveAdminNode、ReorderAdminNodes
// [POS]: domain/nodefabric 后台节点的摆放：从 node_admin.go 拆出。复制在发布锁下物化当前适用配置，移动与排序带 row_version 乐观锁并同事务审计
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (s *Service) CloneAdminNode(ctx context.Context, tenantID, id string, in CloneAdminNodeInput) (*AdminNode, error) {
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if err := validateAdminUUID("target_server_id", in.TargetServerID, false); err != nil {
		return nil, err
	}
	if err := validateAdminUUID("pool_id", in.PoolID, false); err != nil {
		return nil, err
	}
	var cloneID string
	var legacyProtocol bool
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		before, err := scanAdminNode(tx.QueryRow(ctx, adminNodeSelect+` WHERE n.tenant_id=$1 AND n.id=$2::uuid FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if before.RowVersion != in.RowVersion {
			return nodeVersionConflict(before.RowVersion)
		}
		legacyProtocol = before.ProtocolSchemaVersion == 0
		if before.ServerID == nil {
			return httpx.New(httpx.CodeConflict, "原节点未绑定服务器")
		}
		targetServerID := strings.TrimSpace(in.TargetServerID)
		if targetServerID == "" {
			targetServerID = *before.ServerID
		}
		if err := s.lockServerCapacity(ctx, tx, tenantID, targetServerID); err != nil {
			return err
		}
		poolID := value(before.PoolID)
		if in.PoolID != "" {
			poolID = in.PoolID
		}
		if err := validatePool(ctx, tx, tenantID, poolID); err != nil {
			return err
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = before.Name + "-copy"
		}
		if err := validateAdminNodeName(name); err != nil {
			return err
		}
		sortOrder := before.SortOrder + 1
		if in.SortOrder != nil {
			sortOrder = *in.SortOrder
		}
		err = tx.QueryRow(ctx, `INSERT INTO nodes
			(tenant_id,name,server_id,pool_id,status,serving_status,node_type,server_host,
			 server_port,kernel,traffic_rate,display_name,protocol_config,
			 protocol_schema_version,config_validated_at,sort_order,row_version,country_code)
			SELECT tenant_id,$3,$4::uuid,nullif($5,'')::uuid,'draft','draft',
			 CASE WHEN protocol_schema_version=0 THEN NULL ELSE node_type END,
			 CASE WHEN protocol_schema_version=0 THEN NULL ELSE server_host END,
			 CASE WHEN protocol_schema_version=0 THEN NULL ELSE server_port END,
			 kernel,traffic_rate,display_name,
			 CASE WHEN protocol_schema_version=0 THEN '{}'::jsonb ELSE protocol_config END,
			 protocol_schema_version,
			 CASE WHEN protocol_schema_version=0 THEN NULL ELSE config_validated_at END,
			 $6,1,country_code
			FROM nodes WHERE tenant_id=$1 AND id=$2::uuid RETURNING id`,
			tenantID, id, name, targetServerID, poolID, sortOrder).Scan(&cloneID)
		if db.IsUniqueViolation(err) {
			return httpx.New(httpx.CodeConflict, "节点名称已存在")
		}
		if err != nil {
			return err
		}
		if err := syncLegacyDesiredConfigVersion(ctx, tx, tenantID, cloneID); err != nil {
			return err
		}
		if in.CopyRouting {
			if _, err := tx.Exec(ctx, `INSERT INTO node_outbounds
				(tenant_id,node_id,tag,type,settings,sort_order)
				SELECT tenant_id,$3::uuid,tag,type,settings,sort_order FROM node_outbounds
				WHERE tenant_id=$1 AND node_id=$2::uuid`, tenantID, id, cloneID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO node_routes
				(tenant_id,node_id,priority,matcher,outbound_tag,enabled,note)
				SELECT tenant_id,$3::uuid,priority,matcher,outbound_tag,enabled,note FROM node_routes
				WHERE tenant_id=$1 AND node_id=$2::uuid`, tenantID, id, cloneID); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.copy", ResourceType: "node", ResourceID: &cloneID, APIDomain: "admin",
			RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"source_node_id": id},
			AfterDigest: map[string]any{"name": name, "server_id": targetServerID,
				"pool_id": poolID, "serving_status": "draft", "copy_routing": in.CopyRouting}})
	})
	if err != nil {
		return nil, err
	}
	out, err := s.GetAdminNode(ctx, tenantID, cloneID)
	if err == nil && legacyProtocol {
		out.Warnings = []string{"源节点为 legacy v0 协议；副本未复制协议配置，请选择已开放的稳定协议后再启用"}
	}
	return out, err
}

func (s *Service) MoveAdminNode(ctx context.Context, tenantID, id string, in MoveAdminNodeInput) (*AdminNode, error) {
	if in.RowVersion <= 0 || in.ServerID == "" {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号", "server_id": "必填"})
	}
	if err := validateAdminUUID("server_id", in.ServerID, true); err != nil {
		return nil, err
	}
	if len([]rune(strings.TrimSpace(in.Reason))) > 500 {
		return nil, httpx.Invalid(map[string]string{"reason": "最多 500 个字符"})
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		before, err := scanAdminNode(tx.QueryRow(ctx, adminNodeSelect+` WHERE n.tenant_id=$1 AND n.id=$2::uuid FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if before.RowVersion != in.RowVersion {
			return nodeVersionConflict(before.RowVersion)
		}
		if before.ServerID != nil && *before.ServerID == in.ServerID {
			return nil
		}
		if before.ServingStatus == "active" || before.ServingStatus == "draining" {
			return httpx.New(httpx.CodeConflict, "活动或排空中的节点不能移动，请先停用")
		}
		var control, identity, runtime, metrics, tasks, provisioning, bootstrap int
		var configApps, usageSources, usageBatches, trafficReports int
		var activeToken bool
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*)::int FROM servers WHERE tenant_id=$1 AND control_node_id=$2::uuid),
			(SELECT count(*)::int FROM node_identities WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM runtime_instances WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM node_metrics WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM node_tasks WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM provisioning_runs WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM bootstrap_tokens WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM node_config_applications WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM usage_sources WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM usage_batches WHERE tenant_id=$1 AND node_id=$2::uuid),
			(SELECT count(*)::int FROM node_traffic_reports WHERE tenant_id=$1 AND node_id=$2::uuid),
			EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2::uuid AND server_token_hash IS NOT NULL)`,
			tenantID, id).Scan(&control, &identity, &runtime, &metrics, &tasks, &provisioning, &bootstrap,
			&configApps, &usageSources, &usageBatches, &trafficReports, &activeToken); err != nil {
			return err
		}
		if control+identity+runtime+metrics+tasks+provisioning+bootstrap+configApps+usageSources+usageBatches+trafficReports > 0 || activeToken {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "节点仍绑定控制面或 Agent 资产，不能移动",
				Fields: map[string]string{"control_node": fmt.Sprint(control), "active_identities": fmt.Sprint(identity),
					"runtime_instances": fmt.Sprint(runtime), "metrics": fmt.Sprint(metrics), "pending_tasks": fmt.Sprint(tasks),
					"provisioning": fmt.Sprint(provisioning), "bootstrap_tokens": fmt.Sprint(bootstrap),
					"config_applications": fmt.Sprint(configApps), "usage_sources": fmt.Sprint(usageSources),
					"usage_batches":       fmt.Sprint(usageBatches),
					"traffic_reports":     fmt.Sprint(trafficReports),
					"active_server_token": fmt.Sprint(activeToken)}}
		}
		if err := s.lockServerCapacity(ctx, tx, tenantID, in.ServerID); err != nil {
			return err
		}
		ct, err := tx.Exec(ctx, `UPDATE nodes SET server_id=$4::uuid,row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid
			AND row_version=$3`, tenantID, id, in.RowVersion, in.ServerID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return nodeVersionConflict(before.RowVersion)
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.server_move", ResourceType: "node", ResourceID: &id, APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"server_id": before.ServerID},
			AfterDigest:  map[string]any{"server_id": in.ServerID, "reason": in.Reason}})
	})
	if err != nil {
		return nil, err
	}
	return s.GetAdminNode(ctx, tenantID, id)
}

func (s *Service) ReorderAdminNodes(ctx context.Context, tenantID string, in ReorderNodesInput) error {
	if len(in.Items) == 0 || len(in.Items) > 200 {
		return httpx.Invalid(map[string]string{"items": "必须包含 1 到 200 个节点"})
	}
	sort.Slice(in.Items, func(i, j int) bool { return in.Items[i].ID < in.Items[j].ID })
	seen := map[string]bool{}
	for _, item := range in.Items {
		if validateAdminUUID("items.id", item.ID, true) != nil || item.RowVersion <= 0 || seen[item.ID] {
			return httpx.Invalid(map[string]string{"items": "节点 ID 必须唯一、格式正确且包含 row_version"})
		}
		seen[item.ID] = true
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// Phase 1 obtains every row lock in deterministic ID order and verifies
		// every version before any mutation. This avoids update-then-wait lock
		// cycles and makes stale batches fail without touching a row.
		for _, item := range in.Items {
			var current int64
			err := tx.QueryRow(ctx, `SELECT row_version FROM nodes
				WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, item.ID).Scan(&current)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			if err != nil {
				return err
			}
			if current != item.RowVersion {
				return nodeVersionConflict(current)
			}
		}
		// Phase 2 applies the already-validated batch.
		for _, item := range in.Items {
			ct, err := tx.Exec(ctx, `UPDATE nodes SET sort_order=$4,row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid
			 AND row_version=$3`, tenantID, item.ID, item.RowVersion, item.SortOrder)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return nodeVersionConflict(item.RowVersion)
			}
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.order_batch", ResourceType: "node_set", APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"count": len(in.Items)}})
	})
}
