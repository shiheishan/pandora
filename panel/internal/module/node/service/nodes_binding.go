package service

// 节点绑定、改绑、解绑服务器。节点可以不绑服务器（只作为配置存着），上线前再绑定。

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// NodeBindInput 是绑定 / 改绑服务器的输入：ServerPort 为 nil 沿用节点已有的端口。
type NodeBindInput struct {
	RowVersion int64
	ServerID   uuid.UUID
	ServerPort *int
	Reason     string
}

// NodeUnbindInput 是解绑服务器的输入。
type NodeUnbindInput struct {
	RowVersion int64
	Reason     string
}

// nodeRebindActiveMsg 是在役节点改绑 / 解绑的拒绝文案。
const nodeRebindActiveMsg = "节点在役，先停用再改绑 / 解绑"

// nodeBindNeedsPortMsg 是绑定时节点还没有端口、请求里也没给的提示。
const nodeBindNeedsPortMsg = "绑定服务器前要先填端口"

// nodeMovableState 是改绑 / 解绑资格的唯一判断：草稿或已停用（在役要先停用，已退役不行）。
// BindNode、UnbindNode 与后台列表的 movable 都用它，前端只读 movable、不自己算。
func nodeMovableState(servingStatus string) bool {
	return servingStatus == NodeDraft || servingStatus == NodeDisabled
}

// nodeRebindRefusal 返回节点不能改绑 / 解绑的 409；能改时为 nil。
func nodeRebindRefusal(servingStatus string) error {
	switch {
	case servingStatus == NodeRetired:
		return inventoryConflict("节点已退役，不能改绑或解绑服务器")
	case !nodeMovableState(servingStatus):
		return inventoryConflict(nodeRebindActiveMsg)
	}
	return nil
}

// BindNode 把草稿或已停用的节点绑定（或改绑）到一台服务器：要有端口，检查服务器容量与同机端口占用。目标就是当前服务器且端口没变时不改。
func (s *Service) BindNode(ctx context.Context, id uuid.UUID, in NodeBindInput) (AdminNode, error) {
	if in.RowVersion <= 0 {
		return AdminNode{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}
	if in.ServerPort != nil && (*in.ServerPort < 1 || *in.ServerPort > 65535) {
		return AdminNode{}, inventoryInvalid("server_port", "端口必须在 1 到 65535 之间")
	}
	reason, err := nodeValidateReason(in.Reason)
	if err != nil {
		return AdminNode{}, err
	}
	var warnings []string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		// 锁序：节点行 → 服务器行
		before, err := s.nodeLockForWrite(ctx, id, in.RowVersion)
		if err != nil {
			return err
		}
		if err := nodeRebindRefusal(before.ServingStatus); err != nil {
			return err
		}
		port := nodeInt(before.ServerPort)
		if in.ServerPort != nil {
			port = *in.ServerPort
		}
		if port == 0 {
			return inventoryInvalid("server_port", nodeBindNeedsPortMsg)
		}
		same := before.ServerID != nil && *before.ServerID == in.ServerID
		if same && port == nodeInt(before.ServerPort) {
			return nil
		}
		raw := json.RawMessage(before.ProtocolConfig)
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		l4 := protocol.ListenL4(nodeStr(before.NodeType), raw)
		claim := nodePortClaim{
			ServerID: in.ServerID, Exclude: &id, Port: port, L4: l4,
			Host: nodeStr(before.ServerHost), CheckReserved: true,
		}
		if same {
			// 节点已经算在这台的容量里，只锁服务器行、不再查容量
			srv, found, err := s.lockServerRow(ctx, in.ServerID)
			if err != nil {
				return err
			}
			if !found {
				return inventoryInvalid("server_id", "服务器不存在或已删除")
			}
			if warnings, err = s.claimNodePort(ctx, srv, claim); err != nil {
				return err
			}
		} else {
			srv, err := s.lockServerForNodes(ctx, in.ServerID)
			if err != nil {
				return err
			}
			if warnings, err = s.claimNodePort(ctx, srv, claim); err != nil {
				return err
			}
		}
		serverID := in.ServerID
		ok, err := s.repo.SetNodeServer(ctx, id, in.RowVersion, &serverID, &port, &l4)
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
		if err := s.nodeAudit(ctx, "node.server_bind", id, map[string]any{
			"from_server_id": from, "server_id": in.ServerID.String(), "server_port": port, "reason": reason,
		}); err != nil {
			return err
		}
		s.MarkConfigChanged(ctx)
		s.MarkUsersChanged(ctx)
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	return s.nodeWithWarnings(ctx, id, warnings)
}

// UnbindNode 解绑草稿或已停用节点的服务器：server_id 置空即释放同机端口占用，端口与协议配置留在节点上，之后可以再绑定。本来就没绑时不改。
func (s *Service) UnbindNode(ctx context.Context, id uuid.UUID, in NodeUnbindInput) (AdminNode, error) {
	if in.RowVersion <= 0 {
		return AdminNode{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}
	reason, err := nodeValidateReason(in.Reason)
	if err != nil {
		return AdminNode{}, err
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		before, err := s.nodeLockForWrite(ctx, id, in.RowVersion)
		if err != nil {
			return err
		}
		if err := nodeRebindRefusal(before.ServingStatus); err != nil {
			return err
		}
		if before.ServerID == nil {
			return nil
		}
		ok, err := s.repo.SetNodeServer(ctx, id, in.RowVersion, nil, before.ServerPort, before.ListenL4)
		if err != nil {
			return nodeWriteErr(err, nodeInt(before.ServerPort), nodeStr(before.ListenL4))
		}
		if !ok {
			return nodeVersionConflict(before.RowVersion)
		}
		s.nodeChanged(ctx, realtime.OpUpdate, id)
		if err := s.nodeAudit(ctx, "node.server_unbind", id, map[string]any{
			"from_server_id": before.ServerID.String(), "reason": reason,
		}); err != nil {
			return err
		}
		s.MarkConfigChanged(ctx)
		s.MarkUsersChanged(ctx)
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	return s.GetAdminNode(ctx, id)
}
