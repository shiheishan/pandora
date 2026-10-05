// [INPUT]: 依赖 domain/nodefabric 的节点列表 ListAdminNodes、旧状态接口 SetLegacyNodeStatus、吊销 RevokeNodeIdentity 与令牌、配置、协议、指标用例，依赖 domain/subscription 的 DeliveryState 与 HeartbeatFreshWindow，依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers 的 nodeList、nodeIssueToken、serverIssueToken、nodeSetStatus、nodeRevokeIdentity、nodePublishConfig、nodeSetProtocol、nodeProtocolSchemas、nodeIssueServerToken、nodeMetrics、nodeRealityKeypair、nodeDelete
// [POS]: api/admin 的节点处理器（NODE / AGT）：从 handlers.go 拆出，不跑 SQL（读写都在 nodefabric）。节点列表的心跳判定只在 Go 侧做（LastBeat 指针），下发状态交给 subscription.DeliveryState（nodefabric 不能反向依赖 subscription，所以留在这一层），协议配置在这里脱敏；单节点路由在 node_routing.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 节点（NODE / AGT）
//------------------------------------------------------------------------------

// nodeListItem 是节点列表的一行：库里的事实（nodefabric.AdminNodeListRow）加上 Go 侧判定的下发状态。
type nodeListItem struct {
	nodefabric.AdminNodeListRow
	// Delivered 回答运营真正关心的那个问题：这个节点现在会不会
	// 出现在用户的订阅里。
	//
	// 它和 Stale 是两回事，不能合并：Stale 是 90 秒的实时视角，
	// 给管理员看「刚刚是不是失联了」；下发用的是 10 分钟窗口，
	// 而且从未心跳过的节点一律不发。只看 Stale 会得出错误结论 ——
	// 一个 stale=true 的节点很可能仍在下发（心跳刚断两分钟），
	// 而一个从未心跳的节点即使刚建好也永远不会下发。
	//
	// 少了这一列，管理员就得自己在脑子里跑一遍下发规则。
	Delivered bool `json:"delivered_to_users"`
	// DeliveryNote 说明「为什么不下发」。没有它，界面上就只是一个
	// 灰点，运营还得来问。
	DeliveryNote string `json:"delivery_note"`
}

func (h *handlers) nodeList(w http.ResponseWriter, r *http.Request) {
	includeRetired := r.URL.Query().Get("include_retired") == "1"
	// 原先写死 LIMIT 200、total 取本页条数：第 201 个节点起被静默截掉，
	// 前端还以为那就是全部（缺陷 21）。现在可分页，total 是真实总数。
	limit, offset := nodeListPage(r.URL.Query())
	rows, total, err := h.d.Node.ListAdminNodes(r.Context(), httpx.TenantIDFrom(r.Context()),
		includeRetired, limit, offset)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	out := make([]nodeListItem, 0, len(rows))
	for _, x := range rows {
		// 心跳的两个事实由 Go 侧从 LastBeat 推出，不在 SQL 里算。
		//
		// 一开始是写成 SQL 布尔列的，结果 last_heartbeat_at 为 NULL 时
		// (NULL >= …) 求值成 NULL 而不是 false，扫进 bool 直接把整个
		// 节点列表打成 500。可以用 COALESCE 补，但那是在给一个本来
		// 就不该存在的三值逻辑打补丁 —— LastBeat 是 *time.Time，
		// 「从未心跳」本来就由 nil 表达得清清楚楚。
		item := nodeListItem{AdminNodeListRow: x}
		everSeen := x.LastBeat != nil
		beatFresh := everSeen &&
			time.Since(*x.LastBeat) < subscription.HeartbeatFreshWindow
		item.Delivered, item.DeliveryNote = subscription.DeliveryState(
			x.ServingStatus, x.PoolID != nil, everSeen, beatFresh)
		item.Protocol = nodefabric.RedactProtocolConfig(x.Protocol)
		out = append(out, item)
	}
	httpx.OK(w, nodeListResponse{Nodes: out, Total: total})
}

// nodeListResponse 是 GET v1/nodes 的响应；Nodes 非 nil，空页编成 []。
type nodeListResponse struct {
	Nodes []nodeListItem `json:"nodes"`
	Total int64          `json:"total"`
}

// nodeListPage 解析节点列表的分页参数。默认 500 条、最多 1000 条：前端按
// 设计在本地做筛选与搜索，一页要装得下常规规模的全部节点。非法值回默认。
func nodeListPage(q url.Values) (limit, offset int) {
	limit, offset = 500, 0
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("limit"))); err == nil && v >= 1 && v <= 1000 {
		limit = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("offset"))); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

// panelURLFrom 推导机器该回连的面板地址。
//
// 后台通常挂在反代后面，r.TLS 和 r.Host 都反映不出用户实际访问的入口，
// 所以优先信反代给的 X-Forwarded-*。推错了的后果很直接：命令复制到
// 机器上跑，连的是内网地址，接不进来。
type issueTokenReq struct {
	NodeName   string `json:"node_name"`
	PoolID     string `json:"pool_id"`
	ServerID   string `json:"server_id"`
	TTLMinutes int    `json:"ttl_minutes"`
}

func (h *handlers) nodeIssueToken(w http.ResponseWriter, r *http.Request) {
	var req issueTokenReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	out, err := h.d.Node.IssueBootstrapToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.IssueTokenInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			NodeName:   req.NodeName,
			PoolID:     req.PoolID,
			ServerID:   req.ServerID,
			PanelURL:   panelURL,
			TTLMinutes: req.TTLMinutes,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

// serverIssueToken 给一台已建好的服务器签发接入令牌。
//
// 和 nodeIssueToken 是同一件事，区别只在 server_id 从路径来、节点名默认
// 取服务器名。单独开一个入口是为了让「在服务器详情里拿接入命令」这个
// 动作不需要前端自己拼节点名——那正是容易填错、导致同一台机器接出两条
// 记录的地方。
func (h *handlers) serverIssueToken(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	var req issueTokenReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	tenantID := httpx.TenantIDFrom(r.Context())
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		srv, err := h.d.Node.GetServer(r.Context(), tenantID, serverID)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
		nodeName = srv.Name
	}
	out, err := h.d.Node.IssueBootstrapToken(r.Context(), tenantID,
		nodefabric.IssueTokenInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			NodeName:   nodeName,
			PoolID:     req.PoolID,
			ServerID:   serverID,
			PanelURL:   panelURL,
			TTLMinutes: req.TTLMinutes,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type nodeStatusReq struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

// nodeSetStatus 推进节点状态（旧状态接口）。状态机、发布锁、身份吊销与审计在
// nodefabric.SetLegacyNodeStatus，这里只解析请求与写响应。
func (h *handlers) nodeSetStatus(w http.ResponseWriter, r *http.Request) {
	var req nodeStatusReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	err := h.d.Node.SetLegacyNodeStatus(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"),
		nodefabric.LegacyNodeStatusInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			RowVersion: req.RowVersion, Status: req.Status, Reason: req.Reason,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, nodeSetStatusResponse{OK: true, RowVersion: req.RowVersion + 1})
}

type nodeSetStatusResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
}

// nodeRevokeIdentity 吊销节点身份（NODE-014）。
// 吊销后该节点的 Agent 下一次请求就会被拒，必须重新引导。
func (h *handlers) nodeRevokeIdentity(w http.ResponseWriter, r *http.Request) {
	if err := h.d.Node.RevokeNodeIdentity(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id")); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, nodeRevokeIdentityResponse{OK: true})
}

type nodeRevokeIdentityResponse struct {
	OK bool `json:"ok"`
}

type publishCfgReq struct {
	Scope    string          `json:"scope"`     // global / pool / node
	ScopeRef string          `json:"scope_ref"` // pool/node 时必填
	Payload  json.RawMessage `json:"payload"`
}

// nodePublishConfig 发布一层配置并签名（AGT-007）。
func (h *handlers) nodePublishConfig(w http.ResponseWriter, r *http.Request) {
	var req publishCfgReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Scope != "global" && req.Scope != "pool" && req.Scope != "node" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"scope": "只支持 global/pool/node"}))
		return
	}
	if req.Scope != "global" && req.ScopeRef == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"scope_ref": "该层级必须指定对象"}))
		return
	}
	var probe map[string]any
	if err := json.Unmarshal(req.Payload, &probe); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"payload": "必须是 JSON 对象"}))
		return
	}

	out, err := h.d.Node.PublishConfig(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.PublishInput{
			ActorID:  httpx.PrincipalFrom(r.Context()).UserID,
			Scope:    req.Scope,
			ScopeRef: req.ScopeRef,
			Payload:  req.Payload,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type nodeProtoReq struct {
	RowVersion  int64           `json:"row_version"`
	NodeType    string          `json:"node_type"`
	ServerHost  string          `json:"server_host"`
	ServerPort  int             `json:"server_port"`
	TrafficRate float64         `json:"traffic_rate"`
	DisplayName string          `json:"display_name"`
	Kernel      string          `json:"kernel"`
	Protocol    json.RawMessage `json:"protocol_config"`
}

// nodeSetProtocol 配置节点的对外服务参数（UniProxy 下发给节点端的内容）。
func (h *handlers) nodeSetProtocol(w http.ResponseWriter, r *http.Request) {
	var req nodeProtoReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.TrafficRate <= 0 {
		req.TrafficRate = 1
	}
	if req.Kernel == "" {
		req.Kernel = "auto"
	}
	if len(req.Protocol) == 0 {
		req.Protocol = json.RawMessage(`{}`)
	}
	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	actor := httpx.PrincipalFrom(r.Context()).UserID
	req.NodeType = nodefabric.CanonicalNodeType(req.NodeType)
	// Keep the compatibility endpoint, but route it through the same stable
	// schema validation, optimistic lock and audit path as PATCH /nodes/{id}.
	out, err := h.d.Node.PatchAdminNode(r.Context(), tenantID, id, nodefabric.PatchAdminNodeInput{
		ActorID: actor, RowVersion: req.RowVersion, NodeType: &req.NodeType,
		ServerHost: &req.ServerHost, ServerPort: &req.ServerPort, Kernel: &req.Kernel,
		TrafficRate: &req.TrafficRate, DisplayName: &req.DisplayName,
		ProtocolConfig: &req.Protocol,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) nodeProtocolSchemas(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, nodeProtocolSchemasResponse{Schemas: nodefabric.ProtocolSchemas()})
}

type nodeProtocolSchemasResponse struct {
	Schemas []nodefabric.ProtocolSchema `json:"schemas"`
}

// nodeIssueServerToken 签发 UniProxy 接入令牌，明文只返回一次。
func (h *handlers) nodeIssueServerToken(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	tok, nodeType, err := h.d.Node.IssueServerToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, nodeID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if nodeType == "" {
		nodeType = "vless"
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 一并给出可直接粘贴的安装命令。令牌只显示这一次；命令本身通过
	// 终端读取令牌，不把运行凭据嵌进 argv 或 shell history。
	install := nodefabric.RenderLegacyInstallCommand(panelURL, nodeID, nodeType)
	httpx.Created(w, nodeIssueServerTokenResponse{
		Token:          tok,
		NodeType:       nodeType,
		PanelURL:       panelURL,
		InstallCommand: install,
		Hint:           "该令牌只显示一次。重新签发会立即作废旧令牌——正在运行的节点会拉配置失败（401）直到用新令牌重装，请确认后再执行安装命令。",
	})
}

type nodeIssueServerTokenResponse struct {
	Token          string `json:"token"`
	NodeType       string `json:"node_type"`
	PanelURL       string `json:"panel_url"`
	InstallCommand string `json:"install_command"`
	Hint           string `json:"hint"`
}

func nullStrAdmin(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nodeMetrics 返回节点探针曲线。
func (h *handlers) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.d.Node.FetchMetrics(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), atoiDefault(r.URL.Query().Get("minutes"), 60))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, m)
}

// nodeRealityKeypair 生成一对 REALITY 用的 x25519 密钥。
//
// 放在服务端而不是让管理员自己跑 xray x25519：那条路要求他能登上某台机器，
// 而且生成的私钥会经过他的剪贴板和终端历史。这里私钥只在这一次响应里出现，
// 保存后就只以密文形式留在协议配置里，读接口不回显。
func (h *handlers) nodeRealityKeypair(w http.ResponseWriter, r *http.Request) {
	priv, pub, err := nodefabric.GenerateRealityKeypair()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	// short id 一并给出：它不是密钥，但少了它客户端连不上，
	// 而「自己想一个十六进制串」是很容易填错的一步。
	var sid [4]byte
	if _, err := rand.Read(sid[:]); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, nodeRealityKeypairResponse{
		PrivateKey: priv,
		PublicKey:  pub,
		ShortID:    hex.EncodeToString(sid[:]),
		Hint:       "私钥只在这一次返回，保存后无法再查看",
	})
}

type nodeRealityKeypairResponse struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
	Hint       string `json:"hint"`
}

type nodeDeleteReq struct {
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

// nodeDelete 删除一个已下线的逻辑节点。
func (h *handlers) nodeDelete(w http.ResponseWriter, r *http.Request) {
	var req nodeDeleteReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Node.DeleteNode(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.DeleteNodeInput{
			ID: chi.URLParam(r, "id"), RowVersion: req.RowVersion,
			ActorID: p.UserID, Reason: req.Reason,
		}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, nodeDeleteResponse{Deleted: true})
}

type nodeDeleteResponse struct {
	Deleted bool `json:"deleted"`
}
