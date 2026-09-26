// [INPUT]: 依赖 platform 的 db/httpx；ReplyNotifier 由装配注入（notify.Service 实现）
// [OUTPUT]: 对外提供 Service、NewService、ReplyNotifier 与 SetReplyNotifier、原子结果 AtomicResult / AtomicCreateResult / AtomicEscalateResult、Categories、视图 Message / Ticket / TicketOrderRef、TicketOwner
// [POS]: domain/support 的服务骨架：依赖注入、SLA 截止计算、两侧共用的视图与预制响应、工单号生成；用例按角色分在 user_tickets.go（用户侧）、agent_tickets.go（客服侧）、escalation.go（超时升级）、withdraw.go（撤回）、macros.go（快捷回复）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package support 实现工单（OPS-001）。
//
// 两条必须守住的性质：
//
//  1. 内部备注对用户不可见。数据库的 CHECK 只保证「用户身份写不了内部备注」，
//     拦不住「用户读到客服写的内部备注」—— 那是查询层的责任。
//     因此凡是面向用户的读取路径，一律带 internal_note = false 条件，
//     且该条件写在 SQL 里而不是 Go 里过滤：漏写会直接少一个 WHERE 而不是
//     多一条泄露的记录，前者在测试中更容易暴露。
//
//  2. SLA 超时自动升级，不依赖人工盯梢。截止时间在创建工单时按优先级算好，
//     扫描任务只做「找出过期的、置为 escalated」这一件幂等的事。
package support

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type Service struct {
	pool *db.Pool
	// notifier 给提单人排「工单有新回复」。为 nil 时不排（只有不承载客服回复的装配会这样）。
	notifier ReplyNotifier
}

func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

// ReplyNotifier 是客服回复通知提单人的出口，由 notify.Service 实现（R115）。
//
// 放一个接口而不是直接依赖 notify：工单域只需要「在我的事务里排一条」和
// 「提交后催一下」这两件事，模板、渠道与偏好过滤都留在 notify 里。
type ReplyNotifier interface {
	Enqueue(ctx context.Context, tx pgx.Tx, tenantID, userID, code string,
		vars map[string]string, dedupeKey string) (int, error)
	Kick()
}

// SetReplyNotifier 接上客服回复通知。承载后台工单回复的 admin 网关必须调用它。
func (s *Service) SetReplyNotifier(n ReplyNotifier) { s.notifier = n }

// ticketRepliedTemplateCode 是客服回复后通知提单人的模板（00023 / 00050 / 00090 种下
// inapp 与 telegram 两个渠道，类别 service，用户可在偏好里关掉）。
const ticketRepliedTemplateCode = "ticket.replied"

const (
	CreateIdempotencyScope     = "support_ticket_create"
	UserReplyIdempotencyScope  = "support_ticket_user_reply"
	UserCloseIdempotencyScope  = "support_ticket_user_close"
	AgentReplyIdempotencyScope = "admin_ticket_reply"
	AssignIdempotencyScope     = "admin_ticket_assign"
	StatusIdempotencyScope     = "admin_ticket_status"
	EscalateIdempotencyScope   = "admin_ticket_escalate"
)

type AtomicResult struct {
	prepared httpx.PreparedResponse
}

func (r AtomicResult) PreparedResponse() httpx.PreparedResponse { return r.prepared }

type AtomicCreateResult struct {
	Ticket *Ticket
	AtomicResult
}

type AtomicEscalateResult struct {
	Escalated int `json:"escalated"`
	AtomicResult
}

type txSuccess func(pgx.Tx) error

func prepareAtomicSuccess(status int, value any) (httpx.PreparedResponse, error) {
	return httpx.PrepareJSON(status, value)
}

//------------------------------------------------------------------------------
// 分类与 SLA
//------------------------------------------------------------------------------

// Categories 是允许的工单分类。封闭枚举而非自由文本：
// 分类要用于路由、统计和 SLA 策略，自由文本会让这三样都失效。
var Categories = map[string]string{
	"general":      "一般咨询",
	"billing":      "账单与支付",
	"subscription": "订阅与套餐",
	"technical":    "连接与技术",
	"account":      "账号与安全",
	"abuse":        "举报与投诉",
}

// slaHours 按优先级给出「首次响应 / 解决」的小时数。
// 数值来自 PRD 对 SLA 的要求，可按运营能力调整，但必须保持
// 首次响应 < 解决时限，否则升级逻辑会出现两次触发。
var slaHours = map[string][2]int{
	"urgent": {1, 8},
	"high":   {4, 24},
	"normal": {12, 72},
	"low":    {24, 168},
}

func slaDue(priority string, from time.Time) (first, resolution time.Time) {
	h, ok := slaHours[priority]
	if !ok {
		h = slaHours["normal"]
	}
	return from.Add(time.Duration(h[0]) * time.Hour),
		from.Add(time.Duration(h[1]) * time.Hour)
}

//------------------------------------------------------------------------------
// 视图
//------------------------------------------------------------------------------

type Message struct {
	ID         string  `json:"id"`
	AuthorKind string  `json:"author_kind"`
	AuthorName *string `json:"author_name"`
	Body       string  `json:"body"`
	// omitempty：用户端的消息 internal_note 恒为 false，加了这个标记后
	// 该字段在用户响应里完全不出现 —— 不向用户暴露「存在内部备注」这个概念本身。
	// 管理端的备注为 true，仍会正常输出。
	InternalNote bool      `json:"internal_note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Ticket struct {
	ID         string     `json:"id"`
	TicketNo   string     `json:"ticket_no"`
	Subject    string     `json:"subject"`
	Category   string     `json:"category"`
	Priority   string     `json:"priority"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at"`

	// 以下字段只在管理端填充
	UserID           *string    `json:"user_id,omitempty"`
	UserEmail        *string    `json:"user_email,omitempty"`
	AssignedTo       *string    `json:"assigned_to,omitempty"`
	AssigneeEmail    *string    `json:"assignee_email,omitempty"`
	SLAFirstDue      *time.Time `json:"sla_first_response_due,omitempty"`
	SLAResolutionDue *time.Time `json:"sla_resolution_due,omitempty"`
	FirstRespondedAt *time.Time `json:"first_responded_at,omitempty"`
	EscalatedAt      *time.Time `json:"escalated_at,omitempty"`
	// SLABreached 表示首次响应已超时且尚未响应
	SLABreached bool `json:"sla_breached,omitempty"`
	// LastMessageAuthorKind 是最后一条非内部备注消息的作者类型（队列）：为 user 时
	// 前端加粗，免得给每个客服建一张已读表
	LastMessageAuthorKind string `json:"last_message_author_kind,omitempty"`
	// UserActivePlan 是用户 active / trialing 最新订阅的套餐名（详情）：客服不一定
	// 有 iam.user.read，不能再去调用户接口
	UserActivePlan *string `json:"user_active_plan,omitempty"`

	MessageCount int       `json:"message_count"`
	LastReplyAt  time.Time `json:"last_reply_at"`
	Messages     []Message `json:"messages,omitempty"`
	// ClosedReason 分辨「已撤回」与「已关闭」：user_closed / withdrawn / agent_closed，未关闭为 null
	ClosedReason *string `json:"closed_reason"`
	// RelatedOrder 只在详情里填（门户与后台详情头「关联订单」，队列不填）
	RelatedOrder *TicketOrderRef `json:"related_order"`
}

type TicketOrderRef struct {
	ID      string `json:"id"`
	OrderNo string `json:"order_no"`
}

//------------------------------------------------------------------------------

func newTicketNo() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return fmt.Sprintf("TK%s-%s", time.Now().UTC().Format("20060102"), enc), nil
}

// TicketOwner 返回工单归属的用户 ID。
//
// 客服回复之后要把变更推给提问的人，而 handler 那层只有工单 ID。
// 单独一个查询而不是让 ReplyAsAgent 多返回一个值：推送是旁路，
// 不该让它的需要去改动回复本身的接口形态。
func (s *Service) TicketOwner(ctx context.Context, tenantID, ticketID string) (string, error) {
	var userID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT user_id::text FROM tickets WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, ticketID).Scan(&userID)
	})
	return userID, err
}
