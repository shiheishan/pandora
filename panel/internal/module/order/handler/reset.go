package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// createResetOrderRequest 是 POST /me/subscriptions/:id/traffic-reset：只有结账部分（余额、优惠码、报价时刻与期望金额）。
type createResetOrderRequest struct {
	checkoutReq
}

// createResetOrder 门户买流量重置：哪一份由路径给出；金额与 as_of、expect 来自 action=reset 的报价。
func (h *Handler) createResetOrder(c *gin.Context) {
	subID, ok := pathID(c)
	if !ok {
		return
	}
	var req createResetOrderRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	created, err := h.svc.CreateResetOrder(c.Request.Context(), service.ResetOrderInput{
		SubscriptionID: subID,
		Checkout:       req.checkout(),
		Reply:          createdReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeCreated(c, created)
}
