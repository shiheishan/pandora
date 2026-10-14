package handler

// 路由组的增删改查、组内路由与组成员（本文件归 p5-routing）。

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) listRouteGroups(c *gin.Context) {
	groups, err := h.svc.ListRouteGroups(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := routeGroupsResponse{Groups: make([]routeGroupDTO, 0, len(groups))}
	for _, g := range groups {
		out.Groups = append(out.Groups, routeGroupFromService(g))
	}
	httpx.OK(c, out)
}

func (h *Handler) createRouteGroup(c *gin.Context) {
	var req routeGroupCreateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	g, err := h.svc.CreateRouteGroup(c.Request.Context(), service.RouteGroupCreate{
		Name: req.Name, Description: req.Description, SortOrder: req.SortOrder,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, routeGroupFromService(g))
}

func (h *Handler) updateRouteGroup(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req routeGroupUpdateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	g, affected, err := h.svc.UpdateRouteGroup(c.Request.Context(), id, service.RouteGroupPatch{
		RowVersion: req.RowVersion, Name: req.Name, Description: req.Description, SortOrder: req.SortOrder,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, routeGroupUpdatedResponse{Group: routeGroupFromService(g), AffectedNodes: affected})
}

func (h *Handler) deleteRouteGroup(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req routeGroupDeleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	affected, err := h.svc.DeleteRouteGroup(c.Request.Context(), id, req.RowVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, routeGroupDeletedResponse{Deleted: true, AffectedNodes: affected})
}

func (h *Handler) getGroupRouting(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.GetGroupRouting(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, groupRoutingResponse{
		RowVersion: out.RowVersion, Outbounds: outboundsFromService(out.Outbounds), Routes: routesFromService(out.Routes),
	})
}

func (h *Handler) setGroupRouting(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req groupRoutingRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.SetGroupRouting(c.Request.Context(), id, req.RowVersion, routingInputFromDTO(req.Outbounds, req.Routes))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, groupRoutingSavedResponse{OK: true, RowVersion: res.RowVersion, AffectedNodes: res.AffectedNodes})
}

func (h *Handler) setRouteGroupMembers(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req routeGroupMembersRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	nodeIDs, err := parseUUIDList("node_ids", req.NodeIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.SetRouteGroupMembers(c.Request.Context(), id, req.RowVersion, nodeIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, groupMembersSavedResponse{OK: true, RowVersion: res.RowVersion, AffectedNodes: res.AffectedNodes})
}
