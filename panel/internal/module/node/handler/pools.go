package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) listPools(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.svc.ListPools(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, poolListResponseFrom(rows))
}

func (h *Handler) createPool(reauth gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req poolCreateRequest
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}

		var userGroupIDs *[]uuid.UUID
		if req.AllowedUserGroupIDs != nil {
			reauth(c)
			if c.IsAborted() {
				return
			}
			ids, err := parsePoolUserGroupIDs(*req.AllowedUserGroupIDs)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			userGroupIDs = &ids
		}

		ctx := c.Request.Context()
		id, err := h.svc.CreatePool(ctx, service.PoolCreate{
			Code:         strings.TrimSpace(req.Code),
			Name:         strings.TrimSpace(req.Name),
			Region:       strings.TrimSpace(req.Region),
			UserGroupIDs: userGroupIDs,
		})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, poolCreatedResponse{ID: id.String()})
	}
}

func (h *Handler) updatePool(reauth gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		poolID, err := poolPathID(c, "id")
		if err != nil {
			httpx.Fail(c, err)
			return
		}

		var req poolUpdateRequest
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}

		var userGroupIDs *[]uuid.UUID
		if req.AllowedUserGroupIDs != nil {
			reauth(c)
			if c.IsAborted() {
				return
			}
			ids, err := parsePoolUserGroupIDs(*req.AllowedUserGroupIDs)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			userGroupIDs = &ids
		}

		ctx := c.Request.Context()
		if err := h.svc.UpdatePool(ctx, poolID, service.PoolUpdate{
			Name:         strings.TrimSpace(req.Name),
			Region:       strings.TrimSpace(req.Region),
			Status:       strings.TrimSpace(req.Status),
			UserGroupIDs: userGroupIDs,
		}); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, poolOKResponse{OK: true})
	}
}

func (h *Handler) deletePool(c *gin.Context) {
	poolID, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := h.svc.DeletePool(ctx, poolID); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, poolOKResponse{OK: true})
}
