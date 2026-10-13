package service

// 节点配置文档（p5-routing 实现）。
//
// 文档形状：协议字段经 protocol.KernelConfig 摊平在顶层；必有 server_port、kernel、protocol（只进摘要）；
// 有生效路由时才有 outbounds、routes、final。JSON 用 json.Marshal(map) 确定性编码（键排序、无多余空白）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/google/uuid"
)

var configDocReservedKeys = []string{
	"server_port", "kernel", "kernel_type", "protocol",
	"outbounds", "routes", "final",
}

// ServerConfigs 一次算出这些服务器上每个该跑的节点（repo.ServedOnServerCond）的生效配置文档：
// 服务器 id → 文档（按节点 id 升序）。会话层在配置纪元变了、节点新建流时调。不存在或已删除的服务器不在结果里。
func (s *Service) ServerConfigs(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, error) {
	out := make(map[uuid.UUID][]NodeConfigDoc)
	if len(serverIDs) == 0 {
		return out, nil
	}
	ids := slices.Clone(serverIDs)
	slices.SortFunc(ids, compareUUID)
	ids = slices.Compact(ids)

	rows, err := s.repo.ConfigNodes(ctx, ids)
	if err != nil {
		return nil, err
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
			return nil, err
		}
	}

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
		kernel := ""
		if row.Kernel != nil {
			kernel = *row.Kernel
		}
		protoCfg := ""
		if row.ProtocolConfig != nil {
			protoCfg = *row.ProtocolConfig
		}
		layers := layersByNode[*row.NodeID]
		doc, err := buildNodeConfigDoc(*row.NodeID, nodeType, port, kernel, protoCfg, layers)
		if err != nil {
			return nil, err
		}
		out[row.ServerID] = append(out[row.ServerID], doc)
	}

	for sid, list := range out {
		slices.SortFunc(list, func(a, b NodeConfigDoc) int { return compareUUID(a.NodeID, b.NodeID) })
		out[sid] = list
	}
	return out, nil
}

// buildNodeConfigDoc 组装一个节点的配置文档：协议字段经 protocol.KernelConfig 翻译后摊平在顶层，
// 加 server_port（入站端口）、kernel，有路由时加 outbounds / routes / final。
func buildNodeConfigDoc(nodeID uuid.UUID, nodeType string, port int, kernel string, protocolConfig string, layers []routingLayer) (NodeConfigDoc, error) {
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
	doc["kernel"] = protocol.EffectiveKernel(kernel)
	// protocol 只为让摘要区分协议：改了协议、配置原样不变时 SHA 也要变，节点端才会重装；
	// 节点端装入站以 NodeConfig.node_type 为准，不读这个键。
	doc["protocol"] = protocol.CanonicalType(nodeType)

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
