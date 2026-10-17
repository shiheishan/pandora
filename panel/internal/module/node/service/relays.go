package service

// 中转节点（node_relays）的业务层实现。
// 中转节点是 Xboard parent_id 的等价物：只出现在订阅里，挂在普通节点下，
// 用父节点的协议、倍率、标签、ECH、池与资格，只换自己的名称、地址、端口与可选的 SNI / Host 覆盖。
// 中转机不跑节点端，不占端口、不进配置文档与名单、没有服务状态机，只有启停开关。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// relaysPerNodeMax 是一个父节点最多挂的中转条数。
const relaysPerNodeMax = 20

// NodeRelay 是后台看到的一条中转。
type NodeRelay struct {
	ID           uuid.UUID
	ParentNodeID uuid.UUID
	Name         string
	ServerHost   string
	ServerPort   int
	SNI          *string
	HostHeader   *string
	Enabled      bool
	SortOrder    int
	RowVersion   int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NodeRelayInput 是新建中转的输入；Enabled 为 nil 取 true。
type NodeRelayInput struct {
	Name       string
	ServerHost string
	ServerPort int
	SNI        *string
	HostHeader *string
	Enabled    *bool
	SortOrder  int
}

// NodeRelayPatch 是修改中转的输入：给了的字段才改；SNI / HostHeader 的 Set 且 Value 为 nil 表示清空（沿用父节点）。
type NodeRelayPatch struct {
	RowVersion int64
	Name       *string
	ServerHost *string
	ServerPort *int
	SNI        OptionalString
	HostHeader OptionalString
	Enabled    *bool
	SortOrder  *int
}

// relayVersionConflict 是中转乐观锁冲突。
func relayVersionConflict(current int64) error {
	return &errs.Error{
		Code:    errs.Conflict,
		Message: "中转已被其他管理员修改，请刷新后重试",
		Fields:  map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
	}
}

// relayValidateHost 校验中转服务器地址：trim 后非空、<= 255、且为合法 IP 或 ASCII 主机名。
func relayValidateHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" || len(host) > nodeHostMax || !nodeValidHost(host) {
		return "", inventoryInvalid("server_host", "必须是有效的 IP 或 ASCII 主机名")
	}
	return host, nil
}

// relayValidatePort 校验中转端口：1 到 65535。
func relayValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return inventoryInvalid("server_port", "端口必须在 1 到 65535 之间")
	}
	return nil
}

// relayValidateSNI 校验 SNI 覆盖：nil 或 trim 后空串表示不覆盖（存 NULL）；否则 <= 253、nodeValidHost 且 net.ParseIP == nil。
func relayValidateSNI(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil, nil
	}
	if len(v) > 253 || !nodeValidHost(v) || net.ParseIP(v) != nil {
		return nil, inventoryInvalid("sni", "必须是域名；留空沿用父节点")
	}
	return &v, nil
}

// relayValidateHostHeader 校验 Host 覆盖：nil 或 trim 后空串表示不覆盖（存 NULL）；否则 <= 255 且 nodeValidHost。
func relayValidateHostHeader(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil, nil
	}
	if len(v) > 255 || !nodeValidHost(v) {
		return nil, inventoryInvalid("host_header", "必须是有效的主机名；留空沿用父节点")
	}
	return &v, nil
}

// relayMapDBErr 把写库错误映射到对外错误。
func relayMapDBErr(err error) error {
	if errors.Is(err, repo.ErrRelayCheck) {
		return errs.Newf(errs.ValidationFailed, "中转数据不合法")
	}
	return err
}

// relayFromModel 将 model.NodeRelay 转为对外 DTO。
func relayFromModel(m model.NodeRelay) NodeRelay {
	return NodeRelay{
		ID:           m.ID,
		ParentNodeID: m.ParentNodeID,
		Name:         m.Name,
		ServerHost:   m.ServerHost,
		ServerPort:   m.ServerPort,
		SNI:          m.SNI,
		HostHeader:   m.HostHeader,
		Enabled:      m.Enabled,
		SortOrder:    m.SortOrder,
		RowVersion:   m.RowVersion,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// NodeRelayList 是一个父节点名下的中转与后台的 SNI 提示（SNIHint 空串表示没有要提示的，见 relaySNIHint）。
type NodeRelayList struct {
	Relays  []NodeRelay
	SNIHint string
}

// ListNodeRelays 列出一个父节点名下的全部中转节点；父节点不存在时返回 404 NotFound。
func (s *Service) ListNodeRelays(ctx context.Context, parentID uuid.UUID) (NodeRelayList, error) {
	parent, found, err := s.repo.NodeByID(ctx, parentID, false)
	if err != nil {
		return NodeRelayList{}, err
	}
	if !found {
		return NodeRelayList{}, errs.New(errs.NotFound)
	}
	list, err := s.repo.RelaysByParent(ctx, parentID)
	if err != nil {
		return NodeRelayList{}, err
	}
	out := make([]NodeRelay, len(list))
	for i, m := range list {
		out[i] = relayFromModel(m)
	}
	return NodeRelayList{Relays: out, SNIHint: relaySNIHint(parent)}, nil
}

// CreateNodeRelay 在指定父节点下新建中转。
func (s *Service) CreateNodeRelay(ctx context.Context, parentID uuid.UUID, in NodeRelayInput) (NodeRelay, error) {
	name, err := nodeValidateName(in.Name)
	if err != nil {
		return NodeRelay{}, err
	}
	host, err := relayValidateHost(in.ServerHost)
	if err != nil {
		return NodeRelay{}, err
	}
	if err := relayValidatePort(in.ServerPort); err != nil {
		return NodeRelay{}, err
	}
	sni, err := relayValidateSNI(in.SNI)
	if err != nil {
		return NodeRelay{}, err
	}
	hostHeader, err := relayValidateHostHeader(in.HostHeader)
	if err != nil {
		return NodeRelay{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	var created model.NodeRelay
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		parentNode, found, err := s.repo.NodeByID(ctx, parentID, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if parentNode.ServingStatus == NodeRetired {
			return inventoryConflict("节点已退役，不能再加中转")
		}
		if err := relayCheckSNI(parentNode, sni); err != nil {
			return err
		}

		count, err := s.repo.CountRelays(ctx, parentID)
		if err != nil {
			return err
		}
		if count >= relaysPerNodeMax {
			return inventoryConflict(fmt.Sprintf("每个节点最多 %d 条中转", relaysPerNodeMax))
		}

		m := model.NodeRelay{
			ParentNodeID: parentID,
			Name:         name,
			ServerHost:   host,
			ServerPort:   in.ServerPort,
			SNI:          sni,
			HostHeader:   hostHeader,
			Enabled:      enabled,
			SortOrder:    in.SortOrder,
		}
		if err := s.repo.InsertRelay(ctx, &m); err != nil {
			return relayMapDBErr(err)
		}
		created = m

		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "node.relay_create",
			ResourceType: "node_relay",
			ResourceID:   &created.ID,
			Data: map[string]any{
				"parent_node_id": parentID,
				"name":           created.Name,
				"server_host":    created.ServerHost,
				"server_port":    created.ServerPort,
			},
		}); err != nil {
			return err
		}

		s.MarkLinesChanged(ctx)
		return nil
	})
	if err != nil {
		return NodeRelay{}, err
	}
	return relayFromModel(created), nil
}

// PatchNodeRelay 修改中转节点的属性。
func (s *Service) PatchNodeRelay(ctx context.Context, id uuid.UUID, in NodeRelayPatch) (NodeRelay, error) {
	if in.RowVersion <= 0 {
		return NodeRelay{}, inventoryInvalid("row_version", nodeRowVersionHint)
	}

	var name *string
	if in.Name != nil {
		v, err := nodeValidateName(*in.Name)
		if err != nil {
			return NodeRelay{}, err
		}
		name = &v
	}
	var host *string
	if in.ServerHost != nil {
		v, err := relayValidateHost(*in.ServerHost)
		if err != nil {
			return NodeRelay{}, err
		}
		host = &v
	}
	if in.ServerPort != nil {
		if err := relayValidatePort(*in.ServerPort); err != nil {
			return NodeRelay{}, err
		}
	}
	var sniVal *string
	var sniSet bool
	if in.SNI.Set {
		sniSet = true
		v, err := relayValidateSNI(in.SNI.Value)
		if err != nil {
			return NodeRelay{}, err
		}
		sniVal = v
	}
	var hostHeaderVal *string
	var hostHeaderSet bool
	if in.HostHeader.Set {
		hostHeaderSet = true
		v, err := relayValidateHostHeader(in.HostHeader.Value)
		if err != nil {
			return NodeRelay{}, err
		}
		hostHeaderVal = v
	}

	var updated model.NodeRelay
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		current, found, err := s.repo.RelayByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if current.RowVersion != in.RowVersion {
			return relayVersionConflict(current.RowVersion)
		}

		toUpdate := current
		if name != nil {
			toUpdate.Name = *name
		}
		if host != nil {
			toUpdate.ServerHost = *host
		}
		if in.ServerPort != nil {
			toUpdate.ServerPort = *in.ServerPort
		}
		if sniSet {
			if sniVal != nil {
				parentNode, found, err := s.repo.NodeByID(ctx, current.ParentNodeID, false)
				if err != nil {
					return err
				}
				if !found {
					return errs.New(errs.NotFound)
				}
				if err := relayCheckSNI(parentNode, sniVal); err != nil {
					return err
				}
			}
			toUpdate.SNI = sniVal
		}
		if hostHeaderSet {
			toUpdate.HostHeader = hostHeaderVal
		}
		if in.Enabled != nil {
			toUpdate.Enabled = *in.Enabled
		}
		if in.SortOrder != nil {
			toUpdate.SortOrder = *in.SortOrder
		}

		ok, err := s.repo.UpdateRelay(ctx, id, in.RowVersion, &toUpdate)
		if err != nil {
			return relayMapDBErr(err)
		}
		if !ok {
			return relayVersionConflict(current.RowVersion)
		}

		readBack, found, err := s.repo.RelayByID(ctx, id, false)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		updated = readBack

		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "node.relay_update",
			ResourceType: "node_relay",
			ResourceID:   &id,
			Data: map[string]any{
				"before_row_version": in.RowVersion,
				"name":               updated.Name,
				"server_host":        updated.ServerHost,
				"server_port":        updated.ServerPort,
				"enabled":            updated.Enabled,
			},
		}); err != nil {
			return err
		}

		s.MarkLinesChanged(ctx)
		return nil
	})
	if err != nil {
		return NodeRelay{}, err
	}
	return relayFromModel(updated), nil
}

// DeleteNodeRelay 删除一条中转节点。
func (s *Service) DeleteNodeRelay(ctx context.Context, id uuid.UUID, rowVersion int64) error {
	if rowVersion <= 0 {
		return inventoryInvalid("row_version", nodeRowVersionHint)
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		current, found, err := s.repo.RelayByID(ctx, id, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if current.RowVersion != rowVersion {
			return relayVersionConflict(current.RowVersion)
		}

		ok, err := s.repo.DeleteRelay(ctx, id, rowVersion)
		if err != nil {
			return relayMapDBErr(err)
		}
		if !ok {
			return relayVersionConflict(current.RowVersion)
		}

		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "node.relay_delete",
			ResourceType: "node_relay",
			ResourceID:   &id,
			Data: map[string]any{
				"parent_node_id": current.ParentNodeID,
				"name":           current.Name,
			},
		}); err != nil {
			return err
		}

		s.MarkLinesChanged(ctx)
		return nil
	})
}
