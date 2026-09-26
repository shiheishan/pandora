// [INPUT]: 依赖 uniproxy.go 的 ServingNode，依赖 platform/db 读 node_outbounds / node_routes
// [OUTPUT]: 对外提供 NodeConfigResponse、NodeOutbound、NodeRoute、Service 的 LoadRouting、BuildNodeConfig、ValidateRoutingMatcher
// [POS]: domain/nodefabric 的 UniProxy 配置组装（GET /api/v1/server/UniProxy/config）：从 uniproxy.go 拆出。LoadRouting 与 effective_release_service 的 loadEffectiveRoutingTx 同一口径（节点私有规则在前、全局规则在后）；BuildNodeConfig 产出配置字节与 ETag，路由匹配条件翻成节点端 qnode 形状
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

//------------------------------------------------------------------------------
// GET /api/v1/server/UniProxy/config
//------------------------------------------------------------------------------

type baseConfig struct {
	PushInterval int `json:"push_interval"`
	PullInterval int `json:"pull_interval"`
}

// NodeConfigResponse 是回给节点端的配置。
//
// 字段名必须与 UniProxy 一致，且这里刻意用结构体而非 map：
// map 的 JSON 序列化顺序虽然在 Go 里按键排序是确定的，但一旦有人改成
// map[string]any 拼接就会失去这个保证，而 ETag 依赖字节级稳定。
type NodeConfigResponse struct {
	ServerPort int             `json:"server_port"`
	Host       string          `json:"host,omitempty"`
	ServerName string          `json:"server_name,omitempty"`
	BaseConfig baseConfig      `json:"base_config"`
	Protocol   json.RawMessage `json:"-"`
}

// BuildNodeConfig 组装节点配置并算出 ETag。
//
// 返回的 JSON 是「协议配置」与「通用字段」的合并：protocol_config 里存的是
// 该协议特有的键（tls、network、cipher 等），由管理端按节点类型录入。
// 平台不解释这些键的含义 —— 那是节点端与内核的事，平台只负责原样传递。
// 这样新增一种协议不需要改平台代码（EXT-008 扩展性）。
// NodeOutbound 是一条内核中立的出站描述。
type NodeOutbound struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

// NodeRoute 是一条分流规则。Matcher 的键与节点端约定，平台不解释。
type NodeRoute struct {
	Matcher     json.RawMessage `json:"matcher"`
	OutboundTag string          `json:"outbound"`
}

// qnodeRoute* mirrors the kernel-neutral routing contract understood by the
// bundled QNode agent. It is deliberately private: the admin API keeps its
// compact matcher format while UniProxy translates at the trust boundary.
type qnodeRouteMatch struct {
	Domains        []string `json:"domains,omitempty"`
	DomainSuffixes []string `json:"domain_suffixes,omitempty"`
	IPCIDRs        []string `json:"ip_cidrs,omitempty"`
	Ports          []string `json:"ports,omitempty"`
	Networks       []string `json:"networks,omitempty"`
	SourceCIDRs    []string `json:"source_cidrs,omitempty"`
	SourcePorts    []string `json:"source_ports,omitempty"`
}

type qnodeRouteAction struct {
	Type   string `json:"type"`
	Target string `json:"target,omitempty"`
}

type qnodeRouteRule struct {
	Name   string           `json:"name,omitempty"`
	Match  qnodeRouteMatch  `json:"match"`
	Action qnodeRouteAction `json:"action"`
}

type qnodeOutbound struct {
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Settings json.RawMessage `json:"settings"`
}

// LoadRouting 取出该节点可用的出站与分流规则。
//
// 出站取「全局 + 本节点」两份：大多数出站（直连、拒绝、一条公共中转）
// 本来就该全局共享，逐节点复制只会在改的时候漏掉几台。
// 同名时节点私有的覆盖全局的 —— 这样个别节点可以针对性替换某条线路，
// 而不必把全局那条也改掉。
func (s *Service) LoadRouting(ctx context.Context, tenantID, nodeID string) ([]NodeOutbound, []NodeRoute, error) {
	var outs []NodeOutbound
	var routes []NodeRoute

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tag, type, settings, (node_id IS NOT NULL) AS scoped
			  FROM node_outbounds
			 WHERE tenant_id = $1 AND (node_id IS NULL OR node_id = $2::uuid)
			 ORDER BY scoped, sort_order, tag`, tenantID, nodeID)
		if err != nil {
			return err
		}
		defer rows.Close()

		// 先全局后私有，同 tag 后者覆盖前者
		idx := make(map[string]int)
		for rows.Next() {
			var o NodeOutbound
			var scoped bool
			if err := rows.Scan(&o.Tag, &o.Type, &o.Settings, &scoped); err != nil {
				return err
			}
			if at, dup := idx[o.Tag]; dup {
				outs[at] = o
				continue
			}
			idx[o.Tag] = len(outs)
			outs = append(outs, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// 节点私有规则在前、全局规则在后：节点规则覆盖全局，私有兜底规则会遮住
		// 全局规则（后台编辑器提示）。与 loadEffectiveRoutingTx 同一口径
		rrows, err := tx.Query(ctx, `
			SELECT matcher, outbound_tag
			  FROM node_routes
			 WHERE tenant_id = $1 AND (node_id = $2::uuid OR node_id IS NULL) AND enabled
			 ORDER BY (node_id IS NULL), priority, created_at`, tenantID, nodeID)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var r NodeRoute
			if err := rrows.Scan(&r.Matcher, &r.OutboundTag); err != nil {
				return err
			}
			routes = append(routes, r)
		}
		return rrows.Err()
	})
	return outs, routes, err
}

func (s *Service) BuildNodeConfig(n *ServingNode) ([]byte, string, error) {
	kernel := n.Kernel
	if kernel == "" {
		kernel = "auto"
	}
	base := map[string]any{
		// QNode's panel client rejects a config without protocol. Keep this in
		// the reserved base map so protocol_config can never spoof a different
		// runtime protocol than the node record selected by the credential.
		"protocol":    CanonicalNodeType(n.NodeType),
		"server_port": n.ServerPort,
		// kernel is retained for older UniProxy consumers. Current QNode reads
		// kernel_type and uses its local configured kernel when it is omitted.
		"kernel": kernel,
		"base_config": map[string]any{
			// 上报流量：60 秒够了，快了只是多写库。
			"push_interval": 60,
			// 拉用户名单：15 秒。这个值直接决定「付完钱多久能连上」——
			// 60 秒时实测新用户第一次连必失败，得等下一轮同步，
			// 而用户那边看到的只是「买了个连不上的东西」。
			// 代价是每个节点每分钟 4 次请求，可以忽略。
			"pull_interval": 15,
		},
	}
	switch kernel {
	case "sing-box":
		base["kernel_type"] = "singbox"
	case "xray-core":
		base["kernel_type"] = "xray"
	}
	if n.ServerHost != "" {
		base["host"] = n.ServerHost
		base["server_name"] = n.ServerHost
	}

	// 协议特有字段平铺到顶层，与 UniProxy 的形态一致。
	//
	// 中间过一道 toKernelConfig：管理端存的是 xboard 形状的字段名
	// （cipher、network_settings.path、bandwidth.up…），内核认的是它
	// 原来那套（method、path、up_mbps…）。翻译只发生在这里，数据面
	// 看到的东西和改名之前一字不差。
	if len(n.Protocol) > 0 {
		var stored map[string]any
		if err := json.Unmarshal(n.Protocol, &stored); err != nil {
			return nil, "", fmt.Errorf("节点 %s 的协议配置不是 JSON 对象: %w", n.Name, err)
		}
		// 冲突检查必须在翻译之前做：翻译会把 cipher 并进 method，之后
		// 两者就再也分不开，管理员同时填了两个不同值这件事会被悄悄吞掉。
		if CanonicalNodeType(n.NodeType) == "shadowsocks" {
			method, _ := stored["method"].(string)
			cipher, hasCipher := stored["cipher"].(string)
			if hasCipher && cipher != "" && method != "" && cipher != method {
				return nil, "", fmt.Errorf("节点 %s 的 Shadowsocks method/cipher 冲突", n.Name)
			}
		}
		extra := toKernelConfig(CanonicalNodeType(n.NodeType), stored)
		for k, v := range extra {
			// 不允许协议配置覆盖 server_port 等基础字段：
			// 那会让管理端两处配置打架，且排查时极难发现
			if _, taken := base[k]; !taken {
				base[k] = v
			}
		}
		// QNode 的 UniProxy 模型沿用 XBoard 的 cipher 叫法，同时下发两个名字。
		// 冲突已经在翻译之前查过了，这里只负责补上别名。
		if CanonicalNodeType(n.NodeType) == "shadowsocks" {
			if method, _ := extra["method"].(string); method != "" {
				base["cipher"] = method
			}
		}
	}

	// 出站与分流。为空时整个键都不出现 —— 节点端据此判断
	// 「这个节点没配分流」，而不是「配了一份空的」，两者行为不同：
	// 前者保持默认直出，后者会被当成一份要生效的空规则表。
	if len(n.Outbounds) > 0 {
		base["outbounds"] = n.Outbounds
		qnodeOutbounds := make([]qnodeOutbound, 0, len(n.Outbounds))
		for _, outbound := range n.Outbounds {
			// QNode always creates these two built-ins. Sending duplicates would
			// either fail validation or shadow the fail-safe defaults.
			if outbound.Type == "direct" || outbound.Type == "block" {
				continue
			}
			settings := outbound.Settings
			if len(settings) == 0 {
				settings = json.RawMessage(`{}`)
			}
			qnodeOutbounds = append(qnodeOutbounds, qnodeOutbound{
				Tag: outbound.Tag, Protocol: outbound.Type, Settings: settings,
			})
		}
		if len(qnodeOutbounds) > 0 {
			base["custom_outbounds"] = qnodeOutbounds
		}
	}
	if len(n.Routes) > 0 {
		base["routes"] = n.Routes
		qnodeRoutes := make([]qnodeRouteRule, 0, len(n.Routes))
		for index, route := range n.Routes {
			match, err := qnodeMatch(route.Matcher)
			if err != nil {
				return nil, "", fmt.Errorf("节点 %s 的第 %d 条分流规则无效: %w", n.Name, index+1, err)
			}
			action := qnodeRouteAction{Type: route.OutboundTag}
			if action.Type != "direct" && action.Type != "block" {
				action = qnodeRouteAction{Type: "route", Target: route.OutboundTag}
			}
			qnodeRoutes = append(qnodeRoutes, qnodeRouteRule{
				Name: fmt.Sprintf("panel-%d", index+1), Match: match, Action: action,
			})
		}
		base["custom_route_rules"] = qnodeRoutes
	}

	body, err := json.Marshal(base)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, fmt.Sprintf(`"%x"`, sum[:8]), nil
}

func qnodeMatch(raw json.RawMessage) (qnodeRouteMatch, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil || source == nil {
		return qnodeRouteMatch{}, errors.New("matcher 必须是 JSON 对象")
	}
	var result qnodeRouteMatch
	for key, value := range source {
		values, err := routingValueStrings(value)
		if err != nil {
			return qnodeRouteMatch{}, fmt.Errorf("matcher.%s %w", key, err)
		}
		switch key {
		case "domain", "domains":
			result.Domains = values
		case "domain_suffix", "domain_suffixes":
			result.DomainSuffixes = values
		case "ip", "ip_cidr", "ip_cidrs":
			result.IPCIDRs = values
		case "port", "ports":
			result.Ports = values
		case "network", "networks":
			result.Networks = values
		case "source", "source_ip_cidr", "source_cidrs":
			result.SourceCIDRs = values
		case "source_port", "source_ports":
			result.SourcePorts = values
		default:
			return qnodeRouteMatch{}, fmt.Errorf("matcher.%s 暂不支持跨内核转换", key)
		}
	}
	return result, nil
}

// ValidateRoutingMatcher applies the same cross-kernel translation rules used
// during UniProxy delivery. The bool reports an explicit empty catch-all.
func ValidateRoutingMatcher(raw json.RawMessage) (bool, error) {
	match, err := qnodeMatch(raw)
	if err != nil {
		return false, err
	}
	empty := len(match.Domains) == 0 && len(match.DomainSuffixes) == 0 &&
		len(match.IPCIDRs) == 0 && len(match.Ports) == 0 &&
		len(match.Networks) == 0 && len(match.SourceCIDRs) == 0 &&
		len(match.SourcePorts) == 0
	return empty, nil
}

func routingValueStrings(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		items = []any{value}
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		switch typed := item.(type) {
		case string:
			if typed == "" {
				return nil, errors.New("不能包含空值")
			}
			values = append(values, typed)
		case float64:
			if typed != float64(int64(typed)) {
				return nil, errors.New("数字必须是整数")
			}
			values = append(values, strconv.FormatInt(int64(typed), 10))
		default:
			return nil, errors.New("必须是字符串、整数或它们的数组")
		}
	}
	return values, nil
}
