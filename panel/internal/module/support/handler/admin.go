package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
)

// queue 后台工单队列。
func (h *Handler) queue(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, 25, 100)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var statuses []string
	if raw := c.Query("status"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				statuses = append(statuses, p)
			}
		}
	}
	var assignedTo *uuid.UUID
	if raw := strings.TrimSpace(c.Query("assigned_to")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			nilID := uuid.Nil
			assignedTo = &nilID
		} else {
			assignedTo = &id
		}
	}
	f := service.QueueFilter{
		Statuses:     statuses,
		Priority:     c.Query("priority"),
		Category:     c.Query("category"),
		AssignedTo:   assignedTo,
		Query:        strings.TrimSpace(c.Query("q")),
		OnlyBreached: c.Query("breached") == "1",
		Limit:        limit,
		Offset:       offset,
	}
	rows, total, err := h.svc.Queue(c.Request.Context(), f)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := queueResponse{Tickets: make([]adminTicketDTO, 0, len(rows)), Total: total}
	for _, t := range rows {
		out.Tickets = append(out.Tickets, adminTicketFrom(t))
	}
	httpx.OK(c, out)
}

// adminTicket 后台工单详情。
func (h *Handler) adminTicket(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	d, err := h.svc.AdminTicket(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminDetailFrom(d))
}

// assignees 可指派客服列表。
func (h *Handler) assignees(c *gin.Context) {
	rows, err := h.svc.Assignees(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, assigneesFrom(rows))
}

// listMacros 快捷回复列表。
func (h *Handler) listMacros(c *gin.Context) {
	rows, err := h.svc.ListMacros(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, macrosFrom(rows))
}

// agentReply 客服回复或内部备注。
func (h *Handler) agentReply(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	var req agentReplyRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.AgentReply(c.Request.Context(), service.AgentReplyInput{
		TicketID: id, Body: req.Body, InternalNote: req.InternalNote, Reply: okReply,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

// assign 指派或取消指派。
func (h *Handler) assign(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	var req assignRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	assigneeID, err := req.assigneeID()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.Assign(c.Request.Context(), service.AssignInput{
		TicketID: id, AssigneeID: assigneeID, Reply: okReply,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

// setStatus 客服改工单状态。
func (h *Handler) setStatus(c *gin.Context) {
	id, ok := pathID(c, "工单不存在")
	if !ok {
		return
	}
	var req statusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetStatus(c.Request.Context(), service.StatusInput{
		TicketID: id, Status: req.Status, Reason: req.Reason, Reply: okReply,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}

// escalate SLA 超时批量升级。
func (h *Handler) escalate(c *gin.Context) {
	n, err := h.svc.EscalateOverdue(c.Request.Context(), escalateReply)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, escalatedResponse{Escalated: n})
}

// saveMacro 新建或覆盖快捷回复。
func (h *Handler) saveMacro(c *gin.Context) {
	var id *uuid.UUID
	if raw := c.Param("id"); raw != "" {
		parsed, ok := pathID(c, "快捷回复不存在")
		if !ok {
			return
		}
		id = &parsed
	}
	var req macroRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	saved, err := h.svc.SaveMacro(c.Request.Context(), id, service.MacroInput{
		Title: req.Title, Body: req.Body, SortOrder: req.SortOrder,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, macroSavedResponse{ID: saved.String()})
}

// deleteMacro 删除快捷回复。
func (h *Handler) deleteMacro(c *gin.Context) {
	id, ok := pathID(c, "快捷回复不存在")
	if !ok {
		return
	}
	if err := h.svc.DeleteMacro(c.Request.Context(), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
