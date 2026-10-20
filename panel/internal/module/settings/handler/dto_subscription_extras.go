package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

// SubscriptionExtrasSettings 是订阅附加设置用到的 service 方法。
type SubscriptionExtrasSettings interface {
	SubscriptionExtras(ctx context.Context) (service.SubscriptionExtras, error)
	SetSubscriptionExtras(ctx context.Context, u service.SubscriptionExtrasUpdate) error
	SubscriptionFetchLimit(ctx context.Context) (int, error)
}

type subscriptionExtrasResponse struct {
	InfoNodes         bool `json:"info_nodes"`
	ProtocolPrefix    bool `json:"protocol_prefix"`
	FetchLimitPerHour int  `json:"fetch_limit_per_hour"`
}

type setSubscriptionExtrasRequest struct {
	InfoNodes         *bool `json:"info_nodes"`
	ProtocolPrefix    *bool `json:"protocol_prefix"`
	FetchLimitPerHour *int  `json:"fetch_limit_per_hour"`
}

func subscriptionExtrasToResponse(e service.SubscriptionExtras, fetchLimit int) subscriptionExtrasResponse {
	return subscriptionExtrasResponse{
		InfoNodes:         e.InfoNodes,
		ProtocolPrefix:    e.ProtocolPrefix,
		FetchLimitPerHour: fetchLimit,
	}
}

func (h *Handler) getSubscriptionExtras(c *gin.Context) {
	ctx := c.Request.Context()
	e, err := h.svc.SubscriptionExtras(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	fetchLimit, err := h.svc.SubscriptionFetchLimit(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, subscriptionExtrasToResponse(e, fetchLimit))
}

func (h *Handler) setSubscriptionExtras(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSubscriptionExtrasRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetSubscriptionExtras(ctx, service.SubscriptionExtrasUpdate{
		InfoNodes:         req.InfoNodes,
		ProtocolPrefix:    req.ProtocolPrefix,
		FetchLimitPerHour: req.FetchLimitPerHour,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	e, err := h.svc.SubscriptionExtras(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	fetchLimit, err := h.svc.SubscriptionFetchLimit(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, subscriptionExtrasToResponse(e, fetchLimit))
}
