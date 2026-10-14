package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/support/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakeUsers 代替 identity：Contacts 与 IDsByEmail。
type fakeUsers struct {
	mu       sync.Mutex
	contacts map[uuid.UUID]identitysvc.Contact
}

func (f *fakeUsers) Contacts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]identitysvc.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uuid.UUID]identitysvc.Contact, len(ids))
	for _, id := range ids {
		if c, ok := f.contacts[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (f *fakeUsers) IDsByEmail(_ context.Context, q string, limit int) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q = strings.ToLower(q)
	var out []uuid.UUID
	for id, c := range f.contacts {
		if strings.Contains(strings.ToLower(c.Email), q) {
			out = append(out, id)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeUsers) set(id uuid.UUID, email string, displayName *string) {
	f.mu.Lock()
	f.contacts[id] = identitysvc.Contact{Email: email, DisplayName: displayName}
	f.mu.Unlock()
}

// fakeAdmins 代替 auth 管理员名单。
type fakeAdmins struct {
	mu     sync.Mutex
	admins map[uuid.UUID]bool
}

func (f *fakeAdmins) ActiveAdminIDs(_ context.Context) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uuid.UUID, 0, len(f.admins))
	for id := range f.admins {
		out = append(out, id)
	}
	return out, nil
}

func (f *fakeAdmins) IsActiveAdmin(_ context.Context, userID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.admins[userID], nil
}

func (f *fakeAdmins) add(id uuid.UUID) {
	f.mu.Lock()
	f.admins[id] = true
	f.mu.Unlock()
}

// fakeOrders 校验门户用户名下订单。
type fakeOrders struct {
	orders map[uuid.UUID]struct {
		owner uuid.UUID
		no    string
	}
}

func (f *fakeOrders) OwnedOrderNo(ctx context.Context, orderID uuid.UUID) (string, error) {
	p, ok := authn.From(ctx)
	if !ok {
		return "", errs.New(errs.Unauthorized)
	}
	o, found := f.orders[orderID]
	if !found || o.owner != p.UserID {
		return "", errs.New(errs.NotFound)
	}
	return o.no, nil
}

// fakePlans 返回用户当前套餐名。
type fakePlans struct {
	plans map[uuid.UUID]string
}

func (f *fakePlans) ActivePlanNames(_ context.Context, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string)
	for _, id := range userIDs {
		if n, ok := f.plans[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

// fakeNotifier 记录客服公开回复通知。
type fakeNotifier struct {
	notices []ReplyNotice
	err     error
}

func (f *fakeNotifier) TicketReplied(_ context.Context, n ReplyNotice) error {
	if f.err != nil {
		return f.err
	}
	f.notices = append(f.notices, n)
	return nil
}

// env 只经闭包取连接：不 import pkg/db。
type env struct {
	svc      *Service
	users    *fakeUsers
	admins   *fakeAdmins
	orders   *fakeOrders
	plans    *fakePlans
	notifier *fakeNotifier
	row      func(ctx context.Context, q string, args ...any) *sql.Row
	exec     func(ctx context.Context, q string, args ...any) error
}

func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.50", UserAgent: "support-pg-test/1.0"})

	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x06}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	idemSvc := idemsvc.New(idemrepo.New(d), txm)
	users := &fakeUsers{contacts: map[uuid.UUID]identitysvc.Contact{}}
	admins := &fakeAdmins{admins: map[uuid.UUID]bool{}}
	orders := &fakeOrders{orders: map[uuid.UUID]struct {
		owner uuid.UUID
		no    string
	}{}}
	plans := &fakePlans{plans: map[uuid.UUID]string{}}
	notifier := &fakeNotifier{}

	svc := New(repo.New(d), txm, auditSvc, idemSvc, users, admins)
	svc.UseOrders(orders)
	svc.UsePlanNames(plans)
	svc.UseReplyNotifier(notifier)

	e := &env{
		svc:      svc,
		users:    users,
		admins:   admins,
		orders:   orders,
		plans:    plans,
		notifier: notifier,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	return e, ctx
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func mustCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	e := errs.As(err)
	if e == nil || e.Code != code {
		t.Fatalf("期望错误码 %s，得到 %v", code, err)
	}
}

func (e *env) mkUser(t *testing.T, ctx context.Context, displayName *string) uuid.UUID {
	t.Helper()
	email := fmt.Sprintf("sup-%s@example.test", randHex(16))
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.users.set(id, email, displayName)
	return id
}

func (e *env) mkAdmin(t *testing.T, ctx context.Context, displayName *string) uuid.UUID {
	t.Helper()
	id := e.mkUser(t, ctx, displayName)
	e.admins.add(id)
	return id
}

func asUser(ctx context.Context, user uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{UserID: user, SessionID: "s-user", Audience: authn.AudiencePublic})
	return reqid.With(ctx, reqid.New())
}

func (e *env) asAdmin(ctx context.Context, admin uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID: admin, SessionID: "s-admin", Audience: authn.AudienceAdmin, ReauthAt: time.Now(),
	})
	return reqid.With(ctx, reqid.New())
}

// createBody 生成满足建单最小字数的正文（≥10 字）。
func createBody(extra string) string {
	return "一二三四五六七八九十" + extra
}
