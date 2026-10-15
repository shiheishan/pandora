package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"sync"
	"testing"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// xb-tg 端口的假实现（ports_xb.go）：返回预设值、记下收到的参数。

// fakeSummaries 按用户返回预设的订阅摘要。
type fakeSummaries struct {
	byUser map[uuid.UUID]subsvc.UserSubscriptions
	err    error
}

func (f *fakeSummaries) SummariesByUsers(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]subsvc.UserSubscriptions, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]subsvc.UserSubscriptions{}
	for _, id := range ids {
		if us, ok := f.byUser[id]; ok {
			out[id] = us
		}
	}
	return out, nil
}

// fakeLinks 按用户返回预设的订阅链接。
type fakeLinks struct {
	byUser map[uuid.UUID][]string
	err    error
}

func (f *fakeLinks) ActiveLinks(_ context.Context, userID uuid.UUID) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if l, ok := f.byUser[userID]; ok {
		return l, nil
	}
	return []string{}, nil
}

// fakeBalances 按用户返回预设余额，币种固定。
type fakeBalances struct {
	byUser   map[uuid.UUID]decimal.Decimal
	currency string
	err      error
}

func (f *fakeBalances) UserBalances(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]decimal.Decimal, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	out := map[uuid.UUID]decimal.Decimal{}
	for _, id := range ids {
		out[id] = f.byUser[id]
	}
	cur := f.currency
	if cur == "" {
		cur = "CNY"
	}
	return out, cur, nil
}

// fakeAdmins 把 active 里的用户当作可登录的管理员。
type fakeAdmins struct {
	active map[uuid.UUID]bool
	err    error
}

func (f *fakeAdmins) IsActiveAdmin(_ context.Context, userID uuid.UUID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.active[userID], nil
}

// ActiveUserGroups 让 fakeDir 满足 Directory：除了 inactive 里标了的，都当 active 账号。
func (f *fakeDir) ActiveUserGroups(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]*uuid.UUID{}
	for _, id := range ids {
		if !f.inactive[id] {
			out[id] = f.userGroup[id]
		}
	}
	return out, nil
}

// fakeReplier 记下以管理员身份回复工单的调用。
type fakeReplier struct {
	mu      sync.Mutex
	replies []fakeReply
	err     error
}

// fakeReply 是一次回复调用。
type fakeReply struct {
	AdminID  uuid.UUID
	TicketID uuid.UUID
	Body     string
}

func (f *fakeReplier) ReplyAsAdmin(_ context.Context, adminID, ticketID uuid.UUID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, fakeReply{AdminID: adminID, TicketID: ticketID, Body: body})
	return f.err
}

var (
	_ SubscriptionSummaries = (*fakeSummaries)(nil)
	_ SubscribeLinks        = (*fakeLinks)(nil)
	_ Balances              = (*fakeBalances)(nil)
	_ Admins                = (*fakeAdmins)(nil)
	_ TicketReplier         = (*fakeReplier)(nil)
)

// insertTestTicket 给 userID 插一张最小的工单（telegram_ticket_messages 的外键要它），返回工单 id。
func (e *pgEnv) insertTestTicket(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	no := "TK20260101-" + base32.StdEncoding.EncodeToString(b)[:8]
	var row struct{ ID uuid.UUID }
	if err := e.scan(e.ctx, &row, `INSERT INTO tickets (ticket_no, user_id, subject, category, priority, sla_first_response_due, sla_resolution_due)
VALUES (?, ?, '测试工单主题', 'general', 'normal', now() + interval '12 hours', now() + interval '72 hours') RETURNING id`, no, userID); err != nil {
		t.Fatal(err)
	}
	return row.ID
}
