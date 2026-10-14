package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
)

// adminOrders 后台订单列表：q 搜订单号或用户邮箱，status 逗号分隔，user_id、from、to 可选。
func (h *Handler) adminOrders(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, adminListDefault, listMax)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	q := c.Query("q")
	if err := rejectNUL(map[string]string{"q": q, "status": c.Query("status")}); err != nil {
		httpx.Fail(c, err)
		return
	}
	f := service.ListFilter{
		Statuses: splitStatuses(c.Query("status")),
		Q:        strings.TrimSpace(q),
		Limit:    limit,
		Offset:   offset,
	}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"user_id": "格式不正确"}))
			return
		}
		f.UserID = &id
	}
	if f.From, err = parseDay("from", c.Query("from"), false); err != nil {
		httpx.Fail(c, err)
		return
	}
	if f.To, err = parseDay("to", c.Query("to"), true); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.AdminOrders(c.Request.Context(), f)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := adminOrdersFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// adminOrder 后台订单详情。
func (h *Handler) adminOrder(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	res, err := h.svc.AdminOrder(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := adminOrderDetailFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// adminOrderPayments 后台订单的支付尝试、收款（退款恒为空）。
func (h *Handler) adminOrderPayments(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	res, err := h.svc.AdminOrderPayments(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := orderPaymentsFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// adminCancel 后台取消待付订单：带 expected_state_version 与原因。
func (h *Handler) adminCancel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req adminCancelRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.Cancel(c.Request.Context(), service.CancelInput{
		OrderID:              id,
		Admin:                true,
		ExpectedStateVersion: req.ExpectedStateVersion,
		Reason:               req.Reason,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminCancelResponse{AlreadyTerminal: res.AlreadyTerminal, Order: releasedFrom(res)})
}

// markPaid 后台把待付订单按线下已收款结清。
func (h *Handler) markPaid(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req markPaidRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.MarkPaid(c.Request.Context(), service.MarkPaidInput{
		OrderID:   id,
		Reason:    req.Reason,
		Reference: req.Reference,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, markPaidFrom(res))
}

// adminQuery 后台向渠道查单（payment 记操作人审计）。
func (h *Handler) adminQuery(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	res, err := h.svc.AdminQuery(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, queryFrom(res))
}
