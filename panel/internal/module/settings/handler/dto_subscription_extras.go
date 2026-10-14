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
}

type subscriptionExtrasResponse struct {
	InfoNodes      bool     `json:"info_nodes"`
	ProtocolPrefix bool     `json:"protocol_prefix"`
	LinkBaseURLs   []string `json:"link_base_urls"`
}

type setSubscriptionExtrasRequest struct {
	InfoNodes      *bool     `json:"info_nodes"`
	ProtocolPrefix *bool     `json:"protocol_prefix"`
	LinkBaseURLs   *[]string `json:"link_base_urls"`
}

func subscriptionExtrasToResponse(e service.SubscriptionExtras) subscriptionExtrasResponse {
	urls := e.LinkBaseURLs
	if urls == nil {
		urls = []string{}
	}
	return subscriptionExtrasResponse{
		InfoNodes:      e.InfoNodes,
		ProtocolPrefix: e.ProtocolPrefix,
		LinkBaseURLs:   urls,
	}
}

func (h *Handler) getSubscriptionExtras(c *gin.Context) {
	ctx := c.Request.Context()
	e, err := h.svc.SubscriptionExtras(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, subscriptionExtrasToResponse(e))
}

func (h *Handler) setSubscriptionExtras(c *gin.Context) {
	ctx := c.Request.Context()
	var req setSubscriptionExtrasRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetSubscriptionExtras(ctx, service.SubscriptionExtrasUpdate{
		InfoNodes:      req.InfoNodes,
		ProtocolPrefix: req.ProtocolPrefix,
		LinkBaseURLs:   req.LinkBaseURLs,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	e, err := h.svc.SubscriptionExtras(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, subscriptionExtrasToResponse(e))
}
