// [INPUT]: 依赖 domain/nodefabric 的 GetGlobalRouting / SetGlobalRouting / GetNodeRouting / SetNodeRouting 与 NotifyNodeChanged，依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers 的 nodeGetGlobalRouting / nodeSetGlobalRouting 与单节点的 nodeGetRouting / nodeSetRouting
// [POS]: api/admin 的出站与分流（NODE-012）：全局路由（契约后台-07 GET / PUT v1/nodes/routing）与单节点路由只做解析、调用 nodefabric、提交后通知节点、写响应；校验、读写与审计在 nodefabric 的 routing_admin.go，生效口径在 routing_merge.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) nodeGetGlobalRouting(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.GetGlobalRouting(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

type globalRoutingReq struct {
	ExpectedRevision string                       `json:"expected_revision"`
	Outbounds        []nodefabric.RoutingOutbound `json:"outbounds"`
	Routes           []nodefabric.RoutingRule     `json:"routes"`
}

// nodeSetGlobalRouting 全量替换全局出站与规则并发布到全部未退役节点。
func (h *handlers) nodeSetGlobalRouting(w http.ResponseWriter, r *http.Request) {
	var req globalRoutingReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	tenantID := httpx.TenantIDFrom(r.Context())
	res, err := h.d.Node.SetGlobalRouting(r.Context(), nodefabric.SetGlobalRoutingInput{
		TenantID: tenantID, ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		ExpectedRevision: req.ExpectedRevision, Outbounds: req.Outbounds, Routes: req.Routes,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 提交之后再推：推送失败不影响已经落库的配置，节点下次拉取同样拿到新版
	for _, id := range res.NodeIDs {
		h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)
	}
	httpx.OK(w, map[string]any{"ok": true, "revision": res.Revision, "affected_nodes": len(res.NodeIDs)})
}

type routingPayload struct {
	RowVersion int64                        `json:"row_version"`
	Outbounds  []nodefabric.RoutingOutbound `json:"outbounds"`
	Routes     []nodefabric.RoutingRule     `json:"routes"`
}

// nodeGetRouting 读取某节点的私有出站与分流。
func (h *handlers) nodeGetRouting(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.GetNodeRouting(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// nodeSetRouting 全量替换某节点的私有出站与分流。
func (h *handlers) nodeSetRouting(w http.ResponseWriter, r *http.Request) {
	var req routingPayload
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	rowVersion, err := h.d.Node.SetNodeRouting(r.Context(), nodefabric.SetNodeRoutingInput{
		TenantID: tenantID, ActorID: httpx.PrincipalFrom(r.Context()).UserID, NodeID: id,
		RowVersion: req.RowVersion, Outbounds: req.Outbounds, Routes: req.Routes,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 提交之后通知节点：原先只递增 config_source_generation，在线节点要等
	// 下一轮轮询才生效，长连接推送形同虚设（缺陷 18）
	h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)
	httpx.OK(w, map[string]any{"ok": true, "row_version": rowVersion})
}
