package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/content/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakePlans 实现 Plans：PlansByIDs 只返回登记过的套餐。
type fakePlans struct {
	plans map[uuid.UUID]bool
}

func (f *fakePlans) PlansByIDs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error) {
	out := make(map[uuid.UUID]catalogsvc.Plan, len(ids))
	for _, id := range ids {
		if f.plans[id] {
			out[id] = catalogsvc.Plan{ID: id}
		}
	}
	return out, nil
}

// fakeSubs 实现 Subscriptions：LivePlanIDs 按用户返回生效套餐。
type fakeSubs struct {
	byUser map[uuid.UUID][]uuid.UUID
}

func (f *fakeSubs) LivePlanIDs(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	return f.byUser[userID], nil
}

// fakeUsers 实现 Users：DisplayNames 按 map 返回。
type fakeUsers struct {
	names map[uuid.UUID]string
}

func (f *fakeUsers) DisplayNames(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		if n, ok := f.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

type env struct {
	row   func(ctx context.Context, q string, args ...any) *sql.Row
	exec  func(ctx context.Context, q string, args ...any) error
	txCtx func() context.Context
	svc   *Service
	plans *fakePlans
	subs  *fakeSubs
	users *fakeUsers
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testkit.DB(t)
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x03
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	plans := &fakePlans{plans: map[uuid.UUID]bool{}}
	subs := &fakeSubs{byUser: map[uuid.UUID][]uuid.UUID{}}
	users := &fakeUsers{names: map[uuid.UUID]string{}}
	svc := New(repo.New(d), txm, auditSvc, plans, subs, users)
	return &env{
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
		txCtx: func() context.Context { return testkit.TxCtx(t, d) },
		svc:   svc,
		plans: plans,
		subs:  subs,
		users: users,
	}
}

func newUser(t *testing.T, e *env, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := fmt.Sprintf("content-%s@example.test", randHex(16))
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func adminCtx(ctx context.Context, adminID uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID:    adminID,
		SessionID: "s-admin",
		Audience:  authn.AudienceAdmin,
	})
	return reqid.With(ctx, reqid.New())
}

func userCtx(ctx context.Context, userID uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID:    userID,
		SessionID: "s-user",
		Audience:  authn.AudiencePublic,
	})
	return reqid.With(ctx, reqid.New())
}

func countRows(t *testing.T, e *env, ctx context.Context, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// longBody 返回满足校验的最短正文前缀。
func longBody(s string) string {
	const filler = "这是足够长的安全纯文本正文内容。"
	if len(s) >= 10 {
		return s
	}
	return filler + s
}
