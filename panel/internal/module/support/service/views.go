package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/module/support/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// portalUser 取门户登录用户的 ID；没有登录主体回 401。
func portalUser(ctx context.Context) (uuid.UUID, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return uuid.Nil, errs.New(errs.Unauthorized)
	}
	return p.UserID, nil
}

// adminUser 取后台登录主体的 ID；没有主体回 401，不是后台会话回 403。
func adminUser(ctx context.Context) (uuid.UUID, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return uuid.Nil, errs.New(errs.Unauthorized)
	}
	if p.Audience != authn.AudienceAdmin {
		return uuid.Nil, errs.New(errs.Forbidden)
	}
	return p.UserID, nil
}

// ticketFrom 把一行工单转成对外类型（含后台字段；门户的 handler 只取公共字段）。统计字段由调用方填。
func ticketFrom(t model.Ticket, now time.Time) Ticket {
	out := Ticket{
		ID:                  t.ID,
		TicketNo:            t.TicketNo,
		Subject:             t.Subject,
		Category:            t.Category,
		Priority:            t.Priority,
		Status:              t.Status,
		CreatedAt:           t.CreatedAt,
		UpdatedAt:           t.UpdatedAt,
		ResolvedAt:          t.ResolvedAt,
		LastReplyAt:         t.CreatedAt,
		ClosedReason:        t.ClosedReason,
		UserID:              t.UserID,
		AssignedTo:          t.AssignedTo,
		SLAFirstResponseDue: t.SLAFirstResponseDue,
		SLAResolutionDue:    t.SLAResolutionDue,
		FirstRespondedAt:    t.FirstRespondedAt,
		EscalatedAt:         t.EscalatedAt,
		SLABreached:         breached(t, now),
	}
	return out
}

// ticketFromRow 同 ticketFrom，并带上列表的消息统计。
func ticketFromRow(r repo.TicketRow, now time.Time) Ticket {
	out := ticketFrom(r.Ticket, now)
	out.MessageCount = r.MessageCount
	out.LastReplyAt = r.LastReplyAt
	out.LastMessageAuthorKind = r.LastAuthorKind
	return out
}

// breached 报告首次响应是否已超时：还没响应、截止已过、未解决未关闭。
func breached(t model.Ticket, now time.Time) bool {
	return t.FirstRespondedAt == nil && t.SLAFirstResponseDue.Before(now) &&
		t.Status != StatusResolved && t.Status != StatusClosed
}

// orderRefOf 返回工单关联的订单（建单时存下的订单号）。
func orderRefOf(t model.Ticket) *OrderRef {
	if t.RelatedOrderID == nil || t.RelatedOrderNo == nil {
		return nil
	}
	return &OrderRef{ID: *t.RelatedOrderID, OrderNo: *t.RelatedOrderNo}
}

// withMessages 组装详情：消息条数与最后时间按给定的消息算（没有消息时为建单时间）。
func withMessages(t Ticket, ms []Message) TicketDetail {
	if ms == nil {
		ms = []Message{}
	}
	t.MessageCount = len(ms)
	t.LastReplyAt = t.CreatedAt
	if n := len(ms); n > 0 {
		t.LastReplyAt = ms[n-1].CreatedAt
	}
	return TicketDetail{Ticket: t, Messages: ms}
}

// messagesFrom 转换消息。portal 为真时作者名只给用户消息（用户自己的显示名），客服与系统一律 nil；
// 后台给作者的显示名。contacts 来自 Users.Contacts。
func messagesFrom(ms []model.Message, contacts map[uuid.UUID]identitysvc.Contact, portal bool) []Message {
	out := make([]Message, 0, len(ms))
	for _, m := range ms {
		v := Message{
			ID:           m.ID,
			AuthorKind:   m.AuthorKind,
			Body:         m.Body,
			InternalNote: m.InternalNote,
			CreatedAt:    m.CreatedAt,
		}
		if m.AuthorID != nil && (!portal || m.AuthorKind == AuthorUser) {
			if c, ok := contacts[*m.AuthorID]; ok {
				v.AuthorName = c.DisplayName
			}
		}
		if portal {
			v.InternalNote = false
		}
		out = append(out, v)
	}
	return out
}

// authorIDs 收集消息作者 id（去重）。
func authorIDs(ms []model.Message) []uuid.UUID {
	set := map[uuid.UUID]struct{}{}
	var out []uuid.UUID
	for _, m := range ms {
		if m.AuthorID == nil {
			continue
		}
		if _, ok := set[*m.AuthorID]; ok {
			continue
		}
		set[*m.AuthorID] = struct{}{}
		out = append(out, *m.AuthorID)
	}
	return out
}

// fillAdmin 给后台的工单批量补上提单人邮箱、指派人邮箱与在用套餐（邮箱一条 SQL，套餐一次批量读）。
// extra 是还要一起查联系方式的用户（如消息作者），结果一并返回。
func (s *Service) fillAdmin(ctx context.Context, ts []Ticket, extra []uuid.UUID) (map[uuid.UUID]identitysvc.Contact, error) {
	set := map[uuid.UUID]struct{}{}
	var ids, owners []uuid.UUID
	add := func(id uuid.UUID) {
		if _, ok := set[id]; !ok {
			set[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	ownerSet := map[uuid.UUID]struct{}{}
	for _, t := range ts {
		add(t.UserID)
		if t.AssignedTo != nil {
			add(*t.AssignedTo)
		}
		if _, ok := ownerSet[t.UserID]; !ok {
			ownerSet[t.UserID] = struct{}{}
			owners = append(owners, t.UserID)
		}
	}
	for _, id := range extra {
		add(id)
	}
	contacts, err := s.users.Contacts(ctx, ids)
	if err != nil {
		return nil, err
	}
	plans := map[uuid.UUID]string{}
	if s.plans != nil && len(owners) > 0 {
		if plans, err = s.plans.ActivePlanNames(ctx, owners); err != nil {
			return nil, err
		}
	}
	for i := range ts {
		t := &ts[i]
		t.UserEmail = contacts[t.UserID].Email
		if t.AssignedTo != nil {
			if c, ok := contacts[*t.AssignedTo]; ok {
				email := c.Email
				t.AssigneeEmail = &email
			}
		}
		if name, ok := plans[t.UserID]; ok {
			n := name
			t.UserActivePlan = &n
		}
	}
	return contacts, nil
}

// writeAudit 写一条工单或快捷回复的审计（加入调用方事务）。data 只放 id、状态、计数，不放正文。
func (s *Service) writeAudit(ctx context.Context, action, resourceType string, id uuid.UUID, data map[string]any) error {
	rid := id
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   &rid,
		Data:         data,
	})
}
