package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/google/uuid"
)

// P6 各测试共用的跨模块假实现与夹具。

// fakeDir 是内存里的 identity：邮箱、用户组、用户所在组。
type fakeDir struct {
	mu        sync.Mutex
	emails    map[uuid.UUID]string
	groups    []identitysvc.Group
	userGroup map[uuid.UUID]*uuid.UUID
}

func newFakeDir() *fakeDir {
	return &fakeDir{emails: map[uuid.UUID]string{}, userGroup: map[uuid.UUID]*uuid.UUID{}}
}

func (f *fakeDir) Emails(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		if e, ok := f.emails[id]; ok {
			out[id] = e
		}
	}
	return out, nil
}

func (f *fakeDir) UserGroupID(_ context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.userGroup[userID], nil
}

func (f *fakeDir) ListGroups(context.Context) ([]identitysvc.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identitysvc.Group(nil), f.groups...), nil
}

// fakePlans 是内存里的 catalog 套餐。
type fakePlans struct{ plans []catalogsvc.Plan }

func (f *fakePlans) ListPlans(context.Context) ([]catalogsvc.PlanSummary, error) {
	out := make([]catalogsvc.PlanSummary, 0, len(f.plans))
	for _, p := range f.plans {
		out = append(out, catalogsvc.PlanSummary{Plan: p})
	}
	return out, nil
}

func (f *fakePlans) PlansByIDs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error) {
	out := map[uuid.UUID]catalogsvc.Plan{}
	for _, id := range ids {
		for _, p := range f.plans {
			if p.ID == id {
				out[id] = p
			}
		}
	}
	return out, nil
}

// fakeSubs 是内存里的 subscription 读取面：按方法返回预设结果，并记下收到的时间参数。
type fakeSubs struct {
	mu       sync.Mutex
	expiring []subsvc.NotifyCandidate
	expired  map[bool][]subsvc.NotifyCandidate // 键：onlyWithoutLive
	quota    []subsvc.QuotaNotice
	live     map[uuid.UUID][]uuid.UUID

	expiringCalls [][2]time.Time
	expiredCalls  [][2]time.Time
}

func newFakeSubs() *fakeSubs {
	return &fakeSubs{expired: map[bool][]subsvc.NotifyCandidate{}, live: map[uuid.UUID][]uuid.UUID{}}
}

func (f *fakeSubs) ExpiringForNotify(_ context.Context, after, until time.Time) ([]subsvc.NotifyCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expiringCalls = append(f.expiringCalls, [2]time.Time{after, until})
	var out []subsvc.NotifyCandidate
	for _, c := range f.expiring {
		if c.PeriodEnd.After(after) && !c.PeriodEnd.After(until) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSubs) ExpiredForNotify(_ context.Context, endedAfter, endedUntil time.Time, onlyWithoutLive bool) ([]subsvc.NotifyCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expiredCalls = append(f.expiredCalls, [2]time.Time{endedAfter, endedUntil})
	var out []subsvc.NotifyCandidate
	for _, c := range f.expired[onlyWithoutLive] {
		if c.PeriodEnd.After(endedAfter) && !c.PeriodEnd.After(endedUntil) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSubs) QuotaUsageForNotify(context.Context, int) ([]subsvc.QuotaNotice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]subsvc.QuotaNotice(nil), f.quota...), nil
}

func (f *fakeSubs) LivePlanIDs(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID{}, f.live[userID]...), nil
}

// usePorts 给 PG 环境装上三个假端口并返回它们。
func (e *pgEnv) usePorts() (*fakeDir, *fakePlans, *fakeSubs) {
	d, p, s := newFakeDir(), &fakePlans{}, newFakeSubs()
	e.svc.UseDirectory(d)
	e.svc.UsePlans(p)
	e.svc.UseSubscriptions(s)
	return d, p, s
}

// insertTestUser 在回滚事务里插一个用户（外键要用），邮箱随机；同时登记进 dir（非 nil 时）。
func (e *pgEnv) insertTestUser(t *testing.T, dir *fakeDir) uuid.UUID {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	email := "notify-" + hex.EncodeToString(b) + "@example.test"
	var row struct{ ID uuid.UUID }
	if err := e.db.Conn(e.ctx).Raw(`INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	id := row.ID
	if dir != nil {
		dir.mu.Lock()
		dir.emails[id] = email
		dir.mu.Unlock()
	}
	return id
}

// fakeTG 是内存里的 Telegram Bot API：记下发出的消息与 setWebhook 调用，按字段返回错误。
type fakeTG struct {
	mu         sync.Mutex
	sent       []fakeTGMessage
	webhooks   []fakeTGWebhook
	sendErr    error
	webhookErr error
}

// fakeTGMessage 是一条发出的消息。
type fakeTGMessage struct {
	Token  string
	ChatID int64
	Text   string
}

// fakeTGWebhook 是一次 setWebhook 调用。
type fakeTGWebhook struct {
	Token  string
	URL    string
	Secret string
}

func (f *fakeTG) SendMessage(_ context.Context, token string, chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fakeTGMessage{Token: token, ChatID: chatID, Text: text})
	return f.sendErr
}

func (f *fakeTG) SetWebhook(_ context.Context, token, url, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.webhooks = append(f.webhooks, fakeTGWebhook{Token: token, URL: url, Secret: secret})
	return f.webhookErr
}
