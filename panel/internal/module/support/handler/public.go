package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// categories 门户工单分类。
func (h *Handler) categories(c *gin.Context) {
	httpx.OK(c, categoriesFrom(h.svc.Categories()))
}

// myTickets 门户「我的工单」。
func (h *Handler) myTickets(c *gin.Context) {
	rows, err := h.svc.MyTickets(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := myTicketsResponse{Tickets: make([]ticketRowDTO, 0, len(rows))}
	for _, t := range rows {
		out.Tickets = append(out.Tickets, ticketRowFrom(t))
	}
	httpx.OK(c, out)
}

// myTicket 门户工单详情。
func (h *Handler) myTicket(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	d, err := h.svc.MyTicket(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, portalDetailFrom(d))
}

// create 门户建单。
func (h *Handler) create(c *gin.Context) {
	var req createRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	orderID, err := req.orderID()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.Create(c.Request.Context(), service.CreateInput{
		Subject: req.Subject, Category: req.Category, Body: req.Body, OrderID: orderID, Reply: createReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, ticketRowFrom(t))
}

// userReply 门户回复。
func (h *Handler) userReply(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	var req bodyRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.UserReply(c.Request.Context(), id, req.Body, okReply); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

// userClose 门户关闭工单（不读请求体）。
func (h *Handler) userClose(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	if err := h.svc.UserClose(c.Request.Context(), id, okReply); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

// withdraw 门户撤回工单。
func (h *Handler) withdraw(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	var req withdrawRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.Withdraw(c.Request.Context(), id, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, withdrawnResponse{Withdrawn: true})
}
