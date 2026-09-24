// [INPUT]: 依赖 platform 的 db/audit/httpx、middleware 的幂等原子完成
// [OUTPUT]: 对外提供 Service、NewService，工单创建、用户侧读写与关闭、客服侧队列 / 回复 / 指派 / 改状态 / 升级，各写操作的 *Atomic 版本
// [POS]: domain/support 的主服务：工单全生命周期；队列 status 接受逗号分隔并回 last_message_author_kind，详情回 user_active_plan，人工升级把优先级提到至少 high；用户与客服两侧都回 closed_reason，用户详情回 related_order；closed_reason 在每条关闭路径上写对（user_closed / withdrawn / agent_closed），重新打开时清空
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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type Service struct{ pool *db.Pool }

func NewService(pool *db.Pool) *Service { return &Service{pool: pool} }

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
	// RelatedOrder 只在详情里填（门户详情头「关联订单」）
	RelatedOrder *TicketOrderRef `json:"related_order"`
}

type TicketOrderRef struct {
	ID      string `json:"id"`
	OrderNo string `json:"order_no"`
}

//------------------------------------------------------------------------------
// 用户侧
//------------------------------------------------------------------------------

type CreateInput struct {
	UserID   string
	Subject  string
	Category string
	Priority string
	Body     string
	OrderID  string
}

const defaultTicketCategory = "general"

// normalizeCreateInput 让所有创建入口共享同一组安全默认值。新用户端只传正文，
// 旧客户端仍可显式提供标题和分类；后者不被覆盖，仍按既有规则校验。
func normalizeCreateInput(in *CreateInput) {
	in.Subject = strings.TrimSpace(in.Subject)
	in.Category = strings.TrimSpace(in.Category)
	in.Body = strings.TrimSpace(in.Body)

	if in.Subject == "" {
		in.Subject = subjectFromBody(in.Body)
	}
	if in.Category == "" {
		in.Category = defaultTicketCategory
	}
}

// subjectFromBody 从正文的首个非空行生成标题。按 rune 截断而非 byte，避免截断
// 中文或 emoji；短标题加稳定前缀以满足既有的最小长度约束。
func subjectFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		runes := []rune(line)
		if len(runes) > 120 {
			runes = runes[:120]
		}
		subject := string(runes)
		if len([]rune(subject)) < 4 {
			subject = "用户咨询：" + subject
		}
		return subject
	}
	return ""
}

func (s *Service) Create(ctx context.Context, tenantID string, in CreateInput) (*Ticket, error) {
	return s.create(ctx, tenantID, in, nil)
}

func (s *Service) CreateAtomic(
	ctx context.Context,
	tenantID string,
	in CreateInput,
	claim middleware.IdempotencyClaim,
) (*AtomicCreateResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, in.UserID, CreateIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("create support ticket: %w", err)
	}
	var prepared httpx.PreparedResponse
	ticket, err := s.create(ctx, tenantID, in, func(tx pgx.Tx, ticket *Ticket) error {
		var err error
		prepared, err = prepareAtomicSuccess(http.StatusCreated, ticket)
		if err != nil {
			return err
		}
		return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
	})
	if err != nil {
		return nil, err
	}
	return &AtomicCreateResult{Ticket: ticket, AtomicResult: AtomicResult{prepared: prepared}}, nil
}

func (s *Service) create(
	ctx context.Context,
	tenantID string,
	in CreateInput,
	complete func(pgx.Tx, *Ticket) error,
) (*Ticket, error) {
	normalizeCreateInput(&in)

	fields := map[string]string{}
	if n := len([]rune(in.Subject)); n < 4 || n > 120 {
		fields["subject"] = "标题需在 4–120 字之间"
	}
	if n := len([]rune(in.Body)); n < 10 || n > 5000 {
		fields["body"] = "内容需在 10–5000 字之间"
	}
	if _, ok := Categories[in.Category]; !ok {
		fields["category"] = "无效的分类"
	}
	// 用户不能自定优先级：谁都会选 urgent，那样 SLA 就没有意义了。
	// 优先级由分类推导，客服可后续调整。
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}

	priority := "normal"
	switch in.Category {
	case "billing", "account":
		priority = "high"
	case "abuse":
		priority = "urgent"
	}

	now := time.Now()
	firstDue, resDue := slaDue(priority, now)

	var t Ticket
	scope := db.Scope{TenantID: tenantID, ActorID: in.UserID}

	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		// Serialize the per-account open-ticket limit across distinct keys and
		// concurrent application replicas.
		var lockedUserID string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM users
			 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`,
			tenantID, in.UserID).Scan(&lockedUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		// 未结工单上限：防止刷单把客服队列淹掉
		var open int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM tickets
			 WHERE tenant_id = $1 AND user_id = $2
			   AND status NOT IN ('resolved', 'closed')`,
			tenantID, in.UserID).Scan(&open); err != nil {
			return err
		}
		if open >= 5 {
			return httpx.New(httpx.CodeConflict,
				"您有 5 个进行中的工单，请先处理完再提交新的")
		}

		no, err := newTicketNo()
		if err != nil {
			return err
		}

		// 关联订单必须属于本人，否则就成了探测他人订单的通道
		var orderID *string
		if in.OrderID != "" {
			var owner string
			err := tx.QueryRow(ctx,
				`SELECT user_id FROM orders WHERE tenant_id = $1 AND id = $2`,
				tenantID, in.OrderID).Scan(&owner)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != in.UserID) {
				return httpx.Invalid(map[string]string{"order_id": "订单不存在"})
			}
			if err != nil {
				return err
			}
			orderID = &in.OrderID
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO tickets
				(tenant_id, ticket_no, user_id, subject, category, priority, status,
				 sla_first_response_due, sla_resolution_due, related_order_id)
			VALUES ($1, $2, $3, $4, $5, $6, 'open', $7, $8, $9)
			RETURNING id, ticket_no, subject, category, priority, status,
			          created_at, updated_at`,
			tenantID, no, in.UserID, in.Subject, in.Category, priority,
			firstDue, resDue, orderID,
		).Scan(&t.ID, &t.TicketNo, &t.Subject, &t.Category, &t.Priority,
			&t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_messages (tenant_id, ticket_id, author_id, author_kind, body)
			VALUES ($1, $2, $3, 'user', $4)`,
			tenantID, t.ID, in.UserID, in.Body); err != nil {
			return err
		}

		t.MessageCount = 1
		t.LastReplyAt = t.CreatedAt

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &in.UserID,
			Action: "ticket.create", ResourceType: "ticket", ResourceID: &t.ID,
			APIDomain: "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"ticket_no": t.TicketNo, "category": t.Category, "priority": t.Priority,
			},
		}); err != nil {
			return err
		}
		if err := plugin.EmitTicketCreated(ctx, tx, tenantID, t.ID, t.TicketNo,
			in.UserID, t.Category, t.Priority, t.Subject); err != nil {
			return err
		}
		if complete != nil {
			return complete(tx, &t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListForUser 返回该用户的工单列表（不含消息正文）。
func (s *Service) ListForUser(ctx context.Context, tenantID, userID string) ([]Ticket, error) {
	out := []Ticket{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.id, t.ticket_no, t.subject, t.category, t.priority, t.status,
			       t.created_at, t.updated_at, t.resolved_at,
			       count(m.id)::int,
			       coalesce(max(m.created_at), t.created_at), t.closed_reason
			  FROM tickets t
			  LEFT JOIN ticket_messages m
			         ON m.ticket_id = t.id AND m.internal_note = false
			 WHERE t.tenant_id = $1 AND t.user_id = $2
			 GROUP BY t.id
			 ORDER BY t.updated_at DESC
			 LIMIT 100`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Ticket
			if err := rows.Scan(&t.ID, &t.TicketNo, &t.Subject, &t.Category,
				&t.Priority, &t.Status, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt,
				&t.MessageCount, &t.LastReplyAt, &t.ClosedReason); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// GetForUser 返回工单详情。内部备注在 SQL 层就被排除。
func (s *Service) GetForUser(ctx context.Context, tenantID, userID, ticketID string) (*Ticket, error) {
	// 非 uuid 与不存在同样 404：交给 SQL 会变成 500
	if _, err := uuid.Parse(ticketID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var t Ticket
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var orderID, orderNo *string
		err := tx.QueryRow(ctx, `
			SELECT t.id, t.ticket_no, t.subject, t.category, t.priority, t.status,
			       t.created_at, t.updated_at, t.resolved_at, t.closed_reason,
			       o.id::text, o.order_no
			  FROM tickets t
			  LEFT JOIN orders o ON o.tenant_id = t.tenant_id AND o.id = t.related_order_id
			 WHERE t.tenant_id = $1 AND t.id = $2 AND t.user_id = $3`,
			tenantID, ticketID, userID,
		).Scan(&t.ID, &t.TicketNo, &t.Subject, &t.Category, &t.Priority,
			&t.Status, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt, &t.ClosedReason,
			&orderID, &orderNo)
		if orderID != nil {
			t.RelatedOrder = &TicketOrderRef{ID: *orderID, OrderNo: *orderNo}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// 不存在与不属于本人返回同一种错误，避免用工单 ID 探测他人工单
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		// internal_note = false 写在 SQL 里，不在 Go 里过滤
		rows, err := tx.Query(ctx, `
			SELECT m.id, m.author_kind, u.display_name, m.body, m.created_at
			  FROM ticket_messages m
			  LEFT JOIN users u ON u.id = m.author_id
			 WHERE m.ticket_id = $1 AND m.internal_note = false
			 ORDER BY m.created_at`, ticketID)
		if err != nil {
			return err
		}
		defer rows.Close()
		t.Messages = []Message{}
		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.ID, &m.AuthorKind, &m.AuthorName,
				&m.Body, &m.CreatedAt); err != nil {
				return err
			}
			// 客服的真实姓名不暴露给用户
			if m.AuthorKind != "user" {
				m.AuthorName = nil
			}
			t.Messages = append(t.Messages, m)
		}
		t.MessageCount = len(t.Messages)
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ReplyAsUser 追加用户回复，并把工单推回待客服处理。
func (s *Service) ReplyAsUser(ctx context.Context, tenantID, userID, ticketID, body string) error {
	return s.replyAsUser(ctx, tenantID, userID, ticketID, body, nil)
}

func (s *Service) ReplyAsUserAtomic(
	ctx context.Context,
	tenantID, userID, ticketID, body string,
	claim middleware.IdempotencyClaim,
) (AtomicResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, userID, UserReplyIdempotencyScope,
	); err != nil {
		return AtomicResult{}, fmt.Errorf("reply to support ticket: %w", err)
	}
	prepared, err := prepareAtomicSuccess(http.StatusOK, map[string]any{"ok": true})
	if err != nil {
		return AtomicResult{}, err
	}
	err = s.replyAsUser(ctx, tenantID, userID, ticketID, body, func(tx pgx.Tx) error {
		return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
	})
	return AtomicResult{prepared: prepared}, err
}

func (s *Service) replyAsUser(
	ctx context.Context,
	tenantID, userID, ticketID, body string,
	complete txSuccess,
) error {
	body = strings.TrimSpace(body)
	if n := len([]rune(body)); n < 1 || n > 5000 {
		return httpx.Invalid(map[string]string{"body": "内容需在 1–5000 字之间"})
	}

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx,
			`SELECT status FROM tickets
			  WHERE tenant_id = $1 AND id = $2 AND user_id = $3 FOR UPDATE`,
			tenantID, ticketID, userID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if status == "closed" {
			return httpx.New(httpx.CodeConflict, "工单已关闭，如需继续请新建工单")
		}

		var messageID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO ticket_messages (tenant_id, ticket_id, author_id, author_kind, body)
			VALUES ($1, $2, $3, 'user', $4)
			RETURNING id`,
			tenantID, ticketID, userID, body).Scan(&messageID); err != nil {
			return err
		}

		// 用户回复后球回到客服；已解决的工单被追问则重新打开
		_, err = tx.Exec(ctx, `
			UPDATE tickets
			   SET status = 'pending_agent',
			       resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END
			 WHERE id = $1`, ticketID)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "ticket.reply", ResourceType: "ticket", ResourceID: &ticketID,
			APIDomain: "public", Outcome: "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": status},
			AfterDigest: map[string]any{
				"status": "pending_agent", "message_id": messageID,
				"body_runes": len([]rune(body)),
			},
		}); err != nil {
			return err
		}
		if complete != nil {
			return complete(tx)
		}
		return nil
	})
}

// CloseByUser 允许用户主动关闭自己的工单。
func (s *Service) CloseByUser(ctx context.Context, tenantID, userID, ticketID string) error {
	return s.closeByUser(ctx, tenantID, userID, ticketID, nil)
}

func (s *Service) CloseByUserAtomic(
	ctx context.Context,
	tenantID, userID, ticketID string,
	claim middleware.IdempotencyClaim,
) (AtomicResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, userID, UserCloseIdempotencyScope,
	); err != nil {
		return AtomicResult{}, fmt.Errorf("close support ticket: %w", err)
	}
	prepared, err := prepareAtomicSuccess(http.StatusOK, map[string]any{"ok": true})
	if err != nil {
		return AtomicResult{}, err
	}
	err = s.closeByUser(ctx, tenantID, userID, ticketID, func(tx pgx.Tx) error {
		return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
	})
	return AtomicResult{prepared: prepared}, err
}

func (s *Service) closeByUser(
	ctx context.Context,
	tenantID, userID, ticketID string,
	complete txSuccess,
) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var before string
		if err := tx.QueryRow(ctx, `
			SELECT status FROM tickets
			 WHERE tenant_id = $1 AND id = $2 AND user_id = $3 FOR UPDATE`,
			tenantID, ticketID, userID).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if before == "closed" {
			return httpx.NotFoundOrForbidden()
		}
		// closed_reason 区分「用户关闭 / 撤回 / 客服关闭」，门户与解决率统计
		// 都靠它；原先这里不写，用户关闭的工单与撤回的分不出来（缺陷 17）
		ct, err := tx.Exec(ctx, `
			UPDATE tickets SET status = 'closed', closed_at = now(),
			       closed_reason = 'user_closed', closed_note = NULL
			 WHERE tenant_id = $1 AND id = $2 AND user_id = $3
			   AND status NOT IN ('closed')`,
			tenantID, ticketID, userID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_messages (tenant_id, ticket_id, author_kind, body)
			VALUES ($1, $2, 'system', '用户已关闭此工单')`,
			tenantID, ticketID); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "ticket.close", ResourceType: "ticket", ResourceID: &ticketID,
			APIDomain: "public", Outcome: "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": before},
			AfterDigest:  map[string]any{"status": "closed", "closed_reason": "user_closed"},
		}); err != nil {
			return err
		}
		if complete != nil {
			return complete(tx)
		}
		return nil
	})
}

//------------------------------------------------------------------------------
// 客服侧
//------------------------------------------------------------------------------

type ListFilter struct {
	Status     string
	Priority   string
	Category   string
	AssignedTo string
	Query      string
	OnlyBreach bool
	Limit      int
	Offset     int
}

// Assignee 是可安全指派工单的客服候选人。这里只暴露客服工作台需要的
// 最小身份字段，不复用普通用户目录，避免把没有工单权限的用户泄露给后台。
type Assignee struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name,omitempty"`
}

// ListEligibleAssignees 返回当前租户内仍有效、且拥有工单写权限的客服。
// 权限绑定在查询时检查，避免把过期或已停用账号作为可选负责人。
func (s *Service) ListEligibleAssignees(ctx context.Context, tenantID string) ([]Assignee, error) {
	out := []Assignee{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT u.id::text, u.email::text, u.display_name
			  FROM users u
			  JOIN role_bindings rb
			    ON rb.tenant_id = u.tenant_id AND rb.user_id = u.id
			  JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
			  JOIN role_permissions rp ON rp.role_id = rb.role_id
			 WHERE u.tenant_id = $1
			   AND u.status = 'active'
			   AND rp.permission_code = 'ops.ticket.write'
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			 ORDER BY u.email::text, u.id::text`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Assignee
			if err := rows.Scan(&item.ID, &item.Email, &item.DisplayName); err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) ListForAgent(ctx context.Context, tenantID string, f ListFilter) ([]Ticket, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 25
	}
	out := []Ticket{}
	var total int

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 队列排序：升级的最前，然后按优先级、再按创建时间。
		// 不用 updated_at 排序 —— 那会让频繁往返的工单一直霸占队首。
		where := `t.tenant_id = $1
			AND ($2 = '' OR t.status = ANY(string_to_array(replace($2, ' ', ''), ',')))
			AND ($3 = '' OR t.priority = $3)
			AND ($4 = '' OR t.category = $4)
			AND ($5 = '' OR t.assigned_to::text = $5)
			AND ($6 = '' OR t.ticket_no ILIKE '%'||$6||'%' OR t.subject ILIKE '%'||$6||'%'
			     OR u.email::text ILIKE '%'||$6||'%')
			AND (NOT $7 OR (t.first_responded_at IS NULL
			                AND t.sla_first_response_due < now()
			                AND t.status NOT IN ('resolved','closed')))`

		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM tickets t
			  JOIN users u ON u.id = t.user_id
			 WHERE `+where,
			tenantID, f.Status, f.Priority, f.Category, f.AssignedTo, f.Query, f.OnlyBreach,
		).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT t.id, t.ticket_no, t.subject, t.category, t.priority, t.status,
			       t.created_at, t.updated_at, t.resolved_at,
			       t.user_id, u.email::text,
			       t.assigned_to, a.email::text,
			       t.sla_first_response_due, t.sla_resolution_due,
			       t.first_responded_at, t.escalated_at,
			       (t.first_responded_at IS NULL AND t.sla_first_response_due < now()
			        AND t.status NOT IN ('resolved','closed')) AS breached,
			       (SELECT count(*)::int FROM ticket_messages m WHERE m.ticket_id = t.id),
			       (SELECT coalesce(max(m.created_at), t.created_at)
			          FROM ticket_messages m WHERE m.ticket_id = t.id),
			       coalesce((SELECT m.author_kind FROM ticket_messages m
			                  WHERE m.ticket_id = t.id AND NOT m.internal_note
			                  ORDER BY m.created_at DESC, m.id DESC LIMIT 1), ''),
			       t.closed_reason
			  FROM tickets t
			  JOIN users u ON u.id = t.user_id
			  LEFT JOIN users a ON a.id = t.assigned_to
			 WHERE `+where+`
			 ORDER BY (t.status = 'escalated') DESC,
			          CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1
			                          WHEN 'normal' THEN 2 ELSE 3 END,
			          t.created_at
			 LIMIT $8 OFFSET $9`,
			tenantID, f.Status, f.Priority, f.Category, f.AssignedTo, f.Query,
			f.OnlyBreach, f.Limit, f.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Ticket
			if err := rows.Scan(&t.ID, &t.TicketNo, &t.Subject, &t.Category,
				&t.Priority, &t.Status, &t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt,
				&t.UserID, &t.UserEmail, &t.AssignedTo, &t.AssigneeEmail,
				&t.SLAFirstDue, &t.SLAResolutionDue, &t.FirstRespondedAt,
				&t.EscalatedAt, &t.SLABreached, &t.MessageCount, &t.LastReplyAt,
				&t.LastMessageAuthorKind, &t.ClosedReason); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetForAgent 返回完整工单，含内部备注。
func (s *Service) GetForAgent(ctx context.Context, tenantID, ticketID string) (*Ticket, error) {
	var t Ticket
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT t.id, t.ticket_no, t.subject, t.category, t.priority, t.status,
			       t.created_at, t.updated_at, t.resolved_at,
			       t.user_id, u.email::text, t.assigned_to, a.email::text,
			       t.sla_first_response_due, t.sla_resolution_due,
			       t.first_responded_at, t.escalated_at,
			       (t.first_responded_at IS NULL AND t.sla_first_response_due < now()
			        AND t.status NOT IN ('resolved','closed')),
			       -- 与后台用户列表的 active_plan 同一口径
			       (SELECT pl.name FROM subscriptions s
			          JOIN plans pl ON pl.id = s.plan_id
			         WHERE s.tenant_id = t.tenant_id AND s.user_id = t.user_id
			           AND s.status IN ('active','trialing')
			         ORDER BY s.created_at DESC LIMIT 1),
			       t.closed_reason
			  FROM tickets t
			  JOIN users u ON u.id = t.user_id
			  LEFT JOIN users a ON a.id = t.assigned_to
			 WHERE t.tenant_id = $1 AND t.id = $2`,
			tenantID, ticketID,
		).Scan(&t.ID, &t.TicketNo, &t.Subject, &t.Category, &t.Priority, &t.Status,
			&t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt, &t.UserID, &t.UserEmail,
			&t.AssignedTo, &t.AssigneeEmail, &t.SLAFirstDue, &t.SLAResolutionDue,
			&t.FirstRespondedAt, &t.EscalatedAt, &t.SLABreached, &t.UserActivePlan, &t.ClosedReason)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "工单不存在")
		}
		if err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT m.id, m.author_kind, u.display_name, m.body,
			       m.internal_note, m.created_at
			  FROM ticket_messages m
			  LEFT JOIN users u ON u.id = m.author_id
			 WHERE m.ticket_id = $1
			 ORDER BY m.created_at`, ticketID)
		if err != nil {
			return err
		}
		defer rows.Close()
		t.Messages = []Message{}
		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.ID, &m.AuthorKind, &m.AuthorName, &m.Body,
				&m.InternalNote, &m.CreatedAt); err != nil {
				return err
			}
			t.Messages = append(t.Messages, m)
		}
		t.MessageCount = len(t.Messages)
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

type AgentReplyInput struct {
	AgentID      string
	TicketID     string
	Body         string
	InternalNote bool
}

func (s *Service) ReplyAsAgent(ctx context.Context, tenantID string, in AgentReplyInput) error {
	return s.replyAsAgent(ctx, tenantID, in, nil)
}

func (s *Service) ReplyAsAgentAtomic(
	ctx context.Context,
	tenantID string,
	in AgentReplyInput,
	claim middleware.IdempotencyClaim,
) (AtomicResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, in.AgentID, AgentReplyIdempotencyScope,
	); err != nil {
		return AtomicResult{}, fmt.Errorf("reply to ticket as agent: %w", err)
	}
	prepared, err := prepareAtomicSuccess(http.StatusOK, map[string]any{"ok": true})
	if err != nil {
		return AtomicResult{}, err
	}
	err = s.replyAsAgent(ctx, tenantID, in, func(tx pgx.Tx) error {
		return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
	})
	return AtomicResult{prepared: prepared}, err
}

func (s *Service) replyAsAgent(
	ctx context.Context,
	tenantID string,
	in AgentReplyInput,
	complete txSuccess,
) error {
	in.Body = strings.TrimSpace(in.Body)
	if n := len([]rune(in.Body)); n < 1 || n > 5000 {
		return httpx.Invalid(map[string]string{"body": "内容需在 1–5000 字之间"})
	}

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.AgentID}, func(tx pgx.Tx) error {
		var status string
		var firstResponded *time.Time
		err := tx.QueryRow(ctx,
			`SELECT status, first_responded_at FROM tickets
			  WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			tenantID, in.TicketID).Scan(&status, &firstResponded)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "工单不存在")
		}
		if err != nil {
			return err
		}

		var messageID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO ticket_messages
				(tenant_id, ticket_id, author_id, author_kind, body, internal_note)
			VALUES ($1, $2, $3, 'agent', $4, $5)
			RETURNING id`,
			tenantID, in.TicketID, in.AgentID, in.Body, in.InternalNote).Scan(&messageID); err != nil {
			return err
		}

		// 内部备注不算对用户的响应，既不计首次响应，也不改变工单状态 ——
		// 否则客服写条备注就能把 SLA 计时停掉，指标立刻失真。
		afterStatus := status
		if !in.InternalNote {
			set := `status = 'pending_user', resolved_at = NULL, closed_at = NULL`
			if firstResponded == nil {
				set += `, first_responded_at = now()`
			}
			if _, err := tx.Exec(ctx, `UPDATE tickets SET `+set+` WHERE id = $1`, in.TicketID); err != nil {
				return err
			}
			afterStatus = "pending_user"
		}

		action := "ticket.reply"
		if in.InternalNote {
			action = "ticket.internal_note"
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.AgentID,
			Action: action, ResourceType: "ticket", ResourceID: &in.TicketID,
			APIDomain: "admin", Outcome: "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": status},
			AfterDigest: map[string]any{
				"status": afterStatus, "message_id": messageID,
				"body_runes": len([]rune(in.Body)), "internal_note": in.InternalNote,
			},
		}); err != nil {
			return err
		}
		if complete != nil {
			return complete(tx)
		}
		return nil
	})
}

// Assign 指派或取消指派（agentID 为空表示取消）。
func (s *Service) Assign(ctx context.Context, tenantID, actorID, ticketID, agentID string) error {
	return s.assign(ctx, tenantID, actorID, ticketID, agentID, nil)
}

func (s *Service) AssignAtomic(
	ctx context.Context,
	tenantID, actorID, ticketID, agentID string,
	claim middleware.IdempotencyClaim,
) (AtomicResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, actorID, AssignIdempotencyScope,
	); err != nil {
		return AtomicResult{}, fmt.Errorf("assign ticket: %w", err)
	}
	prepared, err := prepareAtomicSuccess(http.StatusOK, map[string]any{"ok": true})
	if err != nil {
		return AtomicResult{}, err
	}
	err = s.assign(ctx, tenantID, actorID, ticketID, agentID, func(tx pgx.Tx) error {
		return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
	})
	return AtomicResult{prepared: prepared}, err
}

func (s *Service) assign(
	ctx context.Context,
	tenantID, actorID, ticketID, agentID string,
	complete txSuccess,
) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var before *string
		if err := tx.QueryRow(ctx, `
			SELECT assigned_to::text FROM tickets
			 WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, ticketID,
		).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeNotFound, "工单不存在")
			}
			return err
		}
		var target *string
		if agentID != "" {
			// 只能指派给持有工单处理权限的人，否则被指派者根本打不开工单
			var ok bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM role_bindings rb
					  JOIN role_permissions rp ON rp.role_id = rb.role_id
					  JOIN users u ON u.tenant_id = rb.tenant_id AND u.id = rb.user_id
					 WHERE rb.tenant_id = $1 AND rb.user_id = $2
					   AND u.status = 'active'
					   AND rp.permission_code = 'ops.ticket.write'
					   AND (rb.expires_at IS NULL OR rb.expires_at > now()))`,
				tenantID, agentID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return httpx.Invalid(map[string]string{"assigned_to": "该用户没有工单处理权限"})
			}
			target = &agentID
		}

		ct, err := tx.Exec(ctx,
			`UPDATE tickets SET assigned_to = $3 WHERE tenant_id = $1 AND id = $2`,
			tenantID, ticketID, target)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.New(httpx.CodeNotFound, "工单不存在")
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "ticket.assign", ResourceType: "ticket", ResourceID: &ticketID,
			APIDomain: "admin", Outcome: "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"assigned_to": before},
			AfterDigest:  map[string]any{"assigned_to": target},
		}); err != nil {
			return err
		}
		if complete != nil {
			return complete(tx)
		}
		return nil
	})
}

// 客服可执行的状态流转。resolved/closed 之外的状态由回复动作自动驱动，
// 不开放给接口 —— 手工设成 pending_user 却没有回复，用户会一直等下去。
var agentSettableStatus = map[string]bool{
	"resolved": true, "closed": true, "escalated": true, "pending_agent": true,
}

func (s *Service) SetStatus(ctx context.Context, tenantID, actorID, ticketID, status, reason string) error {
	return s.setStatus(ctx, tenantID, actorID, ticketID, status, reason, nil)
}

func (s *Service) SetStatusAtomic(
	ctx context.Context,
	tenantID, actorID, ticketID, status, reason string,
	claim middleware.IdempotencyClaim,
) (AtomicResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, actorID, StatusIdempotencyScope,
	); err != nil {
		return AtomicResult{}, fmt.Errorf("set ticket status: %w", err)
	}
	prepared, err := prepareAtomicSuccess(http.StatusOK, map[string]any{"ok": true})
	if err != nil {
		return AtomicResult{}, err
	}
	err = s.setStatus(ctx, tenantID, actorID, ticketID, status, reason, func(tx pgx.Tx) error {
		return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
	})
	return AtomicResult{prepared: prepared}, err
}

func (s *Service) setStatus(
	ctx context.Context,
	tenantID, actorID, ticketID, status, reason string,
	complete txSuccess,
) error {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	if !agentSettableStatus[status] {
		return httpx.Invalid(map[string]string{"status": "不支持直接设置该状态"})
	}
	if len([]rune(reason)) > 500 {
		return httpx.Invalid(map[string]string{"reason": "原因不能超过 500 个字符"})
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var before string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM tickets WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			tenantID, ticketID).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeNotFound, "工单不存在")
			}
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE tickets
			   SET status = $2,
			       resolved_at = CASE WHEN $2 = 'resolved' THEN coalesce(resolved_at, now())
			                          WHEN $2 = 'closed' THEN resolved_at
			                          ELSE NULL END,
			       closed_at   = CASE WHEN $2 = 'closed' THEN coalesce(closed_at, now()) ELSE NULL END,
			       -- 客服关闭记 agent_closed；已经是关闭态（用户关闭、撤回）时保留原因。
			       -- 重新打开则清空：关闭原因只描述「当前这次关闭」
			       -- （右侧的 status 是更新前的值：从非关闭态关掉一律是 agent_closed，
			       -- 不沿用旧数据里重新打开后残留的原因）
			       closed_reason = CASE WHEN $2 <> 'closed' THEN NULL
			                            WHEN status = 'closed' THEN coalesce(closed_reason, 'agent_closed')
			                            ELSE 'agent_closed' END,
			       closed_note   = CASE WHEN $2 = 'closed' AND status = 'closed' THEN closed_note ELSE NULL END,
			       escalated_at= CASE WHEN $2 = 'escalated' THEN coalesce(escalated_at, now())
			                          ELSE escalated_at END,
			       -- 人工升级至少提到 high（设计「升级后标记为高优先级」）；已是 urgent 不降
			       priority    = CASE WHEN $2 = 'escalated' AND priority IN ('low','normal') THEN 'high'
			                          ELSE priority END
			 WHERE id = $1`, ticketID, status); err != nil {
			return err
		}

		note := "客服将工单状态改为 " + status
		if reason != "" {
			note += "：" + reason
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_messages (tenant_id, ticket_id, author_kind, body)
			VALUES ($1, $2, 'system', $3)`, tenantID, ticketID, note); err != nil {
			return err
		}

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "ticket.status_change", ResourceType: "ticket", ResourceID: &ticketID,
			APIDomain: "admin", Outcome: "success",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": before},
			AfterDigest:  map[string]any{"status": status, "reason": reason},
		}); err != nil {
			return err
		}
		if complete != nil {
			return complete(tx)
		}
		return nil
	})
}

//------------------------------------------------------------------------------
// SLA 升级（OPS-001 验收「超时自动升级」）
//------------------------------------------------------------------------------

// EscalateOverdue 把首次响应超时的工单置为 escalated。
//
// 幂等（XBD-024）：条件里带 status <> 'escalated'，重复运行不会重复升级，
// 也不会覆盖已有的 escalated_at。返回本次升级的条数。
func (s *Service) EscalateOverdue(ctx context.Context, tenantID string) (int, error) {
	// api_domain is the surface whose policy owns the operation; the audit
	// schema intentionally has no separate "system" domain.
	return s.escalateOverdue(ctx, tenantID, "system", nil, "admin", nil)
}

func (s *Service) EscalateOverdueAsAdminAtomic(
	ctx context.Context,
	tenantID, actorID string,
	claim middleware.IdempotencyClaim,
) (AtomicEscalateResult, error) {
	if err := middleware.ValidateIdempotencyClaim(
		claim, tenantID, actorID, EscalateIdempotencyScope,
	); err != nil {
		return AtomicEscalateResult{}, fmt.Errorf("escalate overdue tickets: %w", err)
	}
	var prepared httpx.PreparedResponse
	n, err := s.escalateOverdue(
		ctx, tenantID, "admin", &actorID, "admin",
		func(tx pgx.Tx, count int) error {
			var err error
			prepared, err = prepareAtomicSuccess(http.StatusOK, map[string]any{"escalated": count})
			if err != nil {
				return err
			}
			return middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)
		},
	)
	return AtomicEscalateResult{
		Escalated: n, AtomicResult: AtomicResult{prepared: prepared},
	}, err
}

func (s *Service) escalateOverdue(
	ctx context.Context,
	tenantID, actorKind string,
	actorID *string,
	apiDomain string,
	complete func(pgx.Tx, int) error,
) (int, error) {
	var n int
	scope := db.Scope{TenantID: tenantID}
	if actorID != nil {
		scope.ActorID = *actorID
	}
	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH candidates AS MATERIALIZED (
				SELECT id, status AS before_status, priority AS before_priority
				  FROM tickets
				 WHERE tenant_id = $1
				   AND first_responded_at IS NULL
				   AND sla_first_response_due < now()
				   AND status NOT IN ('resolved', 'closed', 'escalated')
				 ORDER BY sla_first_response_due, id
				 FOR UPDATE
			), updated AS (
				UPDATE tickets AS t
				   SET status = 'escalated', escalated_at = now(),
				       priority = CASE t.priority
				                    WHEN 'low' THEN 'normal'
				                    WHEN 'normal' THEN 'high'
				                    ELSE 'urgent' END
				  FROM candidates AS c
				 WHERE t.id = c.id
				 RETURNING t.id, c.before_status, c.before_priority, t.priority
			)
			SELECT id, before_status, before_priority, priority FROM updated
			 ORDER BY id`, tenantID)
		if err != nil {
			return err
		}
		type escalatedTicket struct {
			id             string
			beforeStatus   string
			beforePriority string
			priority       string
		}
		var tickets []escalatedTicket
		for rows.Next() {
			var ticket escalatedTicket
			if err := rows.Scan(
				&ticket.id, &ticket.beforeStatus, &ticket.beforePriority, &ticket.priority,
			); err != nil {
				rows.Close()
				return err
			}
			tickets = append(tickets, ticket)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		n = len(tickets)

		for _, ticket := range tickets {
			if _, err := tx.Exec(ctx, `
				INSERT INTO ticket_messages (tenant_id, ticket_id, author_kind, body)
				VALUES ($1, $2, 'system', '首次响应已超过 SLA，工单自动升级')`,
				tenantID, ticket.id); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: actorKind, ActorID: actorID,
				Action: "ticket.sla_escalate", ResourceType: "ticket", ResourceID: &ticket.id,
				APIDomain: apiDomain, Outcome: "success",
				RequestID: httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{
					"status": ticket.beforeStatus, "priority": ticket.beforePriority,
				},
				AfterDigest: map[string]any{
					"status": "escalated", "priority": ticket.priority,
				},
			}); err != nil {
				return err
			}
		}
		if complete != nil {
			return complete(tx, n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
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
