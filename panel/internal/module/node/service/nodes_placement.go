package service

// 节点的复制、迁移与排序。

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// nodeReorderMax 是一次排序最多的节点数。
const nodeReorderMax = 200

// CopyNode 把节点复制成另一台（或同一台）服务器上的草稿：协议原样复制，端口可换；
// CopyRouting 时连节点私有路由与所在路由组一起复制（copyNodeRouting）。
func (s *Service) CopyNode(ctx context.Context, id uuid.UUID, in NodeCopyInput) (AdminNode, error) {
	if in.RowVersion <= 0 {
		return AdminNode{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}
	if in.ServerPort != nil && (*in.ServerPort < 1 || *in.ServerPort > 65535) {
		return AdminNode{}, inventoryInvalid("server_port", "端口必须在 1 到 65535 之间")
	}
	var copyID uuid.UUID
	var warnings []string
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if in.CopyRouting {
			// 锁序同其它改路由的写：先拿路由写锁，再锁节点行
			if err := s.repo.LockRoutingWrites(ctx); err != nil {
				return err
			}
		}
		before, found, err := s.repo.NodeByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if before.RowVersion != in.RowVersion {
			return nodeVersionConflict(before.RowVersion)
		}
		if before.ServerID == nil {
			return inventoryConflict("原节点未绑定服务器")
		}
		target := *before.ServerID
		if in.TargetServerID != nil {
			target = *in.TargetServerID
		}
		srv, err := s.lockServerForNodes(ctx, target)
		if err != nil {
			return err
		}
		port := nodeInt(before.ServerPort)
		if in.ServerPort != nil {
			port = *in.ServerPort
		}
		l4 := protocol.ListenL4(nodeStr(before.NodeType), json.RawMessage(before.ProtocolConfig))
		warnings, err = s.claimNodePort(ctx, srv, nodePortClaim{
			ServerID: target, Port: port, L4: l4, Host: nodeStr(before.ServerHost), CheckReserved: true,
		})
		if err != nil {
			return err
		}
		pool := before.PoolID
		if in.PoolID != nil {
			pool = in.PoolID
		}
		if err := s.nodeLockPool(ctx, pool); err != nil {
			return err
		}
		rawName := strings.TrimSpace(in.Name)
		if rawName == "" {
			rawName = before.Name + "-copy"
		}
		name, err := nodeValidateName(rawName)
		if err != nil {
			return err
		}
		sortOrder := before.SortOrder + 1
		if in.SortOrder != nil {
			sortOrder = *in.SortOrder
		}
		n := model.Node{
			ServerID:              &target,
			PoolID:                pool,
			Name:                  name,
			DisplayName:           before.DisplayName,
			CountryCode:           before.CountryCode,
			ServingStatus:         NodeDraft,
			NodeType:              before.NodeType,
			ServerHost:            before.ServerHost,
			ProtocolConfig:        before.ProtocolConfig,
			ProtocolSchemaVersion: before.ProtocolSchemaVersion,
			ConfigValidatedAt:     before.ConfigValidatedAt,
			TrafficRate:           before.TrafficRate,
			SortOrder:             sortOrder,
			Tags:                  before.Tags,
			TrafficLimitBytes:     before.TrafficLimitBytes,
			TrafficResetDay:       before.TrafficResetDay,
			TrafficRateSchedule:   before.TrafficRateSchedule,
		}
		if n.ProtocolConfig == "" {
			n.ProtocolConfig = "{}"
		}
		if port > 0 {
			n.ServerPort = &port
			n.ListenL4 = nodeOpt(l4)
		}
		if err := s.repo.InsertNode(ctx, &n); err != nil {
			return nodeWriteErr(err, port, l4)
		}
		s.nodeChanged(ctx, realtime.OpInsert, n.ID)
		copyID = n.ID
		var copied routingCopy
		if in.CopyRouting {
			if copied, err = s.copyNodeRouting(ctx, id, copyID); err != nil {
				return err
			}
		}
		var poolID any
		if pool != nil {
			poolID = pool.String()
		}
		if err := s.nodeAudit(ctx, "node.copy", copyID, map[string]any{
			"source_node_id": id.String(), "name": name, "server_id": target.String(),
			"pool_id": poolID, "copy_routing": in.CopyRouting,
			"outbounds": copied.outbounds, "routes": copied.routes, "route_groups": copied.groups,
		}); err != nil {
			return err
		}
		s.MarkConfigChanged(ctx)
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	return s.nodeWithWarnings(ctx, copyID, warnings)
}

// routingCopy 是 copyNodeRouting 复制的条数，进审计。
type routingCopy struct{ outbounds, routes, groups int }

// copyNodeRouting 把源节点的私有出站、规则和路由组成员关系原样复制给副本。在调用方事务里调，调用方已拿路由写锁。
// 组成员一起复制：私有规则可以指向所在组的出站，只复制规则会造出悬空引用。复制后照常用 refuseNewDangling 拒绝新出现的悬空引用。
func (s *Service) copyNodeRouting(ctx context.Context, srcID, dstID uuid.UUID) (routingCopy, error) {
	outs, routes, err := s.repo.ScopeRouting(ctx, repo.RoutingScope{NodeID: &srcID})
	if err != nil {
		return routingCopy{}, err
	}
	rows, err := s.repo.NodeRouteGroups(ctx, []uuid.UUID{srcID})
	if err != nil {
		return routingCopy{}, err
	}
	groups := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, row.GroupID)
	}
	groups = normalizeIDs(groups)
	if err := s.repo.LockRouteGroups(ctx, groups); err != nil {
		return routingCopy{}, err
	}
	before, err := s.repo.DanglingRefs(ctx)
	if err != nil {
		return routingCopy{}, err
	}
	if err := s.repo.ReplaceScopeRouting(ctx, repo.RoutingScope{NodeID: &dstID}, outs, routes); err != nil {
		return routingCopy{}, routingWriteErr(err)
	}
	if err := s.repo.ReplaceNodeGroups(ctx, dstID, groups); err != nil {
		return routingCopy{}, err
	}
	if err := refuseNewDangling(ctx, s, before, "原节点的路由有悬空引用，修好后再复制路由"); err != nil {
		return routingCopy{}, err
	}
	if err := s.repo.BumpRouteGroupVersions(ctx, groups); err != nil {
		return routingCopy{}, err
	}
	return routingCopy{outbounds: len(outs), routes: len(routes), groups: len(groups)}, nil
}

// nodeMovable 报告节点能不能迁移：只有从没服务过的草稿（没有配置回执、没有运行状态）。
func nodeMovable(n model.Node) bool {
	return n.ServingStatus == NodeDraft && n.AppliedConfigSHA256 == nil &&
		n.ApplyFailedSHA256 == nil && n.RuntimeStatus == nil
}

// MoveNode 把从没服务过的草稿节点迁到另一台服务器；目标就是当前服务器时不改。
func (s *Service) MoveNode(ctx context.Context, id uuid.UUID, in NodeMoveInput) (AdminNode, error) {
	if in.RowVersion <= 0 {
		return AdminNode{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}
	reason, err := nodeValidateReason(in.Reason)
	if err != nil {
		return AdminNode{}, err
	}
	var warnings []string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		before, found, err := s.repo.NodeByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if before.RowVersion != in.RowVersion {
			return nodeVersionConflict(before.RowVersion)
		}
		if before.ServerID != nil && *before.ServerID == in.ServerID {
			return nil
		}
		if !nodeMovable(before) {
			return inventoryConflict("只有从没服务过的草稿节点能迁移；其余请复制到新服务器后再退役原节点")
		}
		srv, err := s.lockServerForNodes(ctx, in.ServerID)
		if err != nil {
			return err
		}
		port := nodeInt(before.ServerPort)
		l4 := nodeStr(before.ListenL4)
		if port > 0 {
			warnings, err = s.claimNodePort(ctx, srv, nodePortClaim{
				ServerID: in.ServerID, Exclude: &id, Port: port, L4: l4,
				Host: nodeStr(before.ServerHost), CheckReserved: true,
			})
			if err != nil {
				return err
			}
		}
		ok, err := s.repo.MoveNodeServer(ctx, id, in.RowVersion, in.ServerID)
		if err != nil {
			return nodeWriteErr(err, port, l4)
		}
		if !ok {
			return nodeVersionConflict(before.RowVersion)
		}
		s.nodeChanged(ctx, realtime.OpUpdate, id)
		var from any
		if before.ServerID != nil {
			from = before.ServerID.String()
		}
		if err := s.nodeAudit(ctx, "node.server_move", id, map[string]any{
			"from_server_id": from, "server_id": in.ServerID.String(), "reason": reason,
		}); err != nil {
			return err
		}
		s.MarkConfigChanged(ctx)
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	return s.nodeWithWarnings(ctx, id, warnings)
}

// nodeSortedIDs 校验一批节点引用（1–max 项、id 不重复、版本为正）并按 id 排序（批量操作统一锁序）。
func nodeSortedIDs(ids []uuid.UUID, versions []int64, max int) ([]uuid.UUID, bool) {
	if len(ids) == 0 || len(ids) > max {
		return nil, false
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for i, id := range ids {
		if seen[id] || versions[i] <= 0 {
			return nil, false
		}
		seen[id] = true
	}
	out := slices.Clone(ids)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	return out, true
}

// ReorderNodes 批量改排序键：先全部核对版本，再逐个写；整批原子。
func (s *Service) ReorderNodes(ctx context.Context, items []NodeOrderItem) error {
	ids := make([]uuid.UUID, len(items))
	versions := make([]int64, len(items))
	want := make(map[uuid.UUID]NodeOrderItem, len(items))
	for i, it := range items {
		ids[i], versions[i] = it.ID, it.RowVersion
		want[it.ID] = it
	}
	sorted, ok := nodeSortedIDs(ids, versions, nodeReorderMax)
	if !ok {
		return inventoryInvalid("items", "必须包含 1 到 200 个节点，ID 不重复且带 row_version")
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.NodesByIDsForUpdate(ctx, sorted)
		if err != nil {
			return err
		}
		if len(rows) != len(sorted) {
			return errs.New(errs.NotFound)
		}
		for _, n := range rows {
			if n.RowVersion != want[n.ID].RowVersion {
				return nodeVersionConflict(n.RowVersion)
			}
		}
		for _, n := range rows {
			it := want[n.ID]
			ok, err := s.repo.SetNodeSortOrder(ctx, n.ID, it.RowVersion, it.SortOrder)
			if err != nil {
				return err
			}
			if !ok {
				return nodeVersionConflict(it.RowVersion)
			}
			s.nodeChanged(ctx, realtime.OpUpdate, n.ID)
		}
		return s.audit.Write(ctx, auditsvc.Event{Action: "node.order_batch", ResourceType: "node_set",
			Data: map[string]any{"count": len(rows)}})
	})
}
