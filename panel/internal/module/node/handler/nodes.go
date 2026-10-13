package handler

// 本文件归 p5-inventory：节点的后台接口。

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
)

const (
	nodeListDefaultLimit = 500
	nodeListMaxLimit     = 1000
	nodeKeypairHint      = "私钥只在这一次返回，保存后无法再查看"
)

// nodeRate 把请求里的倍率转成 decimal；nil 表示没给。
func nodeRate(v *float64) *decimal.Decimal {
	if v == nil {
		return nil
	}
	d := decimal.NewFromFloat(*v)
	return &d
}

func (h *Handler) listNodes(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, nodeListDefaultLimit, nodeListMaxLimit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	q := service.NodeQuery{
		IncludeRetired: c.Query("include_retired") == "1",
		State:          c.Query("state"),
		Search:         c.Query("q"),
		Limit:          limit,
		Offset:         offset,
	}
	if raw := c.Query("id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"id": "格式不正确"}))
			return
		}
		q.ID = &id
	}
	rows, total, err := h.svc.QueryNodes(c.Request.Context(), q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if q.ID != nil {
		out := make([]nodeDetailResponse, 0, len(rows))
		for _, r := range rows {
			out = append(out, nodeDetailFrom(r))
		}
		httpx.OK(c, nodesDetailResponse{Nodes: out, Total: total})
		return
	}
	out := make([]nodeRowResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, nodeRowFrom(r))
	}
	httpx.OK(c, nodesListResponse{Nodes: out, Total: total})
}

func (h *Handler) createNode(c *gin.Context) {
	var req nodeCreateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	serverID, err := nodeParseRequiredID("server_id", req.ServerID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	poolID, err := nodeParseOptionalID("pool_id", req.PoolID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	n, err := h.svc.CreateNode(c.Request.Context(), service.NodeInput{
		Name:           req.Name,
		ServerID:       serverID,
		PoolID:         poolID,
		NodeType:       req.NodeType,
		ServerHost:     req.ServerHost,
		ServerPort:     req.ServerPort,
		Kernel:         req.Kernel,
		TrafficRate:    nodeRate(req.TrafficRate),
		DisplayName:    req.DisplayName,
		CountryCode:    req.CountryCode,
		ProtocolConfig: req.ProtocolConfig,
		SortOrder:      req.SortOrder,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, adminNodeFrom(n))
}

func (h *Handler) patchNode(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodePatchRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := service.NodePatch{
		RowVersion:  req.RowVersion,
		Name:        req.Name,
		NodeType:    req.NodeType,
		ServerHost:  req.ServerHost,
		ServerPort:  req.ServerPort,
		Kernel:      req.Kernel,
		TrafficRate: nodeRate(req.TrafficRate),
		DisplayName: req.DisplayName,
		CountryCode: service.OptionalString{Set: req.CountryCode.Set, Value: req.CountryCode.Value},
	}
	if req.PoolID.Set {
		in.PoolID.Set = true
		if req.PoolID.Value != nil {
			pool, err := nodeParseOptionalID("pool_id", *req.PoolID.Value)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			in.PoolID.Value = pool
		}
	}
	if req.ProtocolConfig != nil {
		if _, ok := nodeJSONObject(req.ProtocolConfig); !ok {
			httpx.Fail(c, errs.Invalid(map[string]string{"protocol_config": "必须是 JSON 对象"}))
			return
		}
		in.ProtocolConfig = req.ProtocolConfig
	}
	n, err := h.svc.PatchNode(c.Request.Context(), id, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminNodeFrom(n))
}

func (h *Handler) copyNode(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeCopyRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	target, err := nodeParseOptionalID("target_server_id", req.TargetServerID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	pool, err := nodeParseOptionalID("pool_id", req.PoolID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	n, err := h.svc.CopyNode(c.Request.Context(), id, service.NodeCopyInput{
		RowVersion:     req.RowVersion,
		Name:           req.Name,
		TargetServerID: target,
		PoolID:         pool,
		SortOrder:      req.SortOrder,
		ServerPort:     req.ServerPort,
		CopyRouting:    req.CopyRouting,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, adminNodeFrom(n))
}

func (h *Handler) moveNode(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeMoveRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	serverID, err := nodeParseRequiredID("server_id", req.ServerID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	n, err := h.svc.MoveNode(c.Request.Context(), id, service.NodeMoveInput{
		RowVersion: req.RowVersion, ServerID: serverID, Reason: req.Reason,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminNodeFrom(n))
}

func (h *Handler) reorderNodes(c *gin.Context) {
	var req nodeReorderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]service.NodeOrderItem, 0, len(req.Items))
	for _, it := range req.Items {
		id, err := uuid.Parse(strings.TrimSpace(it.ID))
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"items": "节点 ID 格式不正确"}))
			return
		}
		items = append(items, service.NodeOrderItem{ID: id, RowVersion: it.RowVersion, SortOrder: it.SortOrder})
	}
	if err := h.svc.ReorderNodes(c.Request.Context(), items); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeOKUpdatedResponse{OK: true, Updated: len(items)})
}

func (h *Handler) batchNodeStatus(c *gin.Context) {
	var req nodeStatusBatchRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]service.NodeVersionRef, 0, len(req.Items))
	for _, it := range req.Items {
		id, err := uuid.Parse(strings.TrimSpace(it.ID))
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"items": "节点 ID 格式不正确"}))
			return
		}
		items = append(items, service.NodeVersionRef{ID: id, RowVersion: it.RowVersion})
	}
	status := strings.TrimSpace(req.ServingStatus)
	err := h.svc.BatchNodeStatus(c.Request.Context(), service.NodeStatusBatch{
		Items: items, ServingStatus: status, Reason: req.Reason,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeBatchStatusResponse{OK: true, Updated: len(items), ServingStatus: status})
}

func (h *Handler) activateNode(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeActivateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	n, err := h.svc.ActivateNode(c.Request.Context(), id, req.RowVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminNodeFrom(n))
}

func (h *Handler) retireNode(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeRetireRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	n, err := h.svc.RetireNode(c.Request.Context(), id, req.RowVersion, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminNodeFrom(n))
}

func (h *Handler) deleteNode(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeDeleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.DeleteNode(c.Request.Context(), id, req.RowVersion, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeDeletedResponse{Deleted: true})
}

// publishNodeConfig 是「强制重新下发」：旧的分层配置已删，只接受 scope=node 与空对象 payload。
func (h *Handler) publishNodeConfig(c *gin.Context) {
	var req nodePublishRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if strings.TrimSpace(req.Scope) != "node" {
		fields["scope"] = "只支持 node"
	}
	var id uuid.UUID
	ref := strings.TrimSpace(req.ScopeRef)
	if ref == "" {
		fields["scope_ref"] = "该层级必须指定对象"
	} else if parsed, err := uuid.Parse(ref); err != nil {
		fields["scope_ref"] = "格式不正确"
	} else {
		id = parsed
	}
	if obj, ok := nodeJSONObject(req.Payload); !ok {
		fields["payload"] = "必须是 JSON 对象"
	} else if len(obj) > 0 {
		fields["payload"] = "不再支持分层配置，只能是空对象 {}"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	v, err := h.svc.RepublishNode(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, nodePublishResponse{ConfigID: id, Version: v, Scope: "node", AffectedNodes: 1})
}

// realityKeypair 生成一对 REALITY 密钥，不读请求体、不落库。
func (h *Handler) realityKeypair(c *gin.Context) {
	kp, err := h.svc.NewRealityKeypair()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeRealityKeypairResponse{
		PrivateKey: kp.PrivateKey, PublicKey: kp.PublicKey, ShortID: kp.ShortID, Hint: nodeKeypairHint,
	})
}

// protocolSchemas 返回全部稳定协议的 schema（纯数据，不经 service）。
func (h *Handler) protocolSchemas(c *gin.Context) {
	list := protocol.Schemas()
	out := make([]nodeProtocolSchemaResponse, 0, len(list))
	for _, s := range list {
		out = append(out, nodeProtocolSchemaFrom(s))
	}
	httpx.OK(c, nodeProtocolSchemasResponse{Schemas: out})
}
