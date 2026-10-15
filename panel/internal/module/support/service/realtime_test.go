package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/module/support/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// rtStore 是 support store 的内存假实现，供实时推送单测用。
type rtStore struct {
	tickets  map[uuid.UUID]model.Ticket
	msgs     []*model.Message
	escalate []repo.Escalated
}

var _ store = (*rtStore)(nil)

func (s *rtStore) InsertTicket(_ context.Context, t *model.Ticket) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	t.CreatedAt = now
	t.UpdatedAt = now
	s.tickets[t.ID] = *t
	return nil
}

func (s *rtStore) InsertMessages(_ context.Context, ms []*model.Message) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, m := range ms {
		if m.ID == uuid.Nil {
			m.ID = uuid.New()
		}
		m.CreatedAt = now
		s.msgs = append(s.msgs, m)
	}
	return nil
}

func (s *rtStore) GetTicket(_ context.Context, id uuid.UUID, owner *uuid.UUID) (model.Ticket, bool, error) {
	t, ok := s.tickets[id]
	if !ok {
		return model.Ticket{}, false, nil
	}
	if owner != nil && t.UserID != *owner {
		return model.Ticket{}, false, nil
	}
	return t, true, nil
}

func (s *rtStore) LockTicket(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (model.Ticket, bool, error) {
	return s.GetTicket(ctx, id, owner)
}

func (s *rtStore) UpdateTicket(_ context.Context, id uuid.UUID, from string, cols map[string]any) (bool, error) {
	t, ok := s.tickets[id]
	if !ok || t.Status != from {
		return false, nil
	}
	if st, ok := cols["status"]; ok {
		if v, ok := st.(string); ok {
			t.Status = v
		}
	}
	s.tickets[id] = t
	return true, nil
}

func (s *rtStore) LockUserTickets(context.Context, uuid.UUID) error { return nil }

func (s *rtStore) CountOpenTickets(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func (s *rtStore) UserTickets(context.Context, uuid.UUID, int) ([]repo.TicketRow, error) {
	return []repo.TicketRow{}, nil
}

func (s *rtStore) Queue(context.Context, repo.QueueFilter) ([]repo.TicketRow, int64, error) {
	return []repo.TicketRow{}, 0, nil
}

func (s *rtStore) Messages(context.Context, uuid.UUID, bool) ([]model.Message, error) {
	return []model.Message{}, nil
}

func (s *rtStore) EscalateOverdue(context.Context, time.Time, int) ([]repo.Escalated, error) {
	return s.escalate, nil
}

func (s *rtStore) TicketNos(context.Context, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

func (s *rtStore) ListMacros(context.Context) ([]model.Macro, error) { return []model.Macro{}, nil }

func (s *rtStore) InsertMacro(context.Context, *model.Macro) error { return nil }

func (s *rtStore) UpdateMacro(context.Context, uuid.UUID, string, string, int) (bool, error) {
	return false, nil
}

func (s *rtStore) DeleteMacro(context.Context, uuid.UUID) (string, bool, error) {
	return "", false, nil
}

func (s *rtStore) LastPublicAuthorKind(_ context.Context, ticketID uuid.UUID) (string, bool, error) {
	for i := len(s.msgs) - 1; i >= 0; i-- {
		m := s.msgs[i]
		if m.TicketID == ticketID && !m.InternalNote && m.AuthorKind != AuthorSystem {
			return m.AuthorKind, true, nil
		}
	}
	return "", false, nil
}

func (s *rtStore) LockAutoCloseCandidates(context.Context, time.Time, int) ([]repo.AutoCloseRow, error) {
	return []repo.AutoCloseRow{}, nil
}

type rtTx struct{}

func (rtTx) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type rtAudit struct{}

func (rtAudit) Write(context.Context, auditsvc.Event) error { return nil }

func newRTService(t *testing.T) (*Service, *rtStore, *rtSpy, uuid.UUID, uuid.UUID) {
	t.Helper()
	user := uuid.New()
	admin := uuid.New()
	st := &rtStore{tickets: map[uuid.UUID]model.Ticket{}}
	admins := &fakeAdmins{admins: map[uuid.UUID]bool{}}
	admins.add(admin)
	svc := newService(st, rtTx{}, rtAudit{}, nil, &fakeUsers{contacts: map[uuid.UUID]identitysvc.Contact{}}, admins)
	svc.UseReplyNotifier(&fakeNotifier{})
	pub, spy := newRTSpy(t, realtime.ChannelUser(user), realtime.ChannelAdmin(), realtime.ChannelPublic())
	svc.UseRealtime(pub)
	return svc, st, spy, user, admin
}

func rtAdminCtx(admin uuid.UUID) context.Context {
	ctx := authn.With(context.Background(), authn.Principal{
		UserID: admin, SessionID: "s-admin", Audience: authn.AudienceAdmin, ReauthAt: time.Now(),
	})
	return ctx
}

func TestRealtimeCreateTicket(t *testing.T) {
	svc, _, spy, user, _ := newRTService(t)
	uctx := asUser(context.Background(), user)
	chUser := realtime.ChannelUser(user)
	chAdmin := realtime.ChannelAdmin()

	tk, err := svc.Create(uctx, CreateInput{
		Category: "general",
		Subject:  "测试主题",
		Body:     createBody("x"),
	})
	if err != nil {
		t.Fatal(err)
	}

	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{
		"table": "tickets", "op": realtime.OpInsert, "id": tk.ID.String(),
	})
	spy.expect(t, chAdmin, realtime.TopicTickets, map[string]any{
		"table": "tickets", "op": realtime.OpInsert, "id": tk.ID.String(),
	})
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{
		"table": "ticket_messages", "op": realtime.OpInsert,
	})
	spy.expectNoneOn(t, realtime.ChannelPublic(), 200*time.Millisecond)
}

func TestRealtimeUserReply(t *testing.T) {
	svc, st, spy, user, _ := newRTService(t)
	uctx := asUser(context.Background(), user)
	chUser := realtime.ChannelUser(user)

	tk, err := svc.Create(uctx, CreateInput{Category: "general", Subject: "测试主题", Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	// 认领建单产生的推送
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{"table": "tickets", "op": realtime.OpInsert, "id": tk.ID.String()})
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{"table": "ticket_messages", "op": realtime.OpInsert})

	stored := st.tickets[tk.ID]
	stored.Status = StatusPendingUser
	st.tickets[tk.ID] = stored

	if err := svc.UserReply(uctx, tk.ID, createBody("reply"), nil); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{
		"table": "tickets", "op": realtime.OpUpdate, "id": tk.ID.String(),
	})
	spy.expect(t, chUser, realtime.TopicTicketUpdated, map[string]any{"ticket_id": tk.ID.String()})
}

func TestRealtimeAgentReplyPublic(t *testing.T) {
	svc, st, spy, user, admin := newRTService(t)
	actx := rtAdminCtx(admin)
	chUser := realtime.ChannelUser(user)
	tid := uuid.New()
	st.tickets[tid] = model.Ticket{
		ID: tid, UserID: user, Status: StatusOpen, TicketNo: "T-TEST", Subject: "测试",
	}

	if err := svc.AgentReply(actx, AgentReplyInput{
		TicketID: tid, Body: createBody("agent"), InternalNote: false,
	}); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, chUser, realtime.TopicTicketUpdated, map[string]any{"ticket_id": tid.String()})
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{
		"table": "tickets", "op": realtime.OpUpdate, "id": tid.String(),
	})
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{
		"table": "ticket_messages", "op": realtime.OpInsert,
	})
}

func TestRealtimeAgentInternalNote(t *testing.T) {
	svc, st, spy, user, admin := newRTService(t)
	actx := rtAdminCtx(admin)
	chUser := realtime.ChannelUser(user)
	chAdmin := realtime.ChannelAdmin()
	tid := uuid.New()
	st.tickets[tid] = model.Ticket{
		ID: tid, UserID: user, Status: StatusOpen, TicketNo: "T-TEST", Subject: "测试",
	}

	if err := svc.AgentReply(actx, AgentReplyInput{
		TicketID: tid, Body: createBody("note"), InternalNote: true,
	}); err != nil {
		t.Fatal(err)
	}
	spy.expect(t, chAdmin, realtime.TopicTickets, map[string]any{
		"table": "ticket_messages", "op": realtime.OpInsert,
	})
	spy.expectNoneOn(t, chUser, 300*time.Millisecond)
}

func TestRealtimeEscalate(t *testing.T) {
	svc, st, spy, user, _ := newRTService(t)
	chUser := realtime.ChannelUser(user)
	tid := uuid.New()
	st.escalate = []repo.Escalated{{
		ID: tid, UserID: user, BeforeStatus: StatusOpen, BeforePriority: PriorityNormal, Priority: PriorityHigh,
	}}

	n, err := svc.EscalateOverdueScheduled(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("EscalateOverdueScheduled: n=%d err=%v", n, err)
	}
	spy.expect(t, chUser, realtime.TopicTickets, map[string]any{
		"table": "tickets", "op": realtime.OpUpdate, "id": tid.String(),
	})
}

func TestRealtimeFailedWriteDoesNotPublish(t *testing.T) {
	svc, _, spy, user, _ := newRTService(t)
	uctx := asUser(context.Background(), user)
	if err := svc.UserReply(uctx, uuid.New(), createBody("x"), nil); err == nil {
		t.Fatal("应返回错误")
	}
	spy.expectNone(t, 200*time.Millisecond)
}
