package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
)

func registerTicketRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 工单（OPS-001）---
	// 读写分权：ops.ticket.read 能看队列，处理与指派要 ops.ticket.write
	r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
		Get("/tickets/assignees", h.ticketAssignees)
	r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
		Get("/tickets", h.ticketQueue)
	r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
		Get("/tickets/{id}", h.ticketDetail)
	r.With(
		middleware.RequirePermission("ops.ticket.write", d.Log),
		middleware.Idempotency(d.Pool, support.AgentReplyIdempotencyScope, d.Log),
	).
		Post("/tickets/{id}/reply", h.ticketReply)
	r.With(
		middleware.RequirePermission("ops.ticket.write", d.Log),
		middleware.Idempotency(d.Pool, support.AssignIdempotencyScope, d.Log),
	).
		Post("/tickets/{id}/assign", h.ticketAssign)
	r.With(
		middleware.RequirePermission("ops.ticket.write", d.Log),
		middleware.Idempotency(d.Pool, support.StatusIdempotencyScope, d.Log),
	).
		Post("/tickets/{id}/status", h.ticketStatus)
	r.With(
		middleware.RequirePermission("ops.ticket.write", d.Log),
		middleware.Idempotency(d.Pool, support.EscalateIdempotencyScope, d.Log),
	).
		Post("/tickets/escalate", h.ticketEscalate)
	// 快捷回复：独立资源路径（不挂在 tickets/ 下），配置类写操作不带幂等，
	// 同 user-groups 的惯例
	r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
		Get("/ticket-macros", h.listTicketMacros)
	r.With(middleware.RequirePermission("ops.ticket.write", d.Log)).
		Post("/ticket-macros", h.saveTicketMacro)
	r.With(middleware.RequirePermission("ops.ticket.write", d.Log)).
		Post("/ticket-macros/{id}", h.saveTicketMacro)
	r.With(middleware.RequirePermission("ops.ticket.write", d.Log)).
		Delete("/ticket-macros/{id}", h.deleteTicketMacro)
}
