package service

// 节点 ECH 密钥：后台查看、生成/轮换、停用与过期清理。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ECHRetiredKeyGrace 是轮换后旧 ECH 密钥继续下发给节点端的时长：拿着旧订阅（旧 ECHConfigList）的客户端在这段时间内仍能连上。
// 满期只由任务 node:ech_purge 判定：它删行并推配置纪元；删之前旧钥照常进配置文档和 RetiredKeys，读路径不按时间过滤。
const ECHRetiredKeyGrace = 7 * 24 * time.Hour

// echDocKeysMax 是一个节点的配置文档里最多下发几把 ECH 密钥（当前钥在前），与节点端 kernel 的 maxInboundECHKeys 一致。
const echDocKeysMax = 8

// NodeECH 是后台看到的节点 ECH 状态。
type NodeECH struct {
	// Supported 是按当前协议配置能不能开 ECH；不能时 UnsupportedReason 说明原因（已有密钥照样列出，但不下发）
	Supported         bool
	UnsupportedReason string
	// Enabled 是有当前密钥
	Enabled    bool
	PublicName string
	// ConfigList 是当前密钥的 ECHConfigList（Enabled 为 false 时 nil）；ConfigPEM 是 Xboard 同款 PEM 块
	ConfigList []byte
	ConfigPEM  string
	CreatedAt  *time.Time
	// RetiredKeys 是还在下发（尚未被清理任务删除）的旧钥个数
	RetiredKeys int
}

// echDocKey 是配置文档 ech_keys 的一项（已解封的私钥）。
type echDocKey struct {
	Config      []byte // 单个 ECHConfig
	PrivateKey  []byte
	SendAsRetry bool // 只有当前钥为 true
}

func echAAD(nodeID uuid.UUID) []byte { return []byte("node_ech_key:" + nodeID.String()) }

var errECHSealerMissing = errs.Newf(errs.Unavailable, "节点模块未装配 ECH 封存器")

func nodeKernelConfig(n model.Node) map[string]any {
	storedText := n.ProtocolConfig
	if strings.TrimSpace(storedText) == "" {
		storedText = "{}"
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(storedText), &stored); err != nil {
		return map[string]any{}
	}
	if stored == nil {
		return map[string]any{}
	}
	return protocol.KernelConfig(nodeStr(n.NodeType), stored)
}

// GetNodeECH 返回节点的 ECH 状态；节点不存在回 404。
func (s *Service) GetNodeECH(ctx context.Context, nodeID uuid.UUID) (NodeECH, error) {
	n, found, err := s.repo.NodeByID(ctx, nodeID, false)
	if err != nil {
		return NodeECH{}, err
	}
	if !found {
		return NodeECH{}, errs.New(errs.NotFound)
	}
	keys, err := s.repo.ECHKeysOfNode(ctx, nodeID)
	if err != nil {
		return NodeECH{}, err
	}
	out := NodeECH{
		UnsupportedReason: protocol.ECHUnsupportedReason(nodeStr(n.NodeType), nodeKernelConfig(n)),
	}
	out.Supported = out.UnsupportedReason == ""
	for _, k := range keys {
		if k.SupersededAt == nil {
			out.Enabled = true
			out.PublicName = k.PublicName
			out.ConfigList = append([]byte(nil), k.ConfigList...)
			out.ConfigPEM = protocol.ECHConfigPEM(k.ConfigList)
			t := k.CreatedAt
			out.CreatedAt = &t
		} else {
			out.RetiredKeys++
		}
	}
	return out, nil
}

// GenerateNodeECH 生成或轮换节点 ECH 密钥。
func (s *Service) GenerateNodeECH(ctx context.Context, nodeID uuid.UUID, publicName string) (NodeECH, error) {
	if s.echSealer == nil {
		return NodeECH{}, errECHSealerMissing
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		n, found, err := s.repo.NodeByID(ctx, nodeID, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if n.ServingStatus == NodeRetired {
			return inventoryConflict("节点已退役")
		}
		reason := protocol.ECHUnsupportedReason(nodeStr(n.NodeType), nodeKernelConfig(n))
		if reason != "" {
			return &errs.Error{Code: errs.Conflict, Message: reason}
		}

		existing, err := s.repo.ECHKeysOfNode(ctx, nodeID)
		if err != nil {
			return err
		}
		var currentName string
		for _, k := range existing {
			if k.SupersededAt == nil {
				currentName = k.PublicName
				break
			}
		}

		normInput := strings.TrimSpace(publicName)
		if normInput == "" {
			if currentName != "" {
				normInput = currentName
			} else {
				normInput = nodeStr(n.ServerHost)
			}
		}
		norm, ok := protocol.NormalizeECHPublicName(normInput)
		if !ok {
			return inventoryInvalid("public_name", "必须是域名（客户端握手外层明文里的 SNI，通常填一个与节点同证书的域名）")
		}

		key, err := protocol.GenerateECHKey(norm)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		sealed, err := s.echSealer.Seal(key.PrivateKey, echAAD(nodeID))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		rotated, err := s.repo.SupersedeCurrentECHKey(ctx, nodeID)
		if err != nil {
			return err
		}
		row := &model.NodeECHKey{
			NodeID:           nodeID,
			PublicName:       norm,
			ConfigList:       key.ConfigList,
			PrivateKeySealed: sealed,
		}
		if err := s.repo.InsertECHKey(ctx, row); err != nil {
			return err
		}
		if err := s.nodeAudit(ctx, "node.ech_generate", nodeID, map[string]any{
			"public_name": norm,
			"rotated":     rotated,
		}); err != nil {
			return err
		}
		s.MarkConfigChanged(ctx)
		s.MarkLinesChanged(ctx)
		return nil
	})
	if err != nil {
		return NodeECH{}, err
	}
	return s.GetNodeECH(ctx, nodeID)
}

// DisableNodeECH 立即停用节点 ECH（含尚未清理的旧钥）。
func (s *Service) DisableNodeECH(ctx context.Context, nodeID uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		_, found, err := s.repo.NodeByID(ctx, nodeID, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		n, err := s.repo.DeleteECHKeysOfNode(ctx, nodeID)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := s.nodeAudit(ctx, "node.ech_disable", nodeID, map[string]any{"deleted_keys": n}); err != nil {
			return err
		}
		s.MarkConfigChanged(ctx)
		s.MarkLinesChanged(ctx)
		return nil
	})
}

// PurgeRetiredECHKeys 删除过渡期已过的旧钥；删了至少一行时推进配置纪元。
func (s *Service) PurgeRetiredECHKeys(ctx context.Context) (int64, error) {
	n, err := s.repo.PurgeRetiredECHKeys(ctx, s.now().Add(-ECHRetiredKeyGrace))
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.MarkConfigChanged(ctx)
	}
	return n, nil
}

// echDocKeys 批量解封配置文档用的 ECH 密钥；第二个 map 是生成失败的节点与原因。
func (s *Service) echDocKeys(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID][]echDocKey, map[uuid.UUID]string, error) {
	rows, err := s.repo.ECHKeysByNodes(ctx, nodeIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return map[uuid.UUID][]echDocKey{}, map[uuid.UUID]string{}, nil
	}
	if s.echSealer == nil {
		return nil, nil, errECHSealerMissing
	}
	out := make(map[uuid.UUID][]echDocKey)
	bad := make(map[uuid.UUID]string)
	var curID uuid.UUID
	var curKeys []echDocKey
	var curBad bool

	flush := func() {
		if curID == uuid.Nil || curBad {
			return
		}
		if len(curKeys) > 0 {
			out[curID] = curKeys
		}
	}

	for _, row := range rows {
		if row.NodeID != curID {
			flush()
			curID = row.NodeID
			curKeys = nil
			curBad = false
		}
		if curBad {
			continue
		}
		plain, err := s.echSealer.Open(row.PrivateKeySealed, echAAD(row.NodeID))
		if err != nil {
			bad[row.NodeID] = "ECH 私钥解封失败"
			curBad = true
			curKeys = nil
			continue
		}
		cfg, err := protocol.ECHConfigFromList(row.ConfigList)
		if err != nil {
			bad[row.NodeID] = "ECH 配置解析失败"
			curBad = true
			curKeys = nil
			continue
		}
		if len(curKeys) >= echDocKeysMax {
			continue
		}
		curKeys = append(curKeys, echDocKey{
			Config:      cfg,
			PrivateKey:  plain,
			SendAsRetry: row.SupersededAt == nil,
		})
	}
	flush()
	return out, bad, nil
}
