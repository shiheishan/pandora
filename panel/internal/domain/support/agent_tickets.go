// [INPUT]: 依赖 service.go 的 Service、视图、ReplyNotifier 与 prepareAtomicSuccess，依赖 platform 的 db/audit/httpx、middleware 的幂等原子完成
// [OUTPUT]: 对外提供 ListFilter、Assignee、AgentReplyInput、Service 的 ListEligibleAssignees、ListForAgent、GetForAgent、ReplyAsAgent / ReplyAsAgentAtomic、Assign / AssignAtomic、SetStatus / SetStatusAtomic
// [POS]: domain/support 的客服侧工单：负责人目录（查询时校验权限绑定）、队列与详情（R75 同口径计数）、回复与内部备注（非内部回复同事务排 ticket.replied，R115）、指派、改状态（closed_reason=agent_closed，重开清空）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
		var orderID, orderNo *string
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
			       t.closed_reason, o.id::text, o.order_no
			  FROM tickets t
			  JOIN users u ON u.id = t.user_id
			  LEFT JOIN users a ON a.id = t.assigned_to
			  LEFT JOIN orders o ON o.tenant_id = t.tenant_id AND o.id = t.related_order_id
			 WHERE t.tenant_id = $1 AND t.id = $2`,
			tenantID, ticketID,
		).Scan(&t.ID, &t.TicketNo, &t.Subject, &t.Category, &t.Priority, &t.Status,
			&t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt, &t.UserID, &t.UserEmail,
			&t.AssignedTo, &t.AssigneeEmail, &t.SLAFirstDue, &t.SLAResolutionDue,
			&t.FirstRespondedAt, &t.EscalatedAt, &t.SLABreached, &t.UserActivePlan, &t.ClosedReason,
			&orderID, &orderNo)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "工单不存在")
		}
		if err != nil {
			return err
		}
		if orderID != nil {
			t.RelatedOrder = &TicketOrderRef{ID: *orderID, OrderNo: *orderNo}
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
		if err := rows.Err(); err != nil {
			return err
		}
		// 与队列同口径：条数与最后时间都算内部备注和系统消息，没有消息时取建单时间
		t.MessageCount = len(t.Messages)
		t.LastReplyAt = t.CreatedAt
		if n := len(t.Messages); n > 0 {
			t.LastReplyAt = t.Messages[n-1].CreatedAt
		}
		return nil
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

	queued := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.AgentID}, func(tx pgx.Tx) error {
		var status, ownerID, subject string
		var firstResponded *time.Time
		err := tx.QueryRow(ctx,
			`SELECT status, first_responded_at, user_id::text, subject FROM tickets
			  WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			tenantID, in.TicketID).Scan(&status, &firstResponded, &ownerID, &subject)
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

		// 通知提单人（R115）：与回复同一事务，回复回滚则通知一起消失。
		// 去重键带消息 id：每条回复恰好一条，重放不会多排。
		// 内部备注用户看不见，自然不通知。
		if !in.InternalNote && s.notifier != nil {
			n, err := s.notifier.Enqueue(ctx, tx, tenantID, ownerID, ticketRepliedTemplateCode,
				map[string]string{"subject": subject}, "ticket-replied:"+messageID)
			if err != nil {
				return err
			}
			queued = n
		}
		if complete != nil {
			return complete(tx)
		}
		return nil
	})
	// 提交之后才催派发：事务里催，可能派发循环抢在提交前扫一遍、扑个空。
	if err == nil && queued > 0 {
		s.notifier.Kick()
	}
	return err
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
