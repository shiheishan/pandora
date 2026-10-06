// [INPUT]: 依赖 domain/nodefabric 的路由组服务（ListRouteGroups / CreateRouteGroup / UpdateRouteGroup / DeleteRouteGroup / GetGroupRouting / SetGroupRouting / SetRouteGroupMembers / SetNodeRouteGroups / PreviewNodeRouting）与 NotifyNodeChanged，依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers 的 listRouteGroups / createRouteGroup / updateRouteGroup / deleteRouteGroup / getRouteGroupRouting / setRouteGroupRouting / setRouteGroupMembers / setNodeRouteGroups / nodeEffectiveRouting
// [POS]: api/admin 的路由组（00096，NODE-012 第三个范围）：只解析请求、调 nodefabric、提交后逐个通知 generation 被推进的节点、写响应；不跑 SQL（node_routing_notify_test.go 守着）。路由门槛在 router_nodes.go 的 registerRouteGroupRoutes

package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// notifyRoutingNodes 在事务提交之后通知 generation 被推进的节点；尽力而为，节点下次拉取同样拿到新版。
func (h *handlers) notifyRoutingNodes(r *http.Request, nodeIDs []string) {
	tenantID := httpx.TenantIDFrom(r.Context())
	for _, id := range nodeIDs {
		h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)
	}
}

func (h *handlers) listRouteGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.d.Node.ListRouteGroups(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listRouteGroupsResponse{Groups: groups})
}

type listRouteGroupsResponse struct {
	Groups []nodefabric.RouteGroup `json:"groups"`
}

type routeGroupCreateReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
}

func (h *handlers) createRouteGroup(w http.ResponseWriter, r *http.Request) {
	var req routeGroupCreateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.CreateRouteGroup(r.Context(), nodefabric.CreateRouteGroupInput{
		TenantID: httpx.TenantIDFrom(r.Context()), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		Name: req.Name, Description: req.Description, SortOrder: req.SortOrder,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type routeGroupUpdateReq struct {
	RowVersion  int64   `json:"row_version"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	SortOrder   *int    `json:"sort_order"`
}

func (h *handlers) updateRouteGroup(w http.ResponseWriter, r *http.Request) {
	var req routeGroupUpdateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, nodeIDs, err := h.d.Node.UpdateRouteGroup(r.Context(), nodefabric.UpdateRouteGroupInput{
		TenantID: httpx.TenantIDFrom(r.Context()), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		GroupID: chi.URLParam(r, "id"), RowVersion: req.RowVersion,
		Name: req.Name, Description: req.Description, SortOrder: req.SortOrder,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyRoutingNodes(r, nodeIDs)
	httpx.OK(w, updateRouteGroupResponse{Group: out, AffectedNodes: len(nodeIDs)})
}

type updateRouteGroupResponse struct {
	Group         *nodefabric.RouteGroup `json:"group"`
	AffectedNodes int                    `json:"affected_nodes"`
}

type routeGroupVersionReq struct {
	RowVersion int64 `json:"row_version"`
}

func (h *handlers) deleteRouteGroup(w http.ResponseWriter, r *http.Request) {
	var req routeGroupVersionReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	nodeIDs, err := h.d.Node.DeleteRouteGroup(r.Context(), nodefabric.DeleteRouteGroupInput{
		TenantID: httpx.TenantIDFrom(r.Context()), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		GroupID: chi.URLParam(r, "id"), RowVersion: req.RowVersion,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyRoutingNodes(r, nodeIDs)
	httpx.OK(w, deleteRouteGroupResponse{Deleted: true, AffectedNodes: len(nodeIDs)})
}

type deleteRouteGroupResponse struct {
	Deleted       bool `json:"deleted"`
	AffectedNodes int  `json:"affected_nodes"`
}

func (h *handlers) getRouteGroupRouting(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.GetGroupRouting(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) setRouteGroupRouting(w http.ResponseWriter, r *http.Request) {
	var req routingPayload
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	res, err := h.d.Node.SetGroupRouting(r.Context(), nodefabric.SetGroupRoutingInput{
		TenantID: httpx.TenantIDFrom(r.Context()), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		GroupID: chi.URLParam(r, "id"), RowVersion: req.RowVersion, Outbounds: req.Outbounds, Routes: req.Routes,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyRoutingNodes(r, res.NodeIDs)
	httpx.OK(w, routeGroupWriteResponse{OK: true, RowVersion: res.RowVersion, AffectedNodes: len(res.NodeIDs)})
}

// routeGroupWriteResponse 是组内路由与组侧成员两个写接口共用的同形响应。
type routeGroupWriteResponse struct {
	OK            bool  `json:"ok"`
	RowVersion    int64 `json:"row_version"`
	AffectedNodes int   `json:"affected_nodes"`
}

type routeGroupMembersReq struct {
	RowVersion int64    `json:"row_version"`
	NodeIDs    []string `json:"node_ids"`
}

func (h *handlers) setRouteGroupMembers(w http.ResponseWriter, r *http.Request) {
	var req routeGroupMembersReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	res, err := h.d.Node.SetRouteGroupMembers(r.Context(), nodefabric.SetRouteGroupMembersInput{
		TenantID: httpx.TenantIDFrom(r.Context()), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		GroupID: chi.URLParam(r, "id"), RowVersion: req.RowVersion, NodeIDs: req.NodeIDs,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyRoutingNodes(r, res.NodeIDs)
	httpx.OK(w, routeGroupWriteResponse{OK: true, RowVersion: res.RowVersion, AffectedNodes: len(res.NodeIDs)})
}

type nodeRouteGroupsReq struct {
	RowVersion int64    `json:"row_version"`
	GroupIDs   []string `json:"group_ids"`
}

func (h *handlers) setNodeRouteGroups(w http.ResponseWriter, r *http.Request) {
	var req nodeRouteGroupsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	res, err := h.d.Node.SetNodeRouteGroups(r.Context(), nodefabric.SetNodeRouteGroupsInput{
		TenantID: httpx.TenantIDFrom(r.Context()), ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		NodeID: chi.URLParam(r, "id"), RowVersion: req.RowVersion, GroupIDs: req.GroupIDs,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyRoutingNodes(r, res.NodeIDs)
	httpx.OK(w, setNodeRouteGroupsResponse{OK: true, RowVersion: res.RowVersion})
}

type setNodeRouteGroupsResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
}

func (h *handlers) nodeEffectiveRouting(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.PreviewNodeRouting(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
