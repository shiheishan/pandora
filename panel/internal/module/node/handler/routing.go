package handler

// 全局与节点私有路由、生效预览、节点所在组。

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) getGlobalRouting(c *gin.Context) {
	out, err := h.svc.GetGlobalRouting(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, globalRoutingResponse{
		Revision: out.Revision, Outbounds: outboundsFromService(out.Outbounds),
		Routes: routesFromService(out.Routes), OnlineNodes: out.OnlineNodes,
	})
}

func (h *Handler) setGlobalRouting(c *gin.Context) {
	var req globalRoutingRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.SetGlobalRouting(c.Request.Context(), req.ExpectedRevision, routingInputFromDTO(req.Outbounds, req.Routes))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, globalRoutingSavedResponse{OK: true, Revision: res.Revision, AffectedNodes: res.AffectedNodes})
}

func (h *Handler) getNodeRouting(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.GetNodeRouting(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeRoutingResponse{
		RowVersion: out.RowVersion, Groups: groupRefsFromService(out.Groups),
		Outbounds: outboundsFromService(out.Outbounds), Routes: routesFromService(out.Routes),
	})
}

func (h *Handler) setNodeRouting(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeRoutingRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	rv, err := h.svc.SetNodeRouting(c.Request.Context(), id, req.RowVersion, routingInputFromDTO(req.Outbounds, req.Routes))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeRoutingSavedResponse{OK: true, RowVersion: rv})
}

func (h *Handler) previewNodeRouting(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.PreviewNodeRouting(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, effectiveFromService(out))
}

func (h *Handler) setNodeRouteGroups(c *gin.Context) {
	id, err := routingPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeRouteGroupsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	groupIDs, err := parseUUIDList("group_ids", req.GroupIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.SetNodeRouteGroups(c.Request.Context(), id, req.RowVersion, groupIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeRouteGroupsSavedResponse{OK: true, RowVersion: res.RowVersion})
}
