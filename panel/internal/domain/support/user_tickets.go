// [INPUT]: 依赖 service.go 的 Service、视图与 prepareAtomicSuccess，依赖 platform 的 db/audit/httpx、middleware 的幂等原子完成
// [OUTPUT]: 对外提供 CreateInput、Service 的 Create / CreateAtomic、ListForUser、GetForUser、ReplyAsUser / ReplyAsUserAtomic、CloseByUser / CloseByUserAtomic
// [POS]: domain/support 的用户侧工单：提单（标题可由正文推出）、只读自己的工单且 SQL 层排除内部备注、回复与关闭（closed_reason=user_closed）；撤回在 withdraw.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"context"
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
