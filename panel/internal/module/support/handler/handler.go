// Package handler 是 support 模块的 HTTP 层：门户的工单（分类、列表、详情、建单、回复、关闭、撤回）与后台的工单队列、详情、回复、指派、改状态、SLA 升级、快捷回复。
package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// Service 是 handler 用到的 support service 方法；*service.Service 实现它。
type Service interface {
	Categories() []service.Category
	Create(ctx context.Context, in service.CreateInput) (service.Ticket, error)
	MyTickets(ctx context.Context) ([]service.Ticket, error)
	MyTicket(ctx context.Context, id uuid.UUID) (service.TicketDetail, error)
	UserReply(ctx context.Context, id uuid.UUID, body string, reply service.ReplyFunc) error
	UserClose(ctx context.Context, id uuid.UUID, reply service.ReplyFunc) error
	Withdraw(ctx context.Context, id uuid.UUID, reason string) error
	Queue(ctx context.Context, f service.QueueFilter) ([]service.Ticket, int64, error)
	AdminTicket(ctx context.Context, id uuid.UUID) (service.TicketDetail, error)
	Assignees(ctx context.Context) ([]service.Assignee, error)
	AgentReply(ctx context.Context, in service.AgentReplyInput) error
	Assign(ctx context.Context, in service.AssignInput) error
	SetStatus(ctx context.Context, in service.StatusInput) error
	EscalateOverdue(ctx context.Context, reply func(int) (idemsvc.Response, error)) (int, error)
	ListMacros(ctx context.Context) ([]service.Macro, error)
	SaveMacro(ctx context.Context, id *uuid.UUID, in service.MacroInput) (uuid.UUID, error)
	DeleteMacro(ctx context.Context, id uuid.UUID) error
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 幂等作用域（沿用旧名）。
const (
	ScopeCreate     = "support_ticket_create"
	ScopeUserReply  = "support_ticket_user_reply"
	ScopeUserClose  = "support_ticket_user_close"
	ScopeAgentReply = "admin_ticket_reply"
	ScopeAssign     = "admin_ticket_assign"
	ScopeStatus     = "admin_ticket_status"
	ScopeEscalate   = "admin_ticket_escalate"
)

// CreatePerHour 是门户建单按账号每小时上限。
const CreatePerHour = 10

// Handler 是 support 模块的 HTTP 入口。
type Handler struct {
	svc Service
	lim *ratelimit.Limiter
}

// New 构造 Handler；lim 为 nil 时不限流（只用于测试）。
func New(svc Service, lim *ratelimit.Limiter) *Handler { return &Handler{svc: svc, lim: lim} }

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/support/categories", k.RequireAuth, h.categories)
	v1.GET("/support/tickets", k.RequireAuth, h.myTickets)
	v1.GET("/support/tickets/:id", k.RequireAuth, h.myTicket)
	v1.POST("/support/tickets", k.RequireAuth, h.limitCreate(), k.Idempotency(ScopeCreate), h.create)
	v1.POST("/support/tickets/:id/reply", k.RequireAuth, k.Idempotency(ScopeUserReply), h.userReply)
	v1.POST("/support/tickets/:id/close", k.RequireAuth, k.Idempotency(ScopeUserClose), h.userClose)
	v1.POST("/support/tickets/:id/withdraw", k.RequireAuth, h.withdraw)
}

// RegisterAdmin 注册后台路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/tickets/assignees", k.RequireAuth, h.assignees)
	v1.GET("/tickets", k.RequireAuth, h.queue)
	v1.GET("/tickets/:id", k.RequireAuth, h.adminTicket)
	v1.POST("/tickets/escalate", k.RequireAuth, k.Idempotency(ScopeEscalate), h.escalate)
	v1.POST("/tickets/:id/reply", k.RequireAuth, k.Idempotency(ScopeAgentReply), h.agentReply)
	v1.POST("/tickets/:id/assign", k.RequireAuth, k.Idempotency(ScopeAssign), h.assign)
	v1.POST("/tickets/:id/status", k.RequireAuth, k.Idempotency(ScopeStatus), h.setStatus)
	v1.GET("/ticket-macros", k.RequireAuth, h.listMacros)
	v1.POST("/ticket-macros", k.RequireAuth, h.saveMacro)
	v1.POST("/ticket-macros/:id", k.RequireAuth, h.saveMacro)
	v1.DELETE("/ticket-macros/:id", k.RequireAuth, h.deleteMacro)
}

// limitCreate 是按登录账号的建单限流；排在 RequireAuth 之后。
func (h *Handler) limitCreate() gin.HandlerFunc {
	rule := ratelimit.PerWindow("ticket_create", CreatePerHour, time.Hour)
	return func(c *gin.Context) {
		if h.lim == nil {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		p, ok := authn.From(ctx)
		if !ok {
			httpx.Fail(c, errs.New(errs.Unauthorized))
			return
		}
		if err := h.lim.Allow(ctx, rule, p.UserID.String()); err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Next()
	}
}

// pathID 解析路径里的 :id；格式不对按不存在处理。
func pathID(c *gin.Context, msg string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.Newf(errs.NotFound, msg))
		return uuid.Nil, false
	}
	return id, true
}
