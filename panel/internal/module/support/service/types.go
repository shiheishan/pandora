package service

import (
	"time"

	"github.com/google/uuid"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 工单状态。
const (
	StatusOpen         = "open"
	StatusPendingUser  = "pending_user"
	StatusPendingAgent = "pending_agent"
	StatusEscalated    = "escalated"
	StatusResolved     = "resolved"
	StatusClosed       = "closed"
)

// 优先级。
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// 关闭原因。
const (
	ClosedByUser    = "user_closed"
	ClosedWithdrawn = "withdrawn"
	ClosedByAgent   = "agent_closed"
	ClosedAuto      = "auto_closed" // 客服回复后用户太久没回，定时任务按后台设置自动关闭
)

// 消息作者类型。
const (
	AuthorUser   = "user"
	AuthorAgent  = "agent"
	AuthorSystem = "system"
)

// 审计动作与资源类型。
const (
	ActionCreate       = "ticket.create"
	ActionReply        = "ticket.reply"
	ActionInternalNote = "ticket.internal_note"
	ActionClose        = "ticket.close"
	ActionAutoClose    = "ticket.auto_closed"
	ActionWithdraw     = "ticket.withdraw"
	ActionAssign       = "ticket.assign"
	ActionStatusChange = "ticket.status_change"
	ActionSLAEscalate  = "ticket.sla_escalate"
	ActionMacroSaved   = "ticket_macro.saved"
	ActionMacroDeleted = "ticket_macro.deleted"

	ResourceTicket = "ticket"
	ResourceMacro  = "ticket_macro"
)

// 业务上限。
const (
	// MaxOpenTickets 是每个用户同时未结（未解决、未关闭）的工单上限，防止刷单淹没客服队列
	MaxOpenTickets = 5
	// UserListLimit 是门户「我的工单」最多返回的条数
	UserListLimit = 100
	// EscalateBatch 是一轮 SLA 升级最多处理的工单数
	EscalateBatch = 500
	// emailSearchLimit 是队列按邮箱搜索时最多匹配的用户数
	emailSearchLimit = 200
)

// 业务错误。
var (
	ErrTicketNotFound = errs.Newf(errs.NotFound, "工单不存在")
	ErrMacroNotFound  = errs.Newf(errs.NotFound, "快捷回复不存在")
	ErrTooManyOpen    = errs.Newf(errs.Conflict, "您有 5 个进行中的工单，请先处理完再提交新的")
	ErrReplyClosed    = errs.Newf(errs.Conflict, "工单已关闭，如需继续请新建工单")
	ErrAlreadyClosed  = errs.Newf(errs.Conflict, "这个工单已经关闭了")
	ErrAgentReplied   = errs.Newf(errs.Conflict, "客服已经回复过这个工单，不能再撤回。如果问题已解决，请直接关闭")
	ErrStatusConflict = errs.Newf(errs.Conflict, "工单状态已变化，请刷新后重试")
	ErrWaitForAgent   = errs.Newf(errs.Conflict, "请等客服回复后再继续发送")
)

// Category 是一个工单分类。
type Category struct {
	Code string
	Name string
}

// OrderRef 是工单关联的订单。
type OrderRef struct {
	ID      uuid.UUID
	OrderNo string
}

// Ticket 是工单的列表行（门户与后台共用；标「后台」的字段只在后台填）。
type Ticket struct {
	ID           uuid.UUID
	TicketNo     string
	Subject      string
	Category     string
	Priority     string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ResolvedAt   *time.Time
	MessageCount int
	LastReplyAt  time.Time
	ClosedReason *string
	// RelatedOrder 只在详情里填
	RelatedOrder *OrderRef

	// 后台
	UserID              uuid.UUID
	UserEmail           string
	AssignedTo          *uuid.UUID
	AssigneeEmail       *string
	SLAFirstResponseDue time.Time
	SLAResolutionDue    time.Time
	FirstRespondedAt    *time.Time
	EscalatedAt         *time.Time
	// SLABreached 首次响应已超时、还没响应、未解决未关闭
	SLABreached bool
	// LastMessageAuthorKind 是最后一条非内部备注消息的作者类型，没有时为空串（只队列填）
	LastMessageAuthorKind string
	// UserActivePlan 是提单人当前生效订阅的套餐名，没有为 nil
	UserActivePlan *string
}

// Message 是一条工单消息。
type Message struct {
	ID         uuid.UUID
	AuthorKind string
	// AuthorName 门户：用户消息是用户自己的显示名，客服与系统为 nil；后台：作者的显示名
	AuthorName   *string
	Body         string
	InternalNote bool
	CreatedAt    time.Time
}

// TicketDetail 是工单详情：列表行加上消息（永不为 nil）。
type TicketDetail struct {
	Ticket
	Messages []Message
	// ReplyBlocked 门户：开了「须等客服回复」且最后一条公开消息是用户自己的，回复框要禁用（只门户详情填）
	ReplyBlocked bool
}

// Assignee 是可指派的客服（可登录的管理员）。
type Assignee struct {
	ID          uuid.UUID
	Email       string
	DisplayName *string
}

// Macro 是一条快捷回复。
type Macro struct {
	ID        uuid.UUID
	Title     string
	Body      string
	SortOrder int
	UpdatedAt time.Time
}

// MacroInput 是新建或覆盖快捷回复的输入。
type MacroInput struct {
	Title     string
	Body      string
	SortOrder int
}

// ReplyFunc 由 handler 提供：把成功响应编码好，service 在业务事务里用它完成幂等认领；nil 时不完成。
type ReplyFunc func() (idemsvc.Response, error)

// CreateInput 是门户建单的输入。Subject 为空时取正文首行，Category 为空时取 general。
type CreateInput struct {
	Subject  string
	Category string
	Body     string
	OrderID  *uuid.UUID
	// Reply 把建好的工单编码成响应（201），nil 时不完成幂等认领
	Reply func(Ticket) (idemsvc.Response, error)
}

// QueueFilter 是后台队列的筛选。
type QueueFilter struct {
	Statuses []string
	Priority string
	Category string
	// AssignedTo 非 nil 时只取指派给他的（uuid.Nil 什么也匹配不到）
	AssignedTo   *uuid.UUID
	Query        string
	OnlyBreached bool
	Limit        int
	Offset       int
}

// 回复来源。
const (
	SourceTelegram = "telegram"
)

// AgentReplyInput 是客服回复或内部备注。
type AgentReplyInput struct {
	TicketID     uuid.UUID
	Body         string
	InternalNote bool
	// Source 是回复来源：空串是后台，"telegram" 是在 Telegram 管理员群组里回复；非空时写进审计 data 的 via
	Source string
	Reply  ReplyFunc
}

// AssignInput 是指派；AssigneeID 为 nil 表示取消指派。
type AssignInput struct {
	TicketID   uuid.UUID
	AssigneeID *uuid.UUID
	Reply      ReplyFunc
}

// StatusInput 是客服改状态；Reason 可空。
type StatusInput struct {
	TicketID uuid.UUID
	Status   string
	Reason   string
	Reply    ReplyFunc
}
