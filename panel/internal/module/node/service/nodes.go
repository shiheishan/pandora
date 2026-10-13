package service

// 本文件归 p5-inventory：节点的读取、新建、修改与 REALITY 密钥生成。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// nodeKernelAuto 是内核的缺省值。
const nodeKernelAuto = "auto"

// adminNodeFromModel 把一行节点转成写接口回的 AdminNode（协议配置抹掉敏感值，不带 Warnings）。
func adminNodeFromModel(n model.Node) AdminNode {
	raw := n.ProtocolConfig
	if raw == "" {
		raw = "{}"
	}
	return AdminNode{
		ID:                    n.ID,
		NodeNo:                n.NodeNo,
		RowVersion:            n.RowVersion,
		Name:                  n.Name,
		ServerID:              n.ServerID,
		PoolID:                n.PoolID,
		ServingStatus:         n.ServingStatus,
		NodeType:              n.NodeType,
		ServerHost:            n.ServerHost,
		ServerPort:            n.ServerPort,
		Kernel:                n.Kernel,
		TrafficRate:           n.TrafficRate,
		DisplayName:           n.DisplayName,
		CountryCode:           n.CountryCode,
		ProtocolConfig:        protocol.Redact(json.RawMessage(raw)),
		ProtocolSchemaVersion: n.ProtocolSchemaVersion,
		ConfigValidatedAt:     n.ConfigValidatedAt,
		SortOrder:             n.SortOrder,
		CreatedAt:             n.CreatedAt,
		UpdatedAt:             n.UpdatedAt,
	}
}

// GetAdminNode 读一个未删除的节点（带存量协议的提示）；没有回 404。
func (s *Service) GetAdminNode(ctx context.Context, id uuid.UUID) (AdminNode, error) {
	n, found, err := s.repo.NodeByID(ctx, id, false)
	if err != nil {
		return AdminNode{}, err
	}
	if !found {
		return AdminNode{}, errs.New(errs.NotFound)
	}
	out := adminNodeFromModel(n)
	if n.NodeType != nil && n.ProtocolConfig != "" {
		if w := protocol.Warnings(*n.NodeType, json.RawMessage(n.ProtocolConfig)); len(w) > 0 {
			out.Warnings = w
		}
	}
	return out, nil
}

// nodeWithWarnings 读回节点并把这次写入产生的提示追加在后面。
func (s *Service) nodeWithWarnings(ctx context.Context, id uuid.UUID, warnings []string) (AdminNode, error) {
	out, err := s.GetAdminNode(ctx, id)
	if err != nil {
		return AdminNode{}, err
	}
	if len(warnings) > 0 {
		out.Warnings = append(out.Warnings, warnings...)
	}
	return out, nil
}

// nodeAudit 写一条节点审计。
func (s *Service) nodeAudit(ctx context.Context, action string, id uuid.UUID, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{Action: action, ResourceType: "node", ResourceID: &id, Data: data})
}

// nodeKernel 规整内核取值：空取 auto。
func nodeKernel(raw string) string {
	k := strings.TrimSpace(raw)
	if k == "" {
		return nodeKernelAuto
	}
	return k
}

// CreateNode 新建一个草稿节点：协议必须配齐并通过校验，同机端口先到先得。
func (s *Service) CreateNode(ctx context.Context, in NodeInput) (AdminNode, error) {
	name, err := nodeValidateName(in.Name)
	if err != nil {
		return AdminNode{}, err
	}
	display, err := nodeValidateDisplay(in.DisplayName)
	if err != nil {
		return AdminNode{}, err
	}
	country, err := nodeNormalizeCountry(in.CountryCode)
	if err != nil {
		return AdminNode{}, err
	}
	rate := decimal.NewFromInt(1)
	if in.TrafficRate != nil {
		if err := nodeCheckRate(*in.TrafficRate); err != nil {
			return AdminNode{}, err
		}
		rate = *in.TrafficRate
	}
	kernel := nodeKernel(in.Kernel)
	nodeType := protocol.CanonicalType(in.NodeType)
	host := strings.TrimSpace(in.ServerHost)
	raw := in.ProtocolConfig
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	version, err := nodeValidateProtocol(nodeType, kernel, host, in.ServerPort, raw)
	if err != nil {
		return AdminNode{}, err
	}
	l4 := protocol.ListenL4(nodeType, raw)

	var id uuid.UUID
	var warnings []string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		srv, err := s.lockServerForNodes(ctx, in.ServerID)
		if err != nil {
			return err
		}
		warnings, err = s.claimNodePort(ctx, srv, nodePortClaim{
			ServerID: in.ServerID, Port: in.ServerPort, L4: l4, Host: host, CheckReserved: true,
		})
		if err != nil {
			return err
		}
		if err := s.nodeLockPool(ctx, in.PoolID); err != nil {
			return err
		}
		now := s.now()
		serverID, port := in.ServerID, in.ServerPort
		n := model.Node{
			ServerID:              &serverID,
			PoolID:                in.PoolID,
			Name:                  name,
			DisplayName:           display,
			CountryCode:           country,
			ServingStatus:         NodeDraft,
			NodeType:              &nodeType,
			ServerHost:            &host,
			ServerPort:            &port,
			ListenL4:              &l4,
			Kernel:                kernel,
			ProtocolConfig:        string(raw),
			ProtocolSchemaVersion: version,
			ConfigValidatedAt:     &now,
			TrafficRate:           rate,
			SortOrder:             in.SortOrder,
		}
		if err := s.repo.InsertNode(ctx, &n); err != nil {
			return nodeWriteErr(err, port, l4)
		}
		id = n.ID
		if err := s.nodeAudit(ctx, "node.create", id, map[string]any{
			"name": name, "server_id": serverID.String(), "node_type": nodeType, "schema_version": version,
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

// nodePatchPrecheck 在开事务前校验 PATCH 里给了的简单字段。
func nodePatchPrecheck(in *NodePatch) error {
	if in.RowVersion <= 0 {
		return inventoryInvalid("row_version", nodeRowVersionHint)
	}
	if in.Name != nil {
		name, err := nodeValidateName(*in.Name)
		if err != nil {
			return err
		}
		in.Name = &name
	}
	if in.DisplayName != nil {
		if _, err := nodeValidateDisplay(*in.DisplayName); err != nil {
			return err
		}
	}
	if in.CountryCode.Set && in.CountryCode.Value != nil {
		if _, err := nodeNormalizeCountry(*in.CountryCode.Value); err != nil {
			return err
		}
	}
	if in.TrafficRate != nil {
		if err := nodeCheckRate(*in.TrafficRate); err != nil {
			return err
		}
	}
	return nil
}

// PatchNode 改一个未退役的节点：给了的字段才改；协议配置给了就整体替换（同类型时补回被抹掉的密钥）。
// 只有协议内容真变了才推进配置纪元，换池推进名单纪元。
func (s *Service) PatchNode(ctx context.Context, id uuid.UUID, in NodePatch) (AdminNode, error) {
	if err := nodePatchPrecheck(&in); err != nil {
		return AdminNode{}, err
	}
	var warnings []string
	err := s.tx.Do(ctx, func(ctx context.Context) error {
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
		if before.ServingStatus == NodeRetired {
			return inventoryConflict("节点已退役，不能修改")
		}

		next := before
		if in.Name != nil {
			next.Name = *in.Name
		}
		if in.DisplayName != nil {
			next.DisplayName, _ = nodeValidateDisplay(*in.DisplayName)
		}
		if in.CountryCode.Set {
			next.CountryCode = nil
			if in.CountryCode.Value != nil {
				next.CountryCode, _ = nodeNormalizeCountry(*in.CountryCode.Value)
			}
		}
		if in.TrafficRate != nil {
			next.TrafficRate = *in.TrafficRate
		}

		// 池
		poolChanged := false
		if in.PoolID.Set {
			next.PoolID = in.PoolID.Value
			if next.PoolID == nil && before.ServingStatus == NodeActive {
				return inventoryInvalid("pool_id", "在役节点必须属于一个节点池；不选池则不服务任何用户，先停用节点再移出")
			}
			poolChanged = !nodeSameUUID(before.PoolID, next.PoolID)
			if poolChanged {
				if err := s.nodeLockPool(ctx, next.PoolID); err != nil {
					return err
				}
			}
		}

		// 协议
		stored := json.RawMessage(before.ProtocolConfig)
		if len(stored) == 0 {
			stored = json.RawMessage("{}")
		}
		oldType := nodeStr(before.NodeType)
		newType := oldType
		if in.NodeType != nil {
			newType = protocol.CanonicalType(*in.NodeType)
		}
		raw := stored
		if in.ProtocolConfig != nil {
			if newType == oldType {
				merged, err := protocol.PreserveSecrets(stored, in.ProtocolConfig)
				if err != nil {
					return inventoryInvalid("protocol_config", "必须是 JSON 对象")
				}
				raw = merged
			} else {
				raw = in.ProtocolConfig // 换协议不补旧密钥
			}
		}
		host := nodeStr(before.ServerHost)
		if in.ServerHost != nil {
			host = strings.TrimSpace(*in.ServerHost)
		}
		port := nodeInt(before.ServerPort)
		if in.ServerPort != nil {
			port = *in.ServerPort
		}
		kernel := before.Kernel
		if in.Kernel != nil {
			kernel = nodeKernel(*in.Kernel)
		}
		version := before.ProtocolSchemaVersion
		touched := in.NodeType != nil || in.ServerHost != nil || in.ServerPort != nil || in.Kernel != nil || in.ProtocolConfig != nil
		protocolChanged := false
		if touched {
			version, err = nodeValidateProtocol(newType, kernel, host, port, raw)
			if err != nil {
				return err
			}
			protocolChanged = newType != oldType || host != nodeStr(before.ServerHost) ||
				port != nodeInt(before.ServerPort) || kernel != before.Kernel ||
				version != before.ProtocolSchemaVersion || !nodeSameJSON(raw, stored)
		}

		// 同机端口门禁：锁序 节点行 → 服务器行
		l4 := nodeStr(before.ListenL4)
		if touched {
			l4 = protocol.ListenL4(newType, raw)
		}
		portChanged := port != nodeInt(before.ServerPort)
		if before.ServerID != nil && port > 0 && (portChanged || l4 != nodeStr(before.ListenL4)) {
			srv, found, err := s.lockServerRow(ctx, *before.ServerID)
			if err != nil {
				return err
			}
			if !found {
				srv = model.Server{ID: *before.ServerID}
			}
			warnings, err = s.claimNodePort(ctx, srv, nodePortClaim{
				ServerID: *before.ServerID, Exclude: &id, Port: port, L4: l4, Host: host, CheckReserved: portChanged,
			})
			if err != nil {
				return err
			}
		}

		if touched {
			next.NodeType = nodeOpt(newType)
			next.ServerHost = nodeOpt(host)
			next.ServerPort = &port
			next.ListenL4 = nodeOpt(l4)
			next.Kernel = kernel
			next.ProtocolConfig = string(raw)
			next.ProtocolSchemaVersion = version
		}
		if protocolChanged {
			now := s.now()
			next.ConfigValidatedAt = &now
		}
		ok, err := s.repo.UpdateNodeSpec(ctx, id, in.RowVersion, &next)
		if err != nil {
			return nodeWriteErr(err, port, l4)
		}
		if !ok {
			return nodeVersionConflict(before.RowVersion)
		}
		var poolID any
		if next.PoolID != nil {
			poolID = next.PoolID.String()
		}
		if err := s.nodeAudit(ctx, "node.update", id, map[string]any{
			"before_row_version": in.RowVersion, "name": next.Name, "pool_id": poolID,
			"schema_version": next.ProtocolSchemaVersion, "country_code": nodeStr(next.CountryCode),
			"protocol_changed": protocolChanged,
		}); err != nil {
			return err
		}
		if protocolChanged {
			s.MarkConfigChanged(ctx)
		}
		if poolChanged {
			s.MarkUsersChanged(ctx)
		}
		return nil
	})
	if err != nil {
		return AdminNode{}, err
	}
	return s.nodeWithWarnings(ctx, id, warnings)
}

// NewRealityKeypair 生成一对 REALITY 密钥与一个 8 位十六进制的 short_id（私钥只在这一次返回，不落库）。
func (s *Service) NewRealityKeypair() (RealityKeypair, error) {
	priv, pub, err := protocol.GenerateRealityKeypair()
	if err != nil {
		return RealityKeypair{}, errs.Wrap(errs.Internal, err)
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return RealityKeypair{}, errs.Wrap(errs.Internal, err)
	}
	return RealityKeypair{PrivateKey: priv, PublicKey: pub, ShortID: hex.EncodeToString(b[:])}, nil
}
