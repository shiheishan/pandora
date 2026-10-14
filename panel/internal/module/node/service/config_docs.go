package service

// 节点配置文档。
//
// 文档形状：协议字段经 protocol.KernelConfig 摊平在顶层；必有 server_port、protocol（只进摘要）；
// 有生效路由时才有 outbounds、routes、final。JSON 用 json.Marshal(map) 确定性编码（键排序、无多余空白）。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var configDocReservedKeys = []string{
	"server_port", "protocol",
	"outbounds", "routes", "final",
	"ech_keys",
}

// ServerConfigs 一次算出这些服务器上每个该跑的节点（repo.ServedOnServerCond）的生效配置文档：
// 服务器 id → 文档（按节点 id 升序）。只给下发路径用（配置纪元轮询、建流对齐）：不存在或已删除的服务器不在结果里；
// 单个节点文档出错只跳过该节点并在这里记 ERROR——这份结果会下发出去，被跳过的节点端会停掉其入站。
// 只读路径（节点列表、报表）调 serverConfigsWithSkips，自己处理跳过集合。
func (s *Service) ServerConfigs(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, error) {
	out, skips, err := s.serverConfigsWithSkips(ctx, serverIDs)
	if err != nil {
		return nil, err
	}
	for nodeID, sk := range skips {
		plog.From(ctx).Error("节点配置文档生成失败，本次下发不含该节点，节点端将停掉其入站直到配置修好",
			zap.String("server_id", sk.ServerID.String()),
			zap.String("node_id", nodeID.String()),
			zap.String("err", sk.Reason))
	}
	return out, nil
}

// configSkip 是一个因文档生成失败被跳过的节点：所在服务器与错误摘要。
type configSkip struct {
	ServerID uuid.UUID
	Reason   string
}

// serverConfigsWithSkips 同 ServerConfigs，但不记日志，额外返回被跳过的节点（节点 id → 跳过原因）。
func (s *Service) serverConfigsWithSkips(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, map[uuid.UUID]configSkip, error) {
	out := make(map[uuid.UUID][]NodeConfigDoc)
	skips := make(map[uuid.UUID]configSkip)
	if len(serverIDs) == 0 {
		return out, skips, nil
	}
	ids := slices.Clone(serverIDs)
	slices.SortFunc(ids, compareUUID)
	ids = slices.Compact(ids)

	rows, err := s.repo.ConfigNodes(ctx, ids)
	if err != nil {
		return nil, nil, err
	}

	var nodeIDs []uuid.UUID
	for _, row := range rows {
		if row.NodeID != nil {
			nodeIDs = append(nodeIDs, *row.NodeID)
		}
	}

	layersByNode := map[uuid.UUID][]routingLayer{}
	if len(nodeIDs) > 0 {
		layersByNode, _, err = s.nodeRoutingLayers(ctx, nodeIDs)
		if err != nil {
			return nil, nil, err
		}
	}

	var echByNode map[uuid.UUID][]echDocKey
	var echBad map[uuid.UUID]string
	if len(nodeIDs) > 0 {
		echByNode, echBad, err = s.echDocKeys(ctx, nodeIDs)
		if err != nil {
			return nil, nil, err
		}
	} else {
		echByNode = map[uuid.UUID][]echDocKey{}
		echBad = map[uuid.UUID]string{}
	}

	out, skips = assembleServerConfigs(rows, layersByNode, echByNode, echBad)
	return out, skips, nil
}

// assembleServerConfigs 按读到的行组装每台服务器的配置文档（按节点 id 升序）。一个节点的文档生成失败只跳过这个节点，
// 记进返回的跳过集合（节点 id → 所在服务器与错误摘要），不让整批失败：建流对齐与配置纪元都是一批服务器一起算，
// 一台节点的坏配置不能让同批其他服务器都拿不到配置。
// 被跳过的节点不在这次下发的文档里，节点端会按文档集合停掉它的入站，直到配置修好；后台列表把它显示为失败。
func assembleServerConfigs(rows []repo.ConfigNodeRow, layersByNode map[uuid.UUID][]routingLayer, echByNode map[uuid.UUID][]echDocKey, echBad map[uuid.UUID]string) (map[uuid.UUID][]NodeConfigDoc, map[uuid.UUID]configSkip) {
	out := make(map[uuid.UUID][]NodeConfigDoc)
	skips := make(map[uuid.UUID]configSkip)
	for _, row := range rows {
		out[row.ServerID] = []NodeConfigDoc{}
	}

	for _, row := range rows {
		if row.NodeID == nil {
			continue
		}
		nodeType := ""
		if row.NodeType != nil {
			nodeType = *row.NodeType
		}
		port := 0
		if row.ServerPort != nil {
			port = *row.ServerPort
		}
		protoCfg := ""
		if row.ProtocolConfig != nil {
			protoCfg = *row.ProtocolConfig
		}
		if reason, skip := echBad[*row.NodeID]; skip {
			skips[*row.NodeID] = configSkip{ServerID: row.ServerID, Reason: reason}
			continue
		}
		layers := layersByNode[*row.NodeID]
		ech := echByNode[*row.NodeID]
		doc, err := buildNodeConfigDoc(*row.NodeID, nodeType, port, protoCfg, layers, ech)
		if err != nil {
			skips[*row.NodeID] = configSkip{ServerID: row.ServerID, Reason: err.Error()}
			continue
		}
		out[row.ServerID] = append(out[row.ServerID], doc)
	}

	for sid, list := range out {
		slices.SortFunc(list, func(a, b NodeConfigDoc) int { return compareUUID(a.NodeID, b.NodeID) })
		out[sid] = list
	}
	return out, skips
}

// buildNodeConfigDoc 组装一个节点的配置文档：协议字段经 protocol.KernelConfig 翻译后摊平在顶层，
// 加 server_port（入站端口），有路由时加 outbounds / routes / final；协议支持且提供了密钥时加 ech_keys。
func buildNodeConfigDoc(nodeID uuid.UUID, nodeType string, port int, protocolConfig string, layers []routingLayer, ech []echDocKey) (NodeConfigDoc, error) {
	storedText := protocolConfig
	if strings.TrimSpace(protocolConfig) == "" {
		storedText = "{}"
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(storedText), &stored); err != nil {
		return NodeConfigDoc{}, fmt.Errorf("节点 %s 的协议配置不是 JSON 对象: %w", nodeID, err)
	}
	if stored == nil {
		return NodeConfigDoc{}, fmt.Errorf("节点 %s 的协议配置不是 JSON 对象: %w", nodeID, fmt.Errorf("null"))
	}

	doc := protocol.KernelConfig(nodeType, stored)

	// 协议配置不能顶替基础字段、也不能夹带路由；shadowtls 握手端口已由 KernelConfig 折进 server，文档 server_port 永远是入站端口。
	for _, k := range configDocReservedKeys {
		delete(doc, k)
	}
	doc["server_port"] = port
	// protocol 只为让摘要区分协议：改了协议、配置原样不变时 SHA 也要变，节点端才会重装；
	// 节点端装入站以 NodeConfig.node_type 为准，不读这个键。
	doc["protocol"] = protocol.CanonicalType(nodeType)

	if len(ech) > 0 && protocol.ECHCapable(nodeType, doc) {
		echAny := make([]any, 0, len(ech))
		for _, k := range ech {
			echAny = append(echAny, map[string]any{
				"config":        base64.StdEncoding.EncodeToString(k.Config),
				"private_key":   base64.StdEncoding.EncodeToString(k.PrivateKey),
				"send_as_retry": k.SendAsRetry,
			})
		}
		doc["ech_keys"] = echAny
	}

	outs, routes := mergeRouting(layers)
	if len(outs) > 0 || len(routes) > 0 {
		obAny := make([]any, 0, len(outs))
		for _, o := range outs {
			var settings any
			if err := json.Unmarshal(o.Settings, &settings); err != nil {
				return NodeConfigDoc{}, fmt.Errorf("节点 %s 出站 %s settings 解码失败: %w", nodeID, o.Tag, err)
			}
			obAny = append(obAny, map[string]any{
				"tag": o.Tag, "type": o.Type, "settings": settings,
			})
		}
		rtAny := make([]any, 0, len(routes))
		for _, r := range routes {
			var matcher any
			if err := json.Unmarshal(r.Matcher, &matcher); err != nil {
				return NodeConfigDoc{}, fmt.Errorf("节点 %s 规则 matcher 解码失败: %w", nodeID, err)
			}
			rtAny = append(rtAny, map[string]any{
				"outbound": r.Outbound, "matcher": matcher,
			})
		}
		doc["outbounds"] = obAny
		doc["routes"] = rtAny
		// 后台没有单独的最终出站设置，没命中规则走内置直连；兜底请用空匹配规则。
		doc["final"] = "direct"
	}

	body, err := json.Marshal(doc)
	if err != nil {
		return NodeConfigDoc{}, err
	}
	sum := sha256.Sum256(body)
	return NodeConfigDoc{
		NodeID:   nodeID,
		NodeType: protocol.CanonicalType(nodeType),
		JSON:     body,
		SHA256:   hex.EncodeToString(sum[:]),
	}, nil
}
