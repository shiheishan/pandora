package service

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/module/support/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 队列分页的缺省与上限（与旧接口一致）。
const (
	queueDefaultLimit = 25
	queueMaxLimit     = 100
)

// Queue 后台工单队列：升级的最前，再按优先级、建单时间；带提单人邮箱、指派人邮箱与在用套餐。
func (s *Service) Queue(ctx context.Context, f QueueFilter) ([]Ticket, int64, error) {
	if _, err := adminUser(ctx); err != nil {
		return nil, 0, err
	}
	now := s.now()
	rf := repo.QueueFilter{
		Statuses:     f.Statuses,
		Priority:     f.Priority,
		Category:     f.Category,
		AssignedTo:   f.AssignedTo,
		Query:        f.Query,
		OnlyBreached: f.OnlyBreached,
		Now:          now,
		Limit:        f.Limit,
		Offset:       f.Offset,
	}
	if rf.Limit <= 0 {
		rf.Limit = queueDefaultLimit
	}
	if rf.Limit > queueMaxLimit {
		rf.Limit = queueMaxLimit
	}
	if rf.Offset < 0 {
		rf.Offset = 0
	}
	if f.Query != "" {
		// 按邮箱搜提单人：identity 给 id，不跨表 JOIN users
		ids, err := s.users.IDsByEmail(ctx, f.Query, emailSearchLimit)
		if err != nil {
			return nil, 0, err
		}
		rf.UserIDs = ids
	}
	rows, total, err := s.repo.Queue(ctx, rf)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, ticketFromRow(r, now))
	}
	if _, err := s.fillAdmin(ctx, out, nil); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// AdminTicket 后台工单详情：含内部备注，消息带作者显示名。
func (s *Service) AdminTicket(ctx context.Context, id uuid.UUID) (TicketDetail, error) {
	if _, err := adminUser(ctx); err != nil {
		return TicketDetail{}, err
	}
	now := s.now()
	t, ok, err := s.repo.GetTicket(ctx, id, nil)
	if err != nil {
		return TicketDetail{}, err
	}
	if !ok {
		return TicketDetail{}, ErrTicketNotFound
	}
	ms, err := s.repo.Messages(ctx, id, true)
	if err != nil {
		return TicketDetail{}, err
	}
	view := ticketFrom(t, now)
	view.RelatedOrder = orderRefOf(t)
	ts := []Ticket{view}
	contacts, err := s.fillAdmin(ctx, ts, authorIDs(ms))
	if err != nil {
		return TicketDetail{}, err
	}
	return withMessages(ts[0], messagesFrom(ms, contacts, false)), nil
}

// Assignees 可指派的客服：可登录的管理员（后台不分权限），按邮箱升序。
func (s *Service) Assignees(ctx context.Context) ([]Assignee, error) {
	if _, err := adminUser(ctx); err != nil {
		return nil, err
	}
	ids, err := s.admins.ActiveAdminIDs(ctx)
	if err != nil {
		return nil, err
	}
	contacts, err := s.users.Contacts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Assignee, 0, len(ids))
	for _, id := range ids {
		c, ok := contacts[id]
		if !ok {
			continue
		}
		out = append(out, Assignee{ID: id, Email: c.Email, DisplayName: c.DisplayName})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Email != out[j].Email {
			return out[i].Email < out[j].Email
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

// AgentReply 客服回复或写内部备注。公开回复把工单转到等待用户（关闭的即重新打开）、记首次响应并通知提单人；
// 内部备注不改工单：不算首次响应、不改状态，否则写条备注就能停掉 SLA 计时。
func (s *Service) AgentReply(ctx context.Context, in AgentReplyInput) error {
	actor, err := adminUser(ctx)
	if err != nil {
		return err
	}
	body, err := normalizeBody(in.Body)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		t, ok, err := s.repo.LockTicket(ctx, in.TicketID, nil)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTicketNotFound
		}
		if !in.InternalNote && !CanTransition(t.Status, StatusPendingUser) {
			return ErrStatusConflict
		}
		msg := &model.Message{
			TicketID:     t.ID,
			AuthorID:     &actor,
			AuthorKind:   AuthorAgent,
			Body:         body,
			InternalNote: in.InternalNote,
		}
		if err := s.repo.InsertMessages(ctx, []*model.Message{msg}); err != nil {
			return err
		}
		s.messagesAdded(ctx, t.UserID, []*model.Message{msg})
		if in.InternalNote {
			if err := s.writeAudit(ctx, ActionInternalNote, ResourceTicket, t.ID, map[string]any{
				"message_id":    msg.ID.String(),
				"internal_note": true,
			}); err != nil {
				return err
			}
			return s.completeIdem(ctx, in.Reply)
		}

		// 关闭原因与说明随重新打开一起作废，免得门户展示与解决率统计认错
		cols := map[string]any{
			"status":        StatusPendingUser,
			"resolved_at":   nil,
			"closed_at":     nil,
			"closed_reason": nil,
			"closed_note":   nil,
		}
		if t.FirstRespondedAt == nil {
			cols["first_responded_at"] = s.now()
		}
		updated, err := s.repo.UpdateTicket(ctx, t.ID, t.Status, cols)
		if err != nil {
			return err
		}
		if !updated {
			return ErrStatusConflict
		}
		s.ticketChanged(ctx, realtime.OpUpdate, t.ID, t.UserID)
		s.ticketReplied(ctx, t.ID, t.UserID)
		if err := s.writeAudit(ctx, ActionReply, ResourceTicket, t.ID, map[string]any{
			"from":          t.Status,
			"to":            StatusPendingUser,
			"message_id":    msg.ID.String(),
			"internal_note": false,
		}); err != nil {
			return err
		}
		// 与回复同一事务：回复回滚则通知一起消失；去重键带消息 id，每条回复恰好一条
		if s.notifier != nil {
			if err := s.notifier.TicketReplied(ctx, ReplyNotice{
				UserID:    t.UserID,
				TicketID:  t.ID,
				MessageID: msg.ID,
				TicketNo:  t.TicketNo,
				Subject:   t.Subject,
			}); err != nil {
				return err
			}
		}
		return s.completeIdem(ctx, in.Reply)
	})
}

// Assign 指派工单给一名管理员；AssigneeID 为 nil 时取消指派。
func (s *Service) Assign(ctx context.Context, in AssignInput) error {
	if _, err := adminUser(ctx); err != nil {
		return err
	}
	if in.AssigneeID != nil {
		ok, err := s.admins.IsActiveAdmin(ctx, *in.AssigneeID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Invalid(map[string]string{"assigned_to": "该用户不是可指派的管理员"})
		}
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		t, ok, err := s.repo.LockTicket(ctx, in.TicketID, nil)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTicketNotFound
		}
		updated, err := s.repo.UpdateTicket(ctx, t.ID, t.Status, map[string]any{"assigned_to": in.AssigneeID})
		if err != nil {
			return err
		}
		if !updated {
			return ErrStatusConflict
		}
		s.ticketChanged(ctx, realtime.OpUpdate, t.ID, t.UserID)
		if err := s.writeAudit(ctx, ActionAssign, ResourceTicket, t.ID, map[string]any{
			"from": idString(t.AssignedTo),
			"to":   idString(in.AssigneeID),
		}); err != nil {
			return err
		}
		return s.completeIdem(ctx, in.Reply)
	})
}

// SetStatus 客服改状态（resolved、closed、escalated、pending_agent），并记一条系统消息。
func (s *Service) SetStatus(ctx context.Context, in StatusInput) error {
	if _, err := adminUser(ctx); err != nil {
		return err
	}
	to := strings.TrimSpace(in.Status)
	if !agentSettable[to] {
		return errs.Invalid(map[string]string{"status": "不支持直接设置该状态"})
	}
	reason, err := normalizeReason("reason", in.Reason)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		t, ok, err := s.repo.LockTicket(ctx, in.TicketID, nil)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTicketNotFound
		}
		if !CanTransition(t.Status, to) {
			return ErrStatusConflict
		}
		cols := agentStatusCols(t.Status, to, t.ResolvedAt, t.ClosedAt, t.EscalatedAt, t.ClosedReason, t.ClosedNote,
			t.Priority, s.now())
		updated, err := s.repo.UpdateTicket(ctx, t.ID, t.Status, cols)
		if err != nil {
			return err
		}
		if !updated {
			return ErrStatusConflict
		}
		s.ticketChanged(ctx, realtime.OpUpdate, t.ID, t.UserID)
		note := "客服将工单状态改为 " + to
		if reason != "" {
			note += "：" + reason
		}
		sysMsg := &model.Message{
			TicketID:   t.ID,
			AuthorKind: AuthorSystem,
			Body:       note,
		}
		if err := s.repo.InsertMessages(ctx, []*model.Message{sysMsg}); err != nil {
			return err
		}
		s.messagesAdded(ctx, t.UserID, []*model.Message{sysMsg})
		if err := s.writeAudit(ctx, ActionStatusChange, ResourceTicket, t.ID, map[string]any{
			"from":       t.Status,
			"to":         to,
			"has_reason": reason != "",
		}); err != nil {
			return err
		}
		return s.completeIdem(ctx, in.Reply)
	})
}

// idString 把可空 id 转成审计里的字符串或 nil。
func idString(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
