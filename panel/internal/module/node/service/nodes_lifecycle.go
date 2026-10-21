package service

// 节点的批量改状态、上线、退役与删除。

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// nodeStatusBatchMax 是一次批量改状态最多的节点数。
const nodeStatusBatchMax = 100

// nodeNoPoolWarning、nodePoolUnboundWarning 是上线成功但不服务任何人的提示（前端 activationHint 按原文配下一步）。
const (
	nodeNoPoolWarning      = "未划入节点池，不服务任何用户"
	nodePoolUnboundWarning = "所在节点池没有绑定任何套餐，暂时不服务任何用户"
)

// serverNoActiveNodeMsg 是服务器没有在役节点却要进 ready 时的提示：服务器随节点「上线」自动投入服务。
const serverNoActiveNodeMsg = "服务器还没有在役节点，不能投入服务：先新建节点或把节点绑到这台，再在节点上用「上线」（上线时这台会自动投入服务）"

// serverNotEnrolledMsg 是上线时服务器还没有有效接入身份的提示。
const serverNotEnrolledMsg = "服务器还没接入（先在服务器页生成安装命令完成接入）"

// nodeUnboundActivateMsg 是没绑服务器的节点上线 / 启用时的提示。
const nodeUnboundActivateMsg = "节点还没有绑定服务器，先在节点详情「身份与操作」里绑定服务器再上线"

// BatchNodeStatus 批量改服务状态：先全部核对、再写；整批有一条不合法就整批失败。转 active 时要求所在服务器有有效接入身份。
func (s *Service) BatchNodeStatus(ctx context.Context, in NodeStatusBatch) error {
	ids := make([]uuid.UUID, len(in.Items))
	versions := make([]int64, len(in.Items))
	want := make(map[uuid.UUID]int64, len(in.Items))
	for i, it := range in.Items {
		ids[i], versions[i] = it.ID, it.RowVersion
		want[it.ID] = it.RowVersion
	}
	sorted, ok := nodeSortedIDs(ids, versions, nodeStatusBatchMax)
	if !ok {
		return inventoryInvalid("items", "必须包含 1 到 100 个节点，ID 不重复且带 row_version")
	}
	to := in.ServingStatus
	if !ValidNodeServingStatus(to) {
		return inventoryInvalid("serving_status", "不支持的状态")
	}
	reason, err := nodeValidateReason(in.Reason)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.NodesByIDsForUpdate(ctx, sorted)
		if err != nil {
			return err
		}
		if len(rows) != len(sorted) {
			return errs.New(errs.NotFound)
		}
		serverReady := map[uuid.UUID]bool{}
		enrolled := map[uuid.UUID]bool{}
		for _, n := range rows {
			if n.RowVersion != want[n.ID] {
				return nodeVersionConflict(n.RowVersion)
			}
			if !CanNodeTransition(n.ServingStatus, to) {
				return &errs.Error{Code: errs.Conflict, Message: "不允许的节点服务状态转换",
					Fields: map[string]string{"serving_status": n.ServingStatus + " -> " + to, "id": n.ID.String()}}
			}
			if to != NodeActive {
				continue
			}
			if n.ServerID == nil {
				return &errs.Error{Code: errs.Conflict, Message: nodeUnboundActivateMsg,
					Fields: map[string]string{"server_id": nodeUnboundActivateMsg, "id": n.ID.String()}}
			}
			ready := false
			if nodeProtocolReady(n) && n.ServerID != nil {
				r, seen := serverReady[*n.ServerID]
				if !seen {
					srv, found, err := s.repo.ServerByID(ctx, *n.ServerID, false)
					if err != nil {
						return err
					}
					r = found && srv.Status == ServerReady
					serverReady[*n.ServerID] = r
				}
				ready = r
			}
			if !ready {
				return &errs.Error{Code: errs.Conflict,
					Message: "节点协议未配好或所在服务器未就绪，不能启用；新服务器上的节点请用「上线」",
					Fields:  map[string]string{"id": n.ID.String()}}
			}
			if n.ServerID != nil {
				e, seen := enrolled[*n.ServerID]
				if !seen {
					e, err = s.repo.HasActiveIdentity(ctx, *n.ServerID, s.identityNow())
					if err != nil {
						return err
					}
					enrolled[*n.ServerID] = e
				}
				if !e {
					return &errs.Error{Code: errs.Conflict, Message: serverNotEnrolledMsg,
						Fields: map[string]string{"server_id": serverNotEnrolledMsg, "id": n.ID.String()}}
				}
			}
		}
		for _, n := range rows {
			ok, err := s.repo.SetNodeServing(ctx, n.ID, n.ServingStatus, n.RowVersion, to)
			if err != nil {
				return nodeWriteErr(err, nodeInt(n.ServerPort), nodeStr(n.ListenL4))
			}
			if !ok {
				return nodeVersionConflict(n.RowVersion)
			}
			s.nodeChanged(ctx, realtime.OpUpdate, n.ID)
		}
		if err := s.audit.Write(ctx, auditsvc.Event{Action: "node.status_batch", ResourceType: "node_set",
			Data: map[string]any{"count": len(rows), "serving_status": to, "reason": reason}}); err != nil {
			return err
		}
		s.MarkUsersChanged(ctx)
		s.MarkConfigChanged(ctx)
		return nil
	})
}

// nodeLockForWrite 锁住节点行并核对版本：不存在回 404，版本不等回 409。
func (s *Service) nodeLockForWrite(ctx context.Context, id uuid.UUID, rowVersion int64) (model.Node, error) {
	n, found, err := s.repo.NodeByID(ctx, id, true)
	if err != nil {
		return model.Node{}, err
	}
	if !found {
		return model.Node{}, errs.New(errs.NotFound)
	}
	if n.RowVersion != rowVersion {
		return model.Node{}, nodeVersionConflict(n.RowVersion)
	}
	return n, nil
}

// ActivateNode 上线一个草稿或停用的节点；所在服务器未就绪时一并转 ready。已在役则什么都不改（幂等）。要求所在服务器有有效接入身份。没绑服务器的回 409 server_id。
func (s *Service) ActivateNode(ctx context.Context, id uuid.UUID, rowVersion int64) (AdminNode, error) {
	if rowVersion <= 0 {
		return AdminNode{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		n, found, err := s.repo.NodeByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if n.ServingStatus == NodeActive {
			return nil
		}
		if n.RowVersion != rowVersion {
			return nodeVersionConflict(n.RowVersion)
		}
		switch {
		case n.ServingStatus == NodeRetired:
			return inventoryConflict("节点已退役，不能上线")
		case !CanNodeTransition(n.ServingStatus, NodeActive):
			return inventoryConflict("节点处于「" + statusLabel(nodeStatusLabels, n.ServingStatus) + "」，不能上线")
		case n.ServerID == nil:
			return &errs.Error{Code: errs.Conflict, Message: nodeUnboundActivateMsg,
				Fields: map[string]string{"server_id": nodeUnboundActivateMsg}}
		case !nodeProtocolReady(n):
			return inventoryConflict("节点的协议配置还没就绪（协议类型、对外地址、端口），补齐后再上线")
		}
		// 锁序：节点行 → 服务器行
		srv, found, err := s.lockServerRow(ctx, *n.ServerID)
		if err != nil {
			return err
		}
		if !found {
			return inventoryConflict("节点所在的服务器已删除，不能上线")
		}
		enrolled, err := s.repo.HasActiveIdentity(ctx, srv.ID, s.identityNow())
		if err != nil {
			return err
		}
		if !enrolled {
			return &errs.Error{Code: errs.Conflict, Message: serverNotEnrolledMsg,
				Fields: map[string]string{"server_id": serverNotEnrolledMsg}}
		}
		readied := srv.Status != ServerReady
		if readied && !CanServerTransition(srv.Status, ServerReady) {
			return inventoryConflict("节点所在的服务器处于「" + statusLabel(serverStatusLabels, srv.Status) + "」，不能投入服务：把节点换绑到其他服务器后再上线")
		}
		ok, err := s.repo.SetNodeServing(ctx, id, n.ServingStatus, rowVersion, NodeActive)
		if err != nil {
			return nodeWriteErr(err, nodeInt(n.ServerPort), nodeStr(n.ListenL4))
		}
		if !ok {
			return nodeVersionConflict(n.RowVersion)
		}
		s.nodeChanged(ctx, realtime.OpUpdate, id)
		if readied {
			ok, err := s.repo.SetServerStatus(ctx, srv.ID, srv.Status, srv.RowVersion, ServerReady, nil)
			if err != nil {
				return serverWriteErr(err)
			}
			if !ok {
				return serverVersionConflict(srv.RowVersion)
			}
			s.serverChanged(ctx, realtime.OpUpdate, srv.ID)
		}
		if err := s.nodeAudit(ctx, "node.activate", id, map[string]any{
			"serving_status_before": n.ServingStatus, "server_id": srv.ID.String(),
			"server_status_before": srv.Status, "server_readied": readied,
		}); err != nil {
			return err
		}
		s.MarkUsersChanged(ctx)
		s.MarkConfigChanged(ctx)
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	out, err := s.GetAdminNode(ctx, id)
	if err != nil {
		return AdminNode{}, err
	}
	out.Warnings = s.activationWarnings(ctx, out.PoolID)
	return out, nil
}

// activationWarnings 是上线成功后「不服务任何人」的提示：没划池，或所在池没被在用套餐绑定（与列表下发说明同一口径）。
// 上线已经提交，读池套餐失败只记警告、不提示，不把成功的上线报成失败；没装配 catalog 时不查池套餐。
func (s *Service) activationWarnings(ctx context.Context, poolID *uuid.UUID) []string {
	if poolID == nil {
		return []string{nodeNoPoolWarning}
	}
	if s.catalog == nil || s.inUse == nil {
		return nil
	}
	plans, err := s.poolLivePlans(ctx, []uuid.UUID{*poolID})
	if err != nil {
		plog.From(ctx).Warn("上线后读节点池套餐失败，不出提示", zap.Error(err))
		return nil
	}
	if len(plans[*poolID]) == 0 {
		return []string{nodePoolUnboundWarning}
	}
	return nil
}

// RetireNode 退役一个节点（不可逆）：同机端口随之释放。
func (s *Service) RetireNode(ctx context.Context, id uuid.UUID, rowVersion int64, reason string) (AdminNode, error) {
	reason, err := nodeValidateReason(reason)
	if err != nil {
		return AdminNode{}, err
	}
	if rowVersion <= 0 {
		return AdminNode{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		n, err := s.nodeLockForWrite(ctx, id, rowVersion)
		if err != nil {
			return err
		}
		if n.ServingStatus == NodeRetired {
			return inventoryConflict("节点已经退役")
		}
		if !CanNodeTransition(n.ServingStatus, NodeRetired) {
			return inventoryConflict("节点处于「" + statusLabel(nodeStatusLabels, n.ServingStatus) + "」，不能退役")
		}
		ok, err := s.repo.SetNodeServing(ctx, id, n.ServingStatus, rowVersion, NodeRetired)
		if err != nil {
			return nodeWriteErr(err, nodeInt(n.ServerPort), nodeStr(n.ListenL4))
		}
		if !ok {
			return nodeVersionConflict(n.RowVersion)
		}
		s.nodeChanged(ctx, realtime.OpUpdate, id)
		if err := s.nodeAudit(ctx, "node.retire", id, map[string]any{
			"serving_status_before": n.ServingStatus, "reason": reason,
		}); err != nil {
			return err
		}
		s.MarkUsersChanged(ctx)
		s.MarkConfigChanged(ctx)
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	return s.GetAdminNode(ctx, id)
}

// DeleteNode 删除草稿或已退役的节点。是软删：流量与指标按节点留档；库里 nodes_deleted_shape
// 要求删掉的必须是 retired，所以草稿先在同一事务里转 retired。在役、停用的要先退役。
func (s *Service) DeleteNode(ctx context.Context, id uuid.UUID, rowVersion int64, reason string) error {
	if rowVersion <= 0 {
		return inventoryInvalid("row_version", nodeRowVersionHint)
	}
	reason, err := nodeValidateReason(reason)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		n, err := s.nodeLockForWrite(ctx, id, rowVersion)
		if err != nil {
			return err
		}
		version := rowVersion
		switch n.ServingStatus {
		case NodeDraft:
			ok, err := s.repo.SetNodeServing(ctx, id, NodeDraft, rowVersion, NodeRetired)
			if err != nil {
				return nodeWriteErr(err, nodeInt(n.ServerPort), nodeStr(n.ListenL4))
			}
			if !ok {
				return nodeVersionConflict(n.RowVersion)
			}
			version++
			s.MarkConfigChanged(ctx)
		case NodeRetired:
		default:
			return errs.Newf(errs.ValidationFailed, "节点还在服务或已停用，先退役再删除")
		}
		ok, err := s.repo.SoftDeleteNode(ctx, id, version)
		if err != nil {
			return nodeWriteErr(err, nodeInt(n.ServerPort), nodeStr(n.ListenL4))
		}
		if !ok {
			return nodeVersionConflict(n.RowVersion)
		}
		s.nodeChanged(ctx, realtime.OpDelete, id)
		return s.nodeAudit(ctx, "node.delete", id, map[string]any{
			"name": n.Name, "serving_status_before": n.ServingStatus, "reason": reason,
		})
	})
}
