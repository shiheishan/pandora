package service

import (
	"context"

	"github.com/google/uuid"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// Create 门户建单。
func (s *Service) Create(ctx context.Context, in CreateInput) (Ticket, error) {
	uid, err := portalUser(ctx)
	if err != nil {
		return Ticket{}, err
	}
	if err := normalizeCreate(&in); err != nil {
		return Ticket{}, err
	}

	var relatedOrderID *uuid.UUID
	var relatedOrderNo *string
	if in.OrderID != nil {
		if s.orders == nil {
			return Ticket{}, errs.Invalid(map[string]string{"order_id": "订单不存在"})
		}
		no, err := s.orders.OwnedOrderNo(ctx, *in.OrderID)
		if e := errs.As(err); e != nil && e.Code == errs.NotFound {
			return Ticket{}, errs.Invalid(map[string]string{"order_id": "订单不存在"})
		}
		if err != nil {
			return Ticket{}, err
		}
		relatedOrderID = in.OrderID
		relatedOrderNo = &no
	}

	now := s.now()
	priority := priorityFor(in.Category)
	first, res := slaDue(priority, now)
	no, err := newTicketNo(now)
	if err != nil {
		return Ticket{}, errs.Wrap(errs.Internal, err)
	}

	var view Ticket
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockUserTickets(ctx, uid); err != nil {
			return err
		}
		open, err := s.repo.CountOpenTickets(ctx, uid)
		if err != nil {
			return err
		}
		if open >= MaxOpenTickets {
			return ErrTooManyOpen
		}

		t := &model.Ticket{
			TicketNo:            no,
			UserID:              uid,
			Subject:             in.Subject,
			Category:            in.Category,
			Priority:            priority,
			Status:              StatusOpen,
			RelatedOrderID:      relatedOrderID,
			RelatedOrderNo:      relatedOrderNo,
			SLAFirstResponseDue: first,
			SLAResolutionDue:    res,
		}
		if err := s.repo.InsertTicket(ctx, t); err != nil {
			return err
		}
		msgs := []*model.Message{{
			TicketID:   t.ID,
			AuthorID:   &uid,
			AuthorKind: AuthorUser,
			Body:       in.Body,
		}}
		if err := s.repo.InsertMessages(ctx, msgs); err != nil {
			return err
		}
		s.ticketChanged(ctx, realtime.OpInsert, t.ID, uid)
		s.messagesAdded(ctx, uid, msgs)
		if err := s.writeAudit(ctx, ActionCreate, ResourceTicket, t.ID, map[string]any{
			"ticket_no": t.TicketNo,
			"category":  t.Category,
			"priority":  t.Priority,
			"has_order": relatedOrderID != nil,
		}); err != nil {
			return err
		}
		if err := pluginevents.Emit(ctx, s.events, "ticket.created", "ticket.created:"+t.ID.String(), map[string]any{"event": "ticket.created", "ticket_id": t.ID.String(), "ticket_no": t.TicketNo, "user_id": uid.String(), "category": t.Category, "priority": t.Priority, "subject": t.Subject}); err != nil {
			return err
		}

		view = ticketFrom(*t, now)
		view.MessageCount = 1
		view.LastReplyAt = view.CreatedAt
		view.RelatedOrder = nil

		if in.Reply != nil {
			return s.completeIdem(ctx, func() (idemsvc.Response, error) {
				return in.Reply(view)
			})
		}
		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return view, nil
}

// MyTickets 门户「我的工单」列表。
func (s *Service) MyTickets(ctx context.Context) ([]Ticket, error) {
	uid, err := portalUser(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rows, err := s.repo.UserTickets(ctx, uid, UserListLimit)
	if err != nil {
		return nil, err
	}
	out := make([]Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, ticketFromRow(r, now))
	}
	return out, nil
}

// MyTicket 门户工单详情（不含内部备注）。
func (s *Service) MyTicket(ctx context.Context, id uuid.UUID) (TicketDetail, error) {
	uid, err := portalUser(ctx)
	if err != nil {
		return TicketDetail{}, err
	}
	now := s.now()
	t, ok, err := s.repo.GetTicket(ctx, id, &uid)
	if err != nil {
		return TicketDetail{}, err
	}
	if !ok {
		return TicketDetail{}, ErrTicketNotFound
	}
	ms, err := s.repo.Messages(ctx, id, false)
	if err != nil {
		return TicketDetail{}, err
	}
	contacts, err := s.users.Contacts(ctx, authorIDs(ms))
	if err != nil {
		return TicketDetail{}, err
	}
	view := ticketFrom(t, now)
	view.RelatedOrder = orderRefOf(t)
	return withMessages(view, messagesFrom(ms, contacts, true)), nil
}

// UserReply 门户追加回复。
func (s *Service) UserReply(ctx context.Context, id uuid.UUID, body string, reply ReplyFunc) error {
	body, err := normalizeBody(body)
	if err != nil {
		return err
	}
	uid, err := portalUser(ctx)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		t, ok, err := s.repo.LockTicket(ctx, id, &uid)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTicketNotFound
		}
		if t.Status == StatusClosed {
			return ErrReplyClosed
		}
		if !CanTransition(t.Status, StatusPendingAgent) {
			return ErrStatusConflict
		}
		msg := &model.Message{
			TicketID:   t.ID,
			AuthorID:   &uid,
			AuthorKind: AuthorUser,
			Body:       body,
		}
		if err := s.repo.InsertMessages(ctx, []*model.Message{msg}); err != nil {
			return err
		}
		cols := map[string]any{
			"status":      StatusPendingAgent,
			"resolved_at": nil,
		}
		updated, err := s.repo.UpdateTicket(ctx, t.ID, t.Status, cols)
		if err != nil {
			return err
		}
		if !updated {
			return ErrStatusConflict
		}
		s.ticketChanged(ctx, realtime.OpUpdate, t.ID, t.UserID)
		s.messagesAdded(ctx, t.UserID, []*model.Message{msg})
		s.ticketReplied(ctx, t.ID, t.UserID)
		if err := s.writeAudit(ctx, ActionReply, ResourceTicket, t.ID, map[string]any{
			"from":       t.Status,
			"to":         StatusPendingAgent,
			"message_id": msg.ID.String(),
		}); err != nil {
			return err
		}
		return s.completeIdem(ctx, reply)
	})
}

// UserClose 门户关闭工单。
func (s *Service) UserClose(ctx context.Context, id uuid.UUID, reply ReplyFunc) error {
	uid, err := portalUser(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	return s.tx.Do(ctx, func(ctx context.Context) error {
		t, ok, err := s.repo.LockTicket(ctx, id, &uid)
		if err != nil {
			return err
		}
		if !ok || t.Status == StatusClosed {
			return ErrTicketNotFound
		}
		if !CanTransition(t.Status, StatusClosed) {
			return ErrStatusConflict
		}
		cols := map[string]any{
			"status":        StatusClosed,
			"closed_at":     now,
			"closed_reason": ClosedByUser,
			"closed_note":   nil,
		}
		updated, err := s.repo.UpdateTicket(ctx, t.ID, t.Status, cols)
		if err != nil {
			return err
		}
		if !updated {
			return ErrStatusConflict
		}
		sysMsg := &model.Message{
			TicketID:   t.ID,
			AuthorKind: AuthorSystem,
			Body:       "用户已关闭此工单",
		}
		if err := s.repo.InsertMessages(ctx, []*model.Message{sysMsg}); err != nil {
			return err
		}
		s.ticketChanged(ctx, realtime.OpUpdate, t.ID, t.UserID)
		s.messagesAdded(ctx, t.UserID, []*model.Message{sysMsg})
		if err := s.writeAudit(ctx, ActionClose, ResourceTicket, t.ID, map[string]any{
			"from":          t.Status,
			"to":            StatusClosed,
			"closed_reason": ClosedByUser,
		}); err != nil {
			return err
		}
		return s.completeIdem(ctx, reply)
	})
}

// Withdraw 门户撤回工单（无幂等）。
func (s *Service) Withdraw(ctx context.Context, id uuid.UUID, reason string) error {
	reason, err := normalizeReason("reason", reason)
	if err != nil {
		return err
	}
	uid, err := portalUser(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	return s.tx.Do(ctx, func(ctx context.Context) error {
		t, ok, err := s.repo.LockTicket(ctx, id, &uid)
		if err != nil {
			return err
		}
		if !ok {
			return ErrTicketNotFound
		}
		if t.Status == StatusClosed {
			return ErrAlreadyClosed
		}
		if t.FirstRespondedAt != nil {
			return ErrAgentReplied
		}
		if !CanTransition(t.Status, StatusClosed) {
			return ErrStatusConflict
		}
		var closedNote *string
		if reason != "" {
			closedNote = &reason
		}
		cols := map[string]any{
			"status":        StatusClosed,
			"closed_at":     now,
			"closed_reason": ClosedWithdrawn,
			"closed_note":   closedNote,
		}
		updated, err := s.repo.UpdateTicket(ctx, t.ID, t.Status, cols)
		if err != nil {
			return err
		}
		if !updated {
			return ErrStatusConflict
		}
		body := "用户已撤回此工单"
		if reason != "" {
			body += "：" + reason
		}
		sysMsg := &model.Message{
			TicketID:   t.ID,
			AuthorKind: AuthorSystem,
			Body:       body,
		}
		if err := s.repo.InsertMessages(ctx, []*model.Message{sysMsg}); err != nil {
			return err
		}
		s.ticketChanged(ctx, realtime.OpUpdate, t.ID, t.UserID)
		s.messagesAdded(ctx, t.UserID, []*model.Message{sysMsg})
		return s.writeAudit(ctx, ActionWithdraw, ResourceTicket, t.ID, map[string]any{
			"from":          t.Status,
			"to":            StatusClosed,
			"closed_reason": ClosedWithdrawn,
			"has_note":      reason != "",
		})
	})
}
