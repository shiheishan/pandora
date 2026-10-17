package handler

// 节点中转的后台接口。

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) listNodeRelays(c *gin.Context) {
	parentID, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	list, err := h.svc.ListNodeRelays(c.Request.Context(), parentID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, relaysFrom(list))
}

func (h *Handler) createNodeRelay(c *gin.Context) {
	parentID, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeRelayCreateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	relay, err := h.svc.CreateNodeRelay(c.Request.Context(), parentID, service.NodeRelayInput{
		Name:       req.Name,
		ServerHost: req.ServerHost,
		ServerPort: req.ServerPort,
		SNI:        req.SNI,
		HostHeader: req.HostHeader,
		Enabled:    req.Enabled,
		SortOrder:  req.SortOrder,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, relayFrom(relay))
}

func (h *Handler) patchNodeRelay(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeRelayPatchRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	relay, err := h.svc.PatchNodeRelay(c.Request.Context(), id, service.NodeRelayPatch{
		RowVersion: req.RowVersion,
		Name:       req.Name,
		ServerHost: req.ServerHost,
		ServerPort: req.ServerPort,
		SNI:        service.OptionalString{Set: req.SNI.Set, Value: req.SNI.Value},
		HostHeader: service.OptionalString{Set: req.HostHeader.Set, Value: req.HostHeader.Value},
		Enabled:    req.Enabled,
		SortOrder:  req.SortOrder,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, relayFrom(relay))
}

func (h *Handler) deleteNodeRelay(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeRelayDeleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.DeleteNodeRelay(c.Request.Context(), id, req.RowVersion); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeDeletedResponse{Deleted: true})
}
