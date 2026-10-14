package service

// 本文件归 p5-inventory：服务器的后台编排（列表、详情、建、改、改状态、删）与给节点写用的服务器锁。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// serverNameTaken 是服务器重名的 409。
func serverNameTaken() error {
	return &errs.Error{Code: errs.Conflict, Message: "服务器名称已存在", Fields: map[string]string{"name": "服务器名称已存在"}}
}

// serverWriteErr 把 repo 写服务器的错误译成对外错误。
func serverWriteErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repo.ErrServerNameTaken):
		return serverNameTaken()
	case errors.Is(err, repo.ErrInventoryCheck):
		return inventoryConflict("服务器数据不满足约束，这次修改没有生效")
	}
	return err
}

// serverAudit 写一条服务器审计。
func (s *Service) serverAudit(ctx context.Context, action string, id uuid.UUID, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{Action: action, ResourceType: "server", ResourceID: &id, Data: data})
}

// serverMetricsByID 批量取服务器最新一个指标点（一条 SQL）。
func (s *Service) serverMetricsByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]repo.MetricRow, error) {
	out := make(map[uuid.UUID]repo.MetricRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.LatestMetrics(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ServerID] = r
	}
	return out, nil
}

// applyServerMetrics 把最新指标点填进 Server：MB、GB 按 1024 进制整除（与 buildNodeMetrics 一致）。
func applyServerMetrics(out *Server, m repo.MetricRow) {
	cpu := int(m.CPUBp)
	memU := m.MemUsedBytes >> 20
	memT := m.MemTotalBytes >> 20
	diskU := m.DiskUsedBytes >> 30
	diskT := m.DiskTotalBytes >> 30
	at := m.RecordedAt
	out.CPUBp = &cpu
	out.MemUsedMB = &memU
	out.MemTotalMB = &memT
	out.DiskUsedGB = &diskU
	out.DiskTotalGB = &diskT
	out.MetricsAt = &at
}

// ListServers 列出未删除的服务器（最多 500 台），带名下节点计数。
func (s *Service) ListServers(ctx context.Context, f ServerFilter) ([]Server, error) {
	if err := serverValidateQuery(&f); err != nil {
		return nil, err
	}
	rows, err := s.repo.ListServers(ctx, f.Status, f.Query)
	if err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	counts, err := s.repo.ServerNodeCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	metrics, err := s.serverMetricsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for _, r := range rows {
		srv := serverFromModel(r, counts[r.ID], now)
		if m, ok := metrics[r.ID]; ok {
			applyServerMetrics(&srv, m)
		}
		out = append(out, srv)
	}
	return out, nil
}

// GetServer 读一台未删除的服务器；没有回 404。
func (s *Service) GetServer(ctx context.Context, id uuid.UUID) (Server, error) {
	m, found, err := s.repo.ServerByID(ctx, id, false)
	if err != nil {
		return Server{}, err
	}
	if !found {
		return Server{}, errs.New(errs.NotFound)
	}
	counts, err := s.repo.ServerNodeCounts(ctx, []uuid.UUID{id})
	if err != nil {
		return Server{}, err
	}
	srv := serverFromModel(m, counts[id], s.now())
	metrics, err := s.serverMetricsByID(ctx, []uuid.UUID{id})
	if err != nil {
		return Server{}, err
	}
	if m, ok := metrics[id]; ok {
		applyServerMetrics(&srv, m)
	}
	return srv, nil
}

// CreateServer 新建一台草稿服务器。
func (s *Service) CreateServer(ctx context.Context, in ServerInput) (Server, error) {
	if err := serverValidateInput(&in); err != nil {
		return Server{}, err
	}
	m := model.Server{
		Name:          in.Name,
		Region:        serverOptText(in.Region),
		Hostname:      serverOptText(in.Hostname),
		PublicIPv4:    serverOptText(in.PublicIPv4),
		PublicIPv6:    serverOptText(in.PublicIPv6),
		PrivateIPv4:   serverOptText(in.PrivateIPv4),
		Architecture:  serverOptText(in.Architecture),
		OSName:        serverOptText(in.OSName),
		CapacityNodes: in.CapacityNodes,
		Notes:         serverOptText(in.Notes),
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertServer(ctx, &m); err != nil {
			return serverWriteErr(err)
		}
		s.serverChanged(ctx, realtime.OpInsert, m.ID)
		return s.serverAudit(ctx, "server.create", m.ID, map[string]any{"name": m.Name, "status": ServerDraft})
	})
	if err != nil {
		return Server{}, err
	}
	return s.GetServer(ctx, m.ID)
}

// serverLockForWrite 锁住服务器行并核对版本：不存在回 404，版本不等回 409。
func (s *Service) serverLockForWrite(ctx context.Context, id uuid.UUID, rowVersion int64) (model.Server, error) {
	m, found, err := s.repo.ServerByID(ctx, id, true)
	if err != nil {
		return model.Server{}, err
	}
	if !found {
		return model.Server{}, errs.New(errs.NotFound)
	}
	if m.RowVersion != rowVersion {
		return model.Server{}, serverVersionConflict(m.RowVersion)
	}
	return m, nil
}

// PatchServer 改服务器的资料（不改状态）；给了的字段才改，文本给空串即清空。
func (s *Service) PatchServer(ctx context.Context, id uuid.UUID, in ServerPatch) (Server, error) {
	if err := serverValidatePatch(&in); err != nil {
		return Server{}, err
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.serverLockForWrite(ctx, id, in.RowVersion)
		if err != nil {
			return err
		}
		if in.CapacityNodes != nil {
			used, err := s.repo.CountLiveNodesOnServer(ctx, id)
			if err != nil {
				return err
			}
			if *in.CapacityNodes < used {
				return &errs.Error{Code: errs.Conflict, Message: "服务器容量不能低于当前节点占用",
					Fields: map[string]string{"capacity_nodes": fmt.Sprintf("minimum=%d", used)}}
			}
			m.CapacityNodes = *in.CapacityNodes
		}
		if in.Name != nil {
			m.Name = *in.Name
		}
		for _, f := range []struct {
			in  *string
			dst **string
		}{
			{in.Region, &m.Region},
			{in.Hostname, &m.Hostname},
			{in.PublicIPv4, &m.PublicIPv4},
			{in.PublicIPv6, &m.PublicIPv6},
			{in.PrivateIPv4, &m.PrivateIPv4},
			{in.Architecture, &m.Architecture},
			{in.OSName, &m.OSName},
			{in.Notes, &m.Notes},
		} {
			if f.in != nil {
				*f.dst = serverOptText(*f.in)
			}
		}
		ok, err := s.repo.UpdateServerProfile(ctx, id, in.RowVersion, &m)
		if err != nil {
			return serverWriteErr(err)
		}
		if !ok {
			return serverVersionConflict(m.RowVersion)
		}
		s.serverChanged(ctx, realtime.OpUpdate, id)
		return s.serverAudit(ctx, "server.update", id, map[string]any{"before_row_version": in.RowVersion, "name": m.Name})
	})
	if err != nil {
		return Server{}, err
	}
	return s.GetServer(ctx, id)
}

// SetServerStatus 改服务器状态：按转换表；进 ready 要求名下有在役节点。改动影响下发，推进两个纪元。
// 退役时在同一事务里吊销身份、作废未用令牌，提交后让会话层发 Tombstone 断流。
func (s *Service) SetServerStatus(ctx context.Context, id uuid.UUID, rowVersion int64, status, reason string) (Server, error) {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	fields := map[string]string{}
	if rowVersion <= 0 {
		fields["row_version"] = serverRowVersionNeeded
	}
	if !ValidServerStatus(status) {
		fields["status"] = "不支持的服务器状态"
	}
	if strings.ContainsRune(reason, 0) {
		fields["reason"] = serverNulMessage
	} else if utf8.RuneCountInString(reason) > serverReasonMax {
		fields["reason"] = fmt.Sprintf("内容过长，最多允许 %d 个字符", serverReasonMax)
	}
	if len(fields) > 0 {
		return Server{}, errs.Invalid(fields)
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.serverLockForWrite(ctx, id, rowVersion)
		if err != nil {
			return err
		}
		if !CanServerTransition(m.Status, status) {
			return &errs.Error{Code: errs.Conflict, Message: "不允许的服务器状态转换",
				Fields: map[string]string{"status": m.Status + " -> " + status}}
		}
		if status == ServerReady {
			has, err := s.repo.ServerHasActiveNode(ctx, id)
			if err != nil {
				return err
			}
			if !has {
				return &errs.Error{Code: errs.Conflict, Message: "服务器没有在役节点，不能进入 ready；先在节点上用「上线」",
					Fields: map[string]string{"nodes": "requires_valid_active_node"}}
			}
		}
		ok, err := s.repo.SetServerStatus(ctx, id, m.Status, rowVersion, status, serverOptText(reason))
		if err != nil {
			return serverWriteErr(err)
		}
		if !ok {
			return serverVersionConflict(m.RowVersion)
		}
		s.serverChanged(ctx, realtime.OpUpdate, id)
		var revoked []uuid.UUID
		var voided int64
		if status == ServerRetired {
			revoked, voided, err = s.revokeServerIdentities(ctx, id, identityRevokeReasonRetired, TombstoneServerRetired)
			if err != nil {
				return err
			}
		}
		auditData := map[string]any{"from": m.Status, "to": status, "reason": reason}
		if status == ServerRetired {
			auditData["revoked_identity_ids"] = identityIDStrings(revoked)
			auditData["voided_tokens"] = voided
		}
		if err := s.serverAudit(ctx, "server.status_change", id, auditData); err != nil {
			return err
		}
		s.MarkUsersChanged(ctx)
		s.MarkConfigChanged(ctx)
		return nil
	})
	if err != nil {
		return Server{}, err
	}
	return s.GetServer(ctx, id)
}

// DeleteServer 软删一台草稿或已退役的服务器：名下未退役的节点一并退役，全部节点从这台服务器上摘下。
// 删除时在同一事务里吊销身份、作废未用令牌，提交后让会话层发 Tombstone 断流。
func (s *Service) DeleteServer(ctx context.Context, id uuid.UUID, rowVersion int64) error {
	if rowVersion <= 0 {
		return inventoryInvalid("row_version", serverRowVersionNeeded)
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.serverLockForWrite(ctx, id, rowVersion)
		if err != nil {
			return err
		}
		if m.Status != ServerDraft && m.Status != ServerRetired {
			return &errs.Error{Code: errs.Conflict, Message: "服务器仍在服务，请先在「状态」里退役",
				Fields: map[string]string{"status": m.Status}}
		}
		silenced, nodeIDs, err := s.repo.RetireServerNodes(ctx, id)
		if err != nil {
			return serverWriteErr(err)
		}
		ok, err := s.repo.SoftDeleteServer(ctx, id, rowVersion)
		if err != nil {
			return serverWriteErr(err)
		}
		if !ok {
			return serverVersionConflict(m.RowVersion)
		}
		s.serverChanged(ctx, realtime.OpDelete, id)
		s.nodesChanged(ctx, nodeIDs)
		revoked, voided, err := s.revokeServerIdentities(ctx, id, identityRevokeReasonDeleted, TombstoneServerDeleted)
		if err != nil {
			return err
		}
		if err := s.serverAudit(ctx, "server.delete", id, map[string]any{
			"status": m.Status, "silenced_nodes": silenced,
			"revoked_identity_ids": identityIDStrings(revoked),
			"voided_tokens":        voided,
		}); err != nil {
			return err
		}
		s.MarkUsersChanged(ctx)
		s.MarkConfigChanged(ctx)
		return nil
	})
}

// ListServerNodes 列出服务器名下未删除的节点（含已退役）；心跳取服务器的。
func (s *Service) ListServerNodes(ctx context.Context, id uuid.UUID) ([]ServerNode, error) {
	srv, found, err := s.repo.ServerByID(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errs.New(errs.NotFound)
	}
	rows, err := s.repo.ListServerNodes(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]ServerNode, 0, len(rows))
	for _, n := range rows {
		out = append(out, ServerNode{
			ID:                n.ID,
			Name:              n.Name,
			ServingStatus:     n.ServingStatus,
			NodeType:          n.NodeType,
			DisplayName:       n.DisplayName,
			ServerHost:        n.ServerHost,
			ServerPort:        n.ServerPort,
			Kernel:            n.Kernel,
			TrafficRate:       n.TrafficRate,
			ConfigValidatedAt: n.ConfigValidatedAt,
			LastHeartbeatAt:   srv.LastHeartbeatAt,
			CreatedAt:         n.CreatedAt,
		})
	}
	return out, nil
}

// lockServerForNodes 锁服务器行（FOR UPDATE）并检查能不能再放一个节点：不存在 422、已退役 409、容量满 409。
func (s *Service) lockServerForNodes(ctx context.Context, id uuid.UUID) (model.Server, error) {
	m, found, err := s.repo.ServerByID(ctx, id, true)
	if err != nil {
		return model.Server{}, err
	}
	if !found {
		return model.Server{}, inventoryInvalid("server_id", "服务器不存在或已删除")
	}
	if m.Status == ServerRetired {
		return model.Server{}, inventoryConflict("目标服务器已退役，不接受节点")
	}
	used, err := s.repo.CountLiveNodesOnServer(ctx, id)
	if err != nil {
		return model.Server{}, err
	}
	if used >= m.CapacityNodes {
		return model.Server{}, &errs.Error{Code: errs.Conflict, Message: "目标服务器容量已满",
			Fields: map[string]string{"capacity_nodes": fmt.Sprintf("%d/%d", used, m.CapacityNodes)}}
	}
	return m, nil
}

// lockServerRow 只锁服务器行（改端口时用，不查容量）；服务器已删除返回 found=false。
func (s *Service) lockServerRow(ctx context.Context, id uuid.UUID) (model.Server, bool, error) {
	return s.repo.ServerByID(ctx, id, true)
}
