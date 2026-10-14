package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) userProfile(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	p, err := h.svc.UserProfile(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, profileFrom(p))
}

func (h *Handler) accessLog(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, 50, 200)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var q accessLogQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	items, err := h.svc.AccessLog(c.Request.Context(), q.toQuery(limit, offset))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, accessLogFrom(items))
}

func (h *Handler) ipClusters(c *gin.Context) {
	includeReviewed := c.Query("include_reviewed") == "1"
	cs, err := h.svc.IPClusters(c.Request.Context(), includeReviewed)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, clustersFrom(cs))
}

func (h *Handler) reviewIPCluster(c *gin.Context) {
	var req reviewClusterRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	key := c.Param("key")
	expires, err := h.svc.ReviewIPCluster(c.Request.Context(), key, req.Note)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, reviewFrom(key, expires))
}

func (h *Handler) disableIPCluster(c *gin.Context) {
	var req disableClusterRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ids, err := parseUserIDs(req.UserIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	r, err := h.svc.DisableIPClusterAccounts(c.Request.Context(), c.Param("key"), ids, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, disableFrom(r))
}
