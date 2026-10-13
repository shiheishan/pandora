package handler

// 本文件归 p5-inventory：服务器的后台接口。

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// inventoryPathID 解析路径里的 :id；不是 uuid 当作不存在。
func inventoryPathID(c *gin.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, errs.New(errs.NotFound)
	}
	return id, nil
}

func (h *Handler) listServers(c *gin.Context) {
	list, err := h.svc.ListServers(c.Request.Context(), service.ServerFilter{Status: c.Query("status"), Query: c.Query("q")})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serversFrom(list))
}

func (h *Handler) getServer(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	s, err := h.svc.GetServer(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serverFrom(s))
}

func (h *Handler) createServer(c *gin.Context) {
	var req serverCreateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	s, err := h.svc.CreateServer(c.Request.Context(), service.ServerInput{
		Name:          req.Name,
		Region:        req.Region,
		Hostname:      req.Hostname,
		PublicIPv4:    req.PublicIPv4,
		PublicIPv6:    req.PublicIPv6,
		PrivateIPv4:   req.PrivateIPv4,
		Architecture:  req.Architecture,
		OSName:        req.OSName,
		CapacityNodes: req.CapacityNodes,
		Notes:         req.Notes,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, serverFrom(s))
}

func (h *Handler) patchServer(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req serverPatchRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	s, err := h.svc.PatchServer(c.Request.Context(), id, service.ServerPatch{
		RowVersion:    req.RowVersion,
		Name:          req.Name,
		Region:        req.Region,
		Hostname:      req.Hostname,
		PublicIPv4:    req.PublicIPv4,
		PublicIPv6:    req.PublicIPv6,
		PrivateIPv4:   req.PrivateIPv4,
		Architecture:  req.Architecture,
		OSName:        req.OSName,
		CapacityNodes: req.CapacityNodes,
		Notes:         req.Notes,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serverFrom(s))
}

func (h *Handler) setServerStatus(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req serverStatusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	s, err := h.svc.SetServerStatus(c.Request.Context(), id, req.RowVersion, req.Status, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serverFrom(s))
}

func (h *Handler) deleteServer(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req serverDeleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.DeleteServer(c.Request.Context(), id, req.RowVersion); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serverDeletedResponse{OK: true, ID: id})
}

func (h *Handler) listServerNodes(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	list, err := h.svc.ListServerNodes(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serverNodesFrom(list))
}
