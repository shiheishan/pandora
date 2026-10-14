package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/giftcard/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// preview 门户兑换前看卡面与落点选项。
func (h *Handler) preview(c *gin.Context) {
	var req codeRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	p, err := h.svc.Preview(c.Request.Context(), req.Code)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := previewFrom(p)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// redeem 门户兑换。
func (h *Handler) redeem(c *gin.Context) {
	var req codeRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	choice, err := req.choice()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.Redeem(c.Request.Context(), service.RedeemInput{Code: req.Code, Choice: choice, Reply: redeemReply})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := redeemFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// myRedemptions 门户「我的兑换记录」。
func (h *Handler) myRedemptions(c *gin.Context) {
	rows, err := h.svc.MyRedemptions(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := myRedemptionsFrom(rows)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}
