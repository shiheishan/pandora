package service

// 节点的读取、新建、修改与 REALITY 密钥生成。

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
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

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
		TrafficRate:           n.TrafficRate,
		DisplayName:           n.DisplayName,
		CountryCode:           n.CountryCode,
		ProtocolConfig:        protocol.Redact(json.RawMessage(raw)),
		ProtocolSchemaVersion: n.ProtocolSchemaVersion,
		ConfigValidatedAt:     n.ConfigValidatedAt,
		SortOrder:             n.SortOrder,
		CreatedAt:             n.CreatedAt,
		UpdatedAt:             n.UpdatedAt,
		Tags:                  decodeNodeTags(n.Tags),
		Traffic:               nodeTrafficFromModel(n),
		RateSchedule:          decodeRateScheduleInputs(n.TrafficRateSchedule),
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

// CreateNode 新建一个草稿节点：协议必须配齐并通过校验；可以不绑服务器（暂不绑定），绑了服务器时要有端口，同机端口先到先得、检查服务器容量。
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
	tags, err := NormalizeNodeTags(in.Tags)
	if err != nil {
		return AdminNode{}, err
	}
	if err := nodeCheckTrafficLimit(in.TrafficLimitBytes); err != nil {
		return AdminNode{}, err
	}
	if err := nodeCheckResetDay(in.TrafficResetDay); err != nil {
		return AdminNode{}, err
	}
	sched, err := ParseRateSchedule(in.TrafficRateSchedule)
	if err != nil {
		return AdminNode{}, err
	}
	nodeType := protocol.CanonicalType(in.NodeType)
	host := strings.TrimSpace(in.ServerHost)
	raw := in.ProtocolConfig
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	version, err := nodeValidateProtocol(nodeType, host, in.ServerPort, raw, nodeEndpointNeed{Port: in.ServerID != nil})
	if err != nil {
		return AdminNode{}, err
	}
	l4 := protocol.ListenL4(nodeType, raw)

	var id uuid.UUID
	var warnings []string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if in.ServerID != nil {
			srv, err := s.lockServerForNodes(ctx, *in.ServerID)
			if err != nil {
				return err
			}
			warnings, err = s.claimNodePort(ctx, srv, nodePortClaim{
				ServerID: *in.ServerID, Port: in.ServerPort, L4: l4, Host: host, CheckReserved: true,
			})
			if err != nil {
				return err
			}
		}
		if err := s.nodeLockPool(ctx, in.PoolID); err != nil {
			return err
		}
		now := s.now()
		port := in.ServerPort
		var serverPort *int
		var listenL4 *string
		if port != 0 {
			serverPort = &port
			listenL4 = &l4
		}
		n := model.Node{
			ServerID:              in.ServerID,
			PoolID:                in.PoolID,
			Name:                  name,
			DisplayName:           display,
			CountryCode:           country,
			ServingStatus:         NodeDraft,
			NodeType:              &nodeType,
			ServerHost:            nodeOpt(host),
			ServerPort:            serverPort,
			ListenL4:              listenL4,
			ProtocolConfig:        string(raw),
			ProtocolSchemaVersion: version,
			ConfigValidatedAt:     &now,
			TrafficRate:           rate,
			SortOrder:             in.SortOrder,
			Tags:                  encodeNodeTags(tags),
			TrafficLimitBytes:     in.TrafficLimitBytes,
			TrafficResetDay:       in.TrafficResetDay,
			TrafficRateSchedule:   encodeRateSchedule(sched),
		}
		if err := s.repo.InsertNode(ctx, &n); err != nil {
			return nodeWriteErr(err, port, l4)
		}
		s.nodeChanged(ctx, realtime.OpInsert, n.ID)
		id = n.ID
		var serverID any
		if in.ServerID != nil {
			serverID = in.ServerID.String()
		}
		if err := s.nodeAudit(ctx, "node.create", id, map[string]any{
			"name": name, "server_id": serverID, "node_type": nodeType, "schema_version": version,
			"tags": tags,
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
	if in.Tags != nil {
		tags, err := NormalizeNodeTags(*in.Tags)
		if err != nil {
			return err
		}
		in.Tags = &tags
	}
	if in.TrafficLimitBytes.Set {
		if err := nodeCheckTrafficLimit(in.TrafficLimitBytes.Value); err != nil {
			return err
		}
	}
	if in.TrafficResetDay.Set {
		if err := nodeCheckResetDay(in.TrafficResetDay.Value); err != nil {
			return err
		}
	}
	if in.TrafficRateSchedule != nil {
		// 预检里解析并写回按开始时刻排好序的输入；事务里 patchNodeTx 会再 Parse 一次落库。
		sched, err := ParseRateSchedule(*in.TrafficRateSchedule)
		if err != nil {
			return err
		}
		*in.TrafficRateSchedule = rateScheduleInputs(sched)
	}
	if in.ServerPort != nil {
		if *in.ServerPort < 1 || *in.ServerPort > 65535 {
			return inventoryInvalid("server_port", "端口必须在 1 到 65535 之间")
		}
	}
	return nil
}

// patchNodeTx 在已有事务里改节点规格；调用方须先 nodePatchPrecheck 并自行 s.tx.Do 开事务。
// 超限标记按锁住的行重算：上限调高或清空即解除，调低到已用以下即标超限（与库里 CHECK 一致）。
func (s *Service) patchNodeTx(ctx context.Context, id uuid.UUID, in NodePatch) ([]string, error) {
	var warnings []string
	before, found, err := s.repo.NodeByID(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errs.New(errs.NotFound)
	}
	if before.RowVersion != in.RowVersion {
		return nil, nodeVersionConflict(before.RowVersion)
	}
	if before.ServingStatus == NodeRetired {
		return nil, inventoryConflict("节点已退役，不能修改")
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

	if in.Tags != nil {
		next.Tags = encodeNodeTags(*in.Tags)
	}
	if in.TrafficLimitBytes.Set {
		next.TrafficLimitBytes = in.TrafficLimitBytes.Value
	}
	if in.TrafficResetDay.Set {
		next.TrafficResetDay = in.TrafficResetDay.Value
		// 设或改重置日不清零已用（用户 2026-10-14 定）：本周期起点按新重置日算，traffic_reset_at 推到 max(原值, 起点)，
		// 定时任务就只在下一个重置日清零。没改（同一天）不动，免得把已到期、待任务清零的周期顶掉。
		if v := in.TrafficResetDay.Value; v != nil && (before.TrafficResetDay == nil || *before.TrafficResetDay != *v) {
			loc, err := s.siteLoc(ctx)
			if err != nil {
				return nil, err
			}
			if start := trafficPeriodStart(*v, s.now(), loc); start.After(next.TrafficResetAt) {
				next.TrafficResetAt = start
			}
		}
	}
	if in.TrafficRateSchedule != nil {
		sched, err := ParseRateSchedule(*in.TrafficRateSchedule)
		if err != nil {
			return nil, err
		}
		next.TrafficRateSchedule = encodeRateSchedule(sched)
	}

	// 池
	poolChanged := false
	if in.PoolID.Set {
		next.PoolID = in.PoolID.Value
		if next.PoolID == nil && before.ServingStatus == NodeActive {
			return nil, inventoryInvalid("pool_id", "在役节点必须属于一个节点池；不选池则不服务任何用户，先停用节点再移出")
		}
		poolChanged = !nodeSameUUID(before.PoolID, next.PoolID)
		if poolChanged {
			if err := s.nodeLockPool(ctx, next.PoolID); err != nil {
				return nil, err
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
				return nil, inventoryInvalid("protocol_config", "必须是 JSON 对象")
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
	version := before.ProtocolSchemaVersion
	touched := in.NodeType != nil || in.ServerHost != nil || in.ServerPort != nil || in.ProtocolConfig != nil
	protocolChanged := false
	if touched {
		version, err = nodeValidateProtocol(newType, host, port, raw, nodeEndpointNeed{
			Host: before.ServingStatus == NodeActive,
			Port: before.ServerID != nil,
		})
		if err != nil {
			return nil, err
		}
		protocolChanged = newType != oldType || host != nodeStr(before.ServerHost) ||
			port != nodeInt(before.ServerPort) ||
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
			return nil, err
		}
		if !found {
			srv = model.Server{ID: *before.ServerID}
		}
		warnings, err = s.claimNodePort(ctx, srv, nodePortClaim{
			ServerID: *before.ServerID, Exclude: &id, Port: port, L4: l4, Host: host, CheckReserved: portChanged,
		})
		if err != nil {
			return nil, err
		}
	}

	if touched {
		next.NodeType = nodeOpt(newType)
		next.ServerHost = nodeOpt(host)
		if port == 0 {
			next.ServerPort = nil
			next.ListenL4 = nil
		} else {
			next.ServerPort = &port
			next.ListenL4 = nodeOpt(l4)
		}
		next.ProtocolConfig = string(raw)
		next.ProtocolSchemaVersion = version
	}
	if protocolChanged {
		now := s.now()
		next.ConfigValidatedAt = &now
	}
	next.TrafficExceededAt = nodeExceededAt(next.TrafficLimitBytes, before.TrafficUsedBytes, before.TrafficExceededAt, s.now())
	exceededChanged := (before.TrafficExceededAt == nil) != (next.TrafficExceededAt == nil)

	ok, err := s.repo.UpdateNodeSpec(ctx, id, in.RowVersion, &next)
	if err != nil {
		return warnings, nodeWriteErr(err, port, l4)
	}
	if !ok {
		return warnings, nodeVersionConflict(before.RowVersion)
	}
	s.nodeChanged(ctx, realtime.OpUpdate, id)
	var poolID any
	if next.PoolID != nil {
		poolID = next.PoolID.String()
	}
	if err := s.nodeAudit(ctx, "node.update", id, map[string]any{
		"before_row_version": in.RowVersion, "name": next.Name, "pool_id": poolID,
		"schema_version": next.ProtocolSchemaVersion, "country_code": nodeStr(next.CountryCode),
		"protocol_changed":      protocolChanged,
		"tags":                  decodeNodeTags(next.Tags),
		"traffic_limit_bytes":   next.TrafficLimitBytes,
		"traffic_reset_day":     next.TrafficResetDay,
		"rate_schedule_changed": in.TrafficRateSchedule != nil,
		"traffic_exceeded":      next.TrafficExceededAt != nil,
	}); err != nil {
		return warnings, err
	}
	if protocolChanged {
		s.MarkConfigChanged(ctx)
	}
	if poolChanged {
		s.MarkUsersChanged(ctx)
	}
	if exceededChanged {
		s.MarkConfigChanged(ctx)
		s.MarkUsersChanged(ctx)
	}
	s.MarkLinesChanged(ctx)
	return warnings, nil
}

// PatchNode 改一个未退役的节点：给了的字段才改；协议配置给了就整体替换（同类型时补回被抹掉的密钥）。
// 只有协议内容真变了才推进配置纪元，换池推进名单纪元；超限状态变化时配置与名单纪元都推。
func (s *Service) PatchNode(ctx context.Context, id uuid.UUID, in NodePatch) (AdminNode, error) {
	if err := nodePatchPrecheck(&in); err != nil {
		return AdminNode{}, err
	}
	var warnings []string
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		w, err := s.patchNodeTx(ctx, id, in)
		warnings = w
		return err
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
