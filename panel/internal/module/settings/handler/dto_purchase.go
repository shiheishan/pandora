package handler

import (
	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type purchaseLimitsResponse struct {
	MaxUnpaidOrders  int `json:"max_unpaid_orders"`
	MaxSubscriptions int `json:"max_subscriptions"`
}

type setPurchaseLimitsRequest struct {
	MaxUnpaidOrders  *int `json:"max_unpaid_orders"`
	MaxSubscriptions *int `json:"max_subscriptions"`
}

func (h *Handler) getPurchaseLimits(c *gin.Context) {
	ctx := c.Request.Context()
	lim, err := h.svc.PurchaseLimits(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, purchaseLimitsResponse{
		MaxUnpaidOrders:  lim.MaxUnpaidOrders,
		MaxSubscriptions: lim.MaxSubscriptions,
	})
}

func (h *Handler) setPurchaseLimits(c *gin.Context) {
	ctx := c.Request.Context()
	var req setPurchaseLimitsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	fields := map[string]string{}
	if req.MaxUnpaidOrders == nil {
		fields["max_unpaid_orders"] = "必填"
	}
	if req.MaxSubscriptions == nil {
		fields["max_subscriptions"] = "必填"
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}
	v := service.PurchaseLimits{
		MaxUnpaidOrders:  *req.MaxUnpaidOrders,
		MaxSubscriptions: *req.MaxSubscriptions,
	}
	if err := h.svc.SetPurchaseLimits(ctx, v); err != nil {
		httpx.Fail(c, err)
		return
	}
	lim, err := h.svc.PurchaseLimits(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, purchaseLimitsResponse{
		MaxUnpaidOrders:  lim.MaxUnpaidOrders,
		MaxSubscriptions: lim.MaxSubscriptions,
	})
}
