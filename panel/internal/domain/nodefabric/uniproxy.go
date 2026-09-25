// [INPUT]: 依赖 platform 的 crypto/db/httpx/audit；读 node_pool_user_groups（00093）与 users.user_group_id；读写 quota_balances 与 traffic_pack_grants（迁移 00070），经 usage_daily.go 的 chargeReportEntry 逐用户记账
// [OUTPUT]: 对外提供 ServingNode、AuthenticateNode、IssueServerToken、BuildNodeConfig、路由校验、ListNodeUsers、PoolAdmitsUserSQL（池限定用户组的唯一谓词）、ReportTraffic / ReportAlive / ReportRuntimeStatus
// [POS]: domain/nodefabric 的 UniProxy 兼容数据面：节点鉴权、令牌签发（写审计、记签发时间与签发人、拒绝已退出服务的节点）、用户下发（只下发给套餐绑定了本节点所在池的订阅，无池节点不下发任何人；池限定了用户组时只给名单内组的用户，R104）与流量上报（先扣套餐本周期额度，超出部分扣用户流量包余额，D-E-1；同事务累加按日用量，00072）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// UniProxy 协议实现（Xboard / V2board 兼容）。
//
// 这套接口的调用方是 V2bX、XrayR、Xboard-Node 这类现成节点端。协议本身很朴素，
// 但有两处必须按它的原样来，否则节点端会静默不工作：
//
//   - 用户 ID 是整数，且 push 上报时作为 JSON 的 key（因此是字符串形式的数字）
//   - config 支持 ETag，节点端靠 304 判断「配置没变」，返回体必须逐字节稳定，
//     所以下面所有 map 都先转成结构体再序列化，不直接 marshal map
//
// 与自研 Agent 的关系：这里只管数据面（谁能连、用了多少流量），
// 节点的生命周期、探针、配置签名仍走 aegis-agent 那条路。两者互不依赖。

//------------------------------------------------------------------------------
// 节点鉴权
//------------------------------------------------------------------------------

type ServingNode struct {
	ID       string
	Name     string
	NodeType string
	// DeclaredType 是节点端在 URL 上自称的协议，仅在它和 NodeType 不一致
	// 时才有值。用来让上层记一条日志——多半意味着刚在面板上改过协议、
	// 节点端还没追上，属于正常的过渡态，但值得看见。
	DeclaredType  string
	ServerHost    string
	ServerPort    int
	TrafficRate   float64
	Protocol      json.RawMessage
	PoolID        *string
	Status        string
	ServerStatus  string
	ServingStatus string
	// Kernel 决定由哪个内核承载：auto / pandora-native / sing-box / xray-core。
	// auto 与 pandora-native 均要求当前 NativeCore 数据面；后两者只为
	// 显式兼容迁移保留。
	Kernel string

	// 出站与分流，由 LoadRouting 填充
	Outbounds []NodeOutbound
	Routes    []NodeRoute
}

// AuthenticateNode 校验 UniProxy 请求携带的 node_id + token。
//
// token 在库里只有哈希。node_id 走的是节点 UUID，而不是协议里常见的自增整数 ——
// 节点数量有限，用 UUID 不会给节点端造成困扰，却省掉一套自增 ID 映射。
func (s *Service) AuthenticateNode(ctx context.Context, tenantID, nodeID, token, nodeType string) (*ServingNode, error) {
	if nodeID == "" || token == "" {
		return nil, httpx.New(httpx.CodeUnauthorized, "缺少 node_id 或 token")
	}

	var n ServingNode
	var proto []byte
	var isControlNode bool
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT n.id, n.name, coalesce(n.node_type,''), coalesce(n.server_host,''),
			       coalesce(n.server_port,0), n.traffic_rate, n.protocol_config, n.pool_id,
			       n.status, coalesce(n.kernel,'auto'), s.status, n.serving_status,
			       coalesce(s.control_node_id=n.id,false)
			  FROM nodes n
			  JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid
			   AND n.server_token_hash = $3
			   AND s.deleted_at IS NULL
			   AND s.status IN ('ready','draining')
			   AND n.serving_status IN ('active','draining')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND `+StableProtocolReadySQL("n"),
			tenantID, nodeID, crypto.HashToken(token),
		).Scan(&n.ID, &n.Name, &n.NodeType, &n.ServerHost, &n.ServerPort,
			&n.TrafficRate, &proto, &n.PoolID, &n.Status, &n.Kernel,
			&n.ServerStatus, &n.ServingStatus, &isControlNode)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 节点不存在与 token 不对返回同一种错误
		return nil, httpx.New(httpx.CodeUnauthorized, "节点认证失败")
	}
	if err != nil {
		return nil, err
	}
	n.NodeType = CanonicalNodeType(n.NodeType)
	// 节点端声明的协议和库里不一致时，以库为准，不再拒绝认证。
	//
	// 原先这里直接返回 401。它看着像一道安全检查，其实不是：能走到这行
	// 说明 node_id 和 token 都已经验过了，而持有正确 token 的人本来就能
	// 拿到这个节点的全部配置——URL 上多写一个协议名不会让他多拿到任何
	// 东西。它真正的作用只是「断言两边认知一致」。
	//
	// 代价却很大：管理员在面板上把节点从 shadowsocks 改成 vless，节点端
	// 还带着旧的 node_type 来拉配置，从这一刻起每次请求都 401——配置拉
	// 不到、心跳停、节点直接失联，而面板上看不出是自己刚才那次修改造成的。
	// 想恢复只能上服务器改 config.json 再重启，「不用手动碰节点端」这个
	// 前提就没了。
	//
	// 现在的做法：库里的协议是唯一权威，下发的配置里带着它（protocol
	// 字段），节点端据此切换适配器。不一致只记一条日志——它仍然是个值得
	// 知道的信号（多半意味着刚改过协议、节点端还没追上），但不该让节点掉线。
	// Service 没有 logger，也不值得为一条日志改它的构造签名。把这个事实
	// 挂在返回值上，由 handler 那层（有 logger）决定怎么记。
	if requestedType := CanonicalNodeType(nodeType); requestedType != "" && requestedType != n.NodeType {
		n.DeclaredType = requestedType
	}

	// 只有正式在役的节点能拉用户。standby/canary 阶段的节点若也能拉，
	// 灰度就失去意义了 —— 用户会被分配到还没验证完的机器上（NODE-010）。
	if !legacyNodeStatusAllowsServing(isControlNode, n.Status) {
		return nil, httpx.New(httpx.CodeForbidden, "节点当前状态不可提供服务")
	}
	n.Protocol = proto
	return &n, nil
}

// A logical service Node has no Agent lifecycle of its own, so serving_status
// is authoritative. A Server control Node still carries the legacy physical
// Agent lifecycle and must pass both gates.
func legacyNodeStatusAllowsServing(isControlNode bool, legacyStatus string) bool {
	if !isControlNode {
		return true
	}
	return legacyStatus == "active" || legacyStatus == "canary" || legacyStatus == "draining"
}

// IssueServerToken 为节点签发 UniProxy 接入令牌，明文只返回一次。
//
// 顺带返回节点类型：调用方要用它拼一键安装命令，而这里本来就要写这一行
// UPDATE，用 RETURNING 带出来比让上层再查一次省一个来回。
//
// 已退役 / 已销毁的节点拒绝签发（409）：给一个不再服务的节点发接入凭据，
// 等于让一台本该下线的机器重新拿到用户名单。签发写审计，与换令牌、吊销
// 身份同级（SEC-012）；审计只记签发这件事，不记令牌或其哈希。
func (s *Service) IssueServerToken(ctx context.Context, tenantID, actorID, nodeID string) (string, string, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return "", "", httpx.New(httpx.CodeNotFound, "节点不存在")
	}
	tok, err := crypto.NewToken(24)
	if err != nil {
		return "", "", httpx.Internal(err)
	}
	var nodeType string
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status, servingStatus string
		scanErr := tx.QueryRow(ctx,
			`SELECT status, serving_status FROM nodes WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`,
			tenantID, nodeID).Scan(&status, &servingStatus)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "节点不存在")
		}
		if scanErr != nil {
			return scanErr
		}
		if serverTokenRefused(status, servingStatus) {
			return httpx.New(httpx.CodeConflict, "节点已退役或已销毁，不能签发接入令牌")
		}
		if err := tx.QueryRow(ctx,
			`UPDATE nodes SET server_token_hash = $3,
			        server_token_issued_at = now(), server_token_issued_by = $4::uuid
			  WHERE tenant_id = $1 AND id = $2::uuid
			 RETURNING coalesce(node_type, '')`,
			tenantID, nodeID, crypto.HashToken(tok), actorID).Scan(&nodeType); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID,
			Action: "node.server_token.issue", ResourceType: "node", ResourceID: &nodeID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx), Outcome: "success",
			AfterDigest: map[string]any{"node_type": nodeType, "old_token_revoked": true}})
	})
	if err != nil {
		return "", "", err
	}
	return tok, nodeType, nil
}

// serverTokenRefused 判定节点是否已退出服务、不能再签发接入令牌。
// 口径与 00058 的「非终态节点」一致：生命周期 retired / destroyed，
// 或服务状态 retired（后台「退役」即它）。
func serverTokenRefused(status, servingStatus string) bool {
	return status == "retired" || status == "destroyed" || servingStatus == "retired"
}

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

//------------------------------------------------------------------------------
// GET /api/v1/server/UniProxy/user
//------------------------------------------------------------------------------

// PoolAdmitsUserSQL 是节点池用户组限定（R104）的唯一 SQL 谓词：池没有限定
// （node_pool_user_groups 里没有它的行），或者用户所在的组在池的名单里。
// 默认组（users.user_group_id 为空）的用户进不了任何限定了的池——NULL 与
// 名单里的组连不上。
//
// 下发三处只能用它：本包 ListNodeUsers（节点拉用户），以及 subscription 的
// listEligibleNodesTx（订阅下载与门户预览）。三个参数是租户、池、用户的
// SQL 表达式，只接受这两个调用方的静态写法，绝不能是用户数据；写成白名单
// 也让「另写一份变体」在调用处就过不去。
func PoolAdmitsUserSQL(tenant, pool, user string) string {
	switch tenant + "|" + pool + "|" + user {
	case "s.tenant_id|$2::uuid|s.user_id", "n.tenant_id|n.pool_id|$4::uuid":
	default:
		panic("unsupported pool admission SQL expressions")
	}
	return "(NOT EXISTS (SELECT 1 FROM node_pool_user_groups npug" +
		" WHERE npug.tenant_id = " + tenant + " AND npug.pool_id = " + pool + ")" +
		" OR EXISTS (SELECT 1 FROM node_pool_user_groups npug" +
		" JOIN users npu ON npu.tenant_id = npug.tenant_id AND npu.user_group_id = npug.user_group_id" +
		" WHERE npug.tenant_id = " + tenant + " AND npug.pool_id = " + pool + " AND npu.id = " + user + "))"
}

type ProxyUser struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

// ListNodeUsers 返回该节点应当放行的用户。
//
// 过滤条件是这套系统里最要紧的一段 SQL —— 它同时决定了「谁能用」和「谁不能用」：
//   - 订阅必须处于可用状态（active / trialing，宽限期内也算）
//   - 套餐必须授权了该节点所属的资源池（XBD-010）；没划进池的节点不服务任何人（R104）
//   - 池限定了用户组时，用户所在的组必须在名单里（R104，PoolAdmitsUserSQL）
//   - 流量必须没跑超（USE-007 超额停用）
//
// 任何一条漏掉，都会变成免费用或该用用不了，两种都是事故。
func (s *Service) ListNodeUsers(ctx context.Context, tenantID string, n *ServingNode) ([]ProxyUser, error) {
	users := []ProxyUser{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 节点必须划进节点池，且订阅的套餐版本绑定了这个池。没划进池的节点
		// 不服务任何订阅（R104，fail closed）：$2 为 NULL 时等式求值为 NULL，
		// EXISTS 为假，列表为空。这与订阅下载、门户预览里的
		// JOIN plan_node_pools 是同一口径；过去这里把无池节点当成对所有有效
		// 订阅开放的公共节点，节点就成了绕过套餐授权的后门。
		//
		// 池限定了用户组时，还要用户所在的组在名单里（R104）。
		poolFilter := `
			AND EXISTS (SELECT 1 FROM plan_node_pools pnp
			             WHERE pnp.tenant_id = s.tenant_id
			               AND pnp.plan_version_id = s.plan_version_id
			               AND pnp.pool_id = $2::uuid)
			AND ` + PoolAdmitsUserSQL("s.tenant_id", "$2::uuid", "s.user_id")

		// 设备限制的判定模式。读设置失败时按 loose 走 ——
		// 配置读不出来不该导致所有人被当成超限踢下线。
		var mode string
		var grace int
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id = $1 AND key = 'device_limit.mode'), 'loose'),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id = $1 AND key = 'device_limit.grace'), 1)`,
			tenantID).Scan(&mode, &grace)
		strict := mode == "strict"

		rows, err := tx.Query(ctx, `
			SELECT s.node_uid, s.proxy_uuid::text,
			       coalesce(pv.throttle_kbps, 0),
			       -- 管理员在订阅上的覆盖优先于套餐规定
			       coalesce(s.device_limit, pv.max_devices, 0)
			  FROM subscriptions s
			  JOIN plan_versions pv ON pv.id = s.plan_version_id
			 WHERE s.tenant_id = $1
			   -- strict 模式：跨节点去重后仍然超限的，本轮不下发到任何节点。
			   --
			   -- 这是与 loose 唯一的区别。loose 下每个节点各判各的，
			   -- 用户在 N 个节点上能连出 N 倍的设备；strict 则把他整条订阅摘掉，
			   -- 直到在线数掉回限额以内。
			   --
			   -- grace 留出的余量用来吸收 IP 抖动：手机切换网络会让同一台设备
			   -- 短暂占两个 IP，卡得太死会让通勤路上的用户反复掉线。
			   AND ( $3::bool = false
			      OR coalesce(s.device_limit, pv.max_devices, 0) <= 0
			      OR NOT EXISTS (
			           SELECT 1 FROM subscription_online_devices d
			            WHERE d.subscription_id = s.id
			              AND d.device_count > coalesce(s.device_limit, pv.max_devices, 0) + $4 ) )
			   AND s.status IN ('active', 'trialing', 'grace')
			   AND (s.current_period_end IS NULL OR s.current_period_end > now())
			   AND EXISTS (
			       SELECT 1
			         FROM nodes gate_node
			         JOIN servers gate_server
			           ON gate_server.tenant_id=gate_node.tenant_id
			          AND gate_server.id=gate_node.server_id
			        WHERE gate_node.tenant_id=$1
			          AND gate_node.id=$5::uuid
			          AND gate_node.serving_status IN ('active','draining')
			          AND gate_server.status IN ('ready','draining')
			          AND gate_server.deleted_at IS NULL
			          AND gate_node.node_type IS NOT NULL
			          AND gate_node.server_port BETWEEN 1 AND 65535
			          AND `+StableProtocolReadySQL("gate_node")+`)
			   -- 流量耗尽的订阅不下发到节点（USE-007）：套餐额度用完、
			   -- 而且用户名下的流量包也没有剩余（D-E-1 先扣套餐再扣流量包）
			   AND ( NOT EXISTS (
			           SELECT 1 FROM quota_balances qb
			            WHERE qb.subscription_id = s.id
			              AND qb.metric = 'traffic.bytes'
			              AND qb.remaining IS NOT NULL
			              AND qb.remaining <= 0)
			      OR EXISTS (
			           SELECT 1 FROM traffic_pack_grants g
			            WHERE g.tenant_id = s.tenant_id AND g.user_id = s.user_id
			              AND g.consumed_bytes < g.granted_bytes) )
			`+poolFilter+`
			 ORDER BY s.node_uid`,
			tenantID, n.PoolID, strict, grace, n.ID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var u ProxyUser
			if err := rows.Scan(&u.ID, &u.UUID, &u.SpeedLimit, &u.DeviceLimit); err != nil {
				return err
			}
			users = append(users, u)
		}
		return rows.Err()
	})
	return users, err
}

//------------------------------------------------------------------------------
// POST /api/v1/server/UniProxy/push
//------------------------------------------------------------------------------

type PushResult struct {
	Accepted   int   `json:"accepted"`
	Duplicate  bool  `json:"duplicate"`
	TotalBytes int64 `json:"total_bytes"`
}

// ReportTraffic 接收节点上报的流量并扣减配额。
//
// 协议的缺陷与应对：UniProxy 的 push 提交的是**增量**且**没有幂等键**，
// 节点端重试会导致同一段流量被计两次。协议层无法修复，这里做两件事：
//
//  1. 近似去重 —— 同一节点在 10 秒内提交完全相同的报文视为重试，直接丢弃。
//     窗口取 10 秒是因为节点端的 push_interval 是 60 秒，正常情况下
//     两次上报不可能在 10 秒内且内容完全一致。
//  2. 原样留档 —— 每一次上报都写进 node_traffic_reports，
//     出现流量争议时可以逐笔回溯，这是唯一的证据来源。
func (s *Service) ReportTraffic(ctx context.Context, tenantID string, n *ServingNode, raw []byte) (*PushResult, error) {
	var report map[string][2]int64
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "上报格式非法")
	}

	sum := sha256.Sum256(raw)
	out := &PushResult{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// --- 近似去重 ---
		var dupID string
		err := tx.QueryRow(ctx, `
			SELECT id FROM node_traffic_reports
			 WHERE node_id = $1 AND content_hash = $2
			   AND received_at > now() - interval '10 seconds'
			 ORDER BY received_at DESC LIMIT 1`,
			n.ID, sum[:]).Scan(&dupID)
		isDup := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		var totalUp, totalDown int64
		for _, v := range report {
			totalUp += v[0]
			totalDown += v[1]
		}

		var reportID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO node_traffic_reports
				(tenant_id, node_id, user_count, total_upload, total_download,
				 traffic_rate, raw_payload, content_hash, duplicate_of)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING id`,
			tenantID, n.ID, len(report), totalUp, totalDown,
			n.TrafficRate, raw, sum[:], nullStr(dupID),
		).Scan(&reportID); err != nil {
			return err
		}

		out.TotalBytes = totalUp + totalDown
		out.Duplicate = isDup
		if isDup {
			// 留了档但不扣量
			return nil
		}

		// --- 扣减配额 ---
		// 按 uid 排序逐个处理：并发的两份上报以同一顺序锁配额行、流量包与
		// 当日用量行，不会交叉死锁（map 迭代顺序是随机的）。
		now := time.Now()
		for _, entry := range sortedReportEntries(report) {
			if entry.used <= 0 {
				continue
			}
			// 按节点倍率折算后计费
			billed := int64(float64(entry.used) * n.TrafficRate)
			accepted, err := chargeReportEntry(ctx, tx, tenantID, entry.uid, billed, now)
			if err != nil {
				return err
			}
			if accepted { // 订阅已删除的 uid 忽略
				out.Accepted++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type reportEntry struct {
	uid  int64
	used int64
}

// sortedReportEntries 把上报整理成按 uid 升序的 (uid, 上下行合计)；非法 key 跳过，
// 不因一个坏值毁掉整批。同一 uid 出现多种写法（"7" 与 "007"）时合并。
func sortedReportEntries(report map[string][2]int64) []reportEntry {
	byUID := map[int64]int64{}
	for key, v := range report {
		uid, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			continue
		}
		byUID[uid] += v[0] + v[1]
	}
	out := make([]reportEntry, 0, len(byUID))
	for uid, used := range byUID {
		out = append(out, reportEntry{uid: uid, used: used})
	}
	slices.SortFunc(out, func(a, b reportEntry) int { return cmp.Compare(a.uid, b.uid) })
	return out
}

// splitTrafficCharge 决定一笔用量怎么分：先吃套餐本周期剩余额度（planRoom，
// nil 表示不限量），超出的部分再从流量包余额里扣（packRemaining），
// 两者都不够的那部分仍记在套餐上（让配额变负、下一轮停止下发）。
// 返回 记到套餐配额上的量 与 从流量包扣的量。
func splitTrafficCharge(billed int64, planRoom *int64, packRemaining int64) (int64, int64) {
	if billed <= 0 {
		return 0, 0
	}
	if planRoom == nil {
		return billed, 0
	}
	overflow := billed - max(*planRoom, 0)
	if overflow <= 0 {
		return billed, 0
	}
	fromPacks := min(overflow, max(packRemaining, 0))
	return billed - fromPacks, fromPacks
}

// chargeTraffic 把一笔已计费的用量记到订阅配额与用户的流量包上（D-E-1）。
//
// 锁顺序：先锁本周期的流量配额行，再按先到先扣的顺序锁流量包。所有上报走同一
// 顺序。套餐剩余额度取本周期所有限量流量行里最紧的一条；没有限量行就是不限量，
// 不动流量包。
func chargeTraffic(ctx context.Context, tx pgx.Tx, tenantID, subID, userID string, billed int64) error {
	var planRoom *int64
	if err := tx.QueryRow(ctx, `
		SELECT min(limit_value + adjusted - consumed)
		  FROM (SELECT limit_value, adjusted, consumed FROM quota_balances
		         WHERE tenant_id = $1 AND subscription_id = $2::uuid
		           AND metric = 'traffic.bytes'
		           AND period_start <= now()
		           AND (period_end IS NULL OR period_end > now())
		         ORDER BY id FOR UPDATE) q
		 WHERE limit_value IS NOT NULL`, tenantID, subID).Scan(&planRoom); err != nil {
		return err
	}

	fromPacks := int64(0)
	if planRoom != nil && billed > max(*planRoom, 0) {
		type openGrant struct {
			id   string
			left int64
		}
		var grants []openGrant
		var packRemaining int64
		rows, err := tx.Query(ctx, `
			SELECT id::text, granted_bytes - consumed_bytes
			  FROM traffic_pack_grants
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			   AND consumed_bytes < granted_bytes
			 ORDER BY created_at, id FOR UPDATE`, tenantID, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g openGrant
			if err := rows.Scan(&g.id, &g.left); err != nil {
				rows.Close()
				return err
			}
			grants = append(grants, g)
			packRemaining += g.left
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		_, fromPacks = splitTrafficCharge(billed, planRoom, packRemaining)
		left := fromPacks
		for _, g := range grants {
			if left == 0 {
				break
			}
			take := min(left, g.left)
			if _, err := tx.Exec(ctx, `
				UPDATE traffic_pack_grants SET consumed_bytes = consumed_bytes + $3
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, g.id, take); err != nil {
				return err
			}
			left -= take
		}
	}

	if planCharge := billed - fromPacks; planCharge > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE quota_balances
			   SET consumed = consumed + $3
			 WHERE tenant_id = $1 AND subscription_id = $2::uuid AND metric = 'traffic.bytes'
			   AND period_start <= now()
			   AND (period_end IS NULL OR period_end > now())`,
			tenantID, subID, planCharge); err != nil {
			return err
		}
	}
	return nil
}

//------------------------------------------------------------------------------
// POST /api/v1/server/UniProxy/alive
//------------------------------------------------------------------------------

// ReportAlive 接收在线 IP 上报，用于设备数限制（XBD-008）。
//
// 只存 IP 的哈希：在线设备数是运营需要的指标，原始 IP 不是。
// 存哈希既能去重计数，又不会积累一份可回溯到个人的地址库。
func (s *Service) ReportAlive(ctx context.Context, tenantID string, n *ServingNode, raw []byte) (int, error) {
	var alive map[string][]string
	if err := json.Unmarshal(raw, &alive); err != nil {
		return 0, httpx.New(httpx.CodeBadRequest, "上报格式非法")
	}

	count := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for uidStr, ips := range alive {
			uid, err := strconv.ParseInt(uidStr, 10, 64)
			if err != nil {
				continue
			}
			var subID string
			if err := tx.QueryRow(ctx,
				`SELECT id FROM subscriptions WHERE tenant_id=$1 AND node_uid=$2`,
				tenantID, uid).Scan(&subID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			for _, ip := range ips {
				h := sha256.Sum256([]byte(ip))
				if _, err := tx.Exec(ctx, `
					INSERT INTO node_alive_ips (tenant_id, node_id, subscription_id, ip_hash)
					VALUES ($1,$2,$3,$4)
					ON CONFLICT (node_id, subscription_id, ip_hash)
					DO UPDATE SET last_seen_at = now()`,
					tenantID, n.ID, subID, h[:]); err != nil {
					return err
				}
				count++
			}
		}
		return nil
	})
	return count, err
}

type uniProxyResourcePair struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type uniProxyStatus struct {
	CPU  float64              `json:"cpu"`
	Mem  uniProxyResourcePair `json:"mem"`
	Swap uniProxyResourcePair `json:"swap"`
	Disk uniProxyResourcePair `json:"disk"`
}

func metricUnit(value, divisor uint64) int64 {
	const maxDBInt = int64(1<<31 - 1)
	converted := value / divisor
	if converted > uint64(maxDBInt) {
		return maxDBInt
	}
	return int64(converted)
}

// ReportRuntimeStatus accepts QNode's UniProxy resource report. It deliberately
// stores only coarse infrastructure metrics and never raw user or address data.
func (s *Service) ReportRuntimeStatus(ctx context.Context, tenantID string, n *ServingNode, raw []byte) error {
	var status uniProxyStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return httpx.New(httpx.CodeBadRequest, "状态上报格式非法")
	}
	if math.IsNaN(status.CPU) || math.IsInf(status.CPU, 0) || status.CPU < 0 || status.CPU > 100 ||
		status.Mem.Used > status.Mem.Total || status.Swap.Used > status.Swap.Total || status.Disk.Used > status.Disk.Total {
		return httpx.New(httpx.CodeBadRequest, "状态上报数值非法")
	}
	cpuBP := int(math.Round(status.CPU * 100))
	memUsed := metricUnit(status.Mem.Used, 1024*1024)
	memTotal := metricUnit(status.Mem.Total, 1024*1024)
	diskUsed := metricUnit(status.Disk.Used, 1024*1024*1024)
	diskTotal := metricUnit(status.Disk.Total, 1024*1024*1024)

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_metrics
				(tenant_id,node_id,cpu_bp,mem_used_mb,mem_total_mb,disk_used_gb,disk_total_gb)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (node_id,recorded_at) DO NOTHING`,
			tenantID, n.ID, cpuBP, memUsed, memTotal, diskUsed, diskTotal); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `
			UPDATE nodes SET last_heartbeat_at=now(), health_score=90
			 WHERE tenant_id=$1 AND id=$2`, tenantID, n.ID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return httpx.New(httpx.CodeNotFound, "节点不存在")
		}
		_, err = tx.Exec(ctx, `
			UPDATE servers SET last_heartbeat_at=now()
			 WHERE tenant_id=$1 AND id=(SELECT server_id FROM nodes WHERE tenant_id=$1 AND id=$2)`,
			tenantID, n.ID)
		return err
	})
}

// PurgeStaleAlive 清理过期的在线记录。由定时任务调用，幂等。
func (s *Service) PurgeStaleAlive(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`DELETE FROM node_alive_ips WHERE last_seen_at < now() - interval '10 minutes'`)
		if err != nil {
			return err
		}
		n = ct.RowsAffected()
		return nil
	})
	return n, err
}

var _ = time.Now // 保留 time 导入供将来的窗口计算使用
