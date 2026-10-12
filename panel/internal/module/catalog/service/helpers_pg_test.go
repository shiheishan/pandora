package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

// fakeUsers 实现 catalog.Users，供集成测试注入用户组与邮箱。
type fakeUsers struct {
	emails  map[uuid.UUID]string
	groupBy map[uuid.UUID]*uuid.UUID
	// deleted 里的用户按「已被删」处理：UserGroupID 回 NotFound（与 identity 的约定一致）。
	deleted map[uuid.UUID]bool
}

func (f *fakeUsers) Emails(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		if e, ok := f.emails[id]; ok {
			out[id] = e
		}
	}
	return out, nil
}

func (f *fakeUsers) UserGroupID(_ context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	if f.deleted[userID] {
		return nil, errs.New(errs.NotFound)
	}
	if f.groupBy == nil {
		return nil, nil
	}
	g, ok := f.groupBy[userID]
	if !ok {
		return nil, nil
	}
	return g, nil
}

type env struct {
	svc   *Service
	users *fakeUsers
	exec  func(ctx context.Context, sql string, args ...any) error
	row   func(ctx context.Context, sql string, args ...any) *sql.Row
}

func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}

	users := &fakeUsers{emails: map[uuid.UUID]string{}, groupBy: map[uuid.UUID]*uuid.UUID{}}
	svc := New(repo.New(d), tx.NewManager(d), auditSvc, users)

	ctx := testkit.TxCtx(t, d)
	ctx = reqid.With(ctx, reqid.New())

	e := &env{
		svc:   svc,
		users: users,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	return e, ctx
}

// newEnvCommitted 与 newEnv 相同，但不用回滚事务（库存并发测试用）。
func newEnvCommitted(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}

	users := &fakeUsers{emails: map[uuid.UUID]string{}, groupBy: map[uuid.UUID]*uuid.UUID{}}
	svc := New(repo.New(d), tx.NewManager(d), auditSvc, users)

	ctx := reqid.With(context.Background(), reqid.New())

	e := &env{
		svc:   svc,
		users: users,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	return e, ctx
}

func seedAdmin(t *testing.T, ctx context.Context, e *env) (context.Context, uuid.UUID) {
	t.Helper()
	email := uniqEmail(t, "admin")
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.users.emails[id] = email
	ctx = authn.With(ctx, authn.Principal{
		UserID:    id,
		SessionID: "s-admin",
		Audience:  authn.AudienceAdmin,
	})
	return ctx, id
}

func seedUser(t *testing.T, ctx context.Context, e *env, groupID *uuid.UUID) uuid.UUID {
	t.Helper()
	email := uniqEmail(t, "user")
	var id uuid.UUID
	if groupID != nil {
		if err := e.row(ctx, `INSERT INTO users (email, user_group_id) VALUES (?, ?) RETURNING id`, email, *groupID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		gid := *groupID
		e.users.groupBy[id] = &gid
	} else {
		if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
	}
	e.users.emails[id] = email
	return id
}

func seedGroup(t *testing.T, ctx context.Context, e *env, code string) uuid.UUID {
	t.Helper()
	if code == "" {
		code = uniqCode(t, "grp")
	}
	name := "组 " + code
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`, code, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func uniqEmail(t *testing.T, local string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s-%s@example.test", local, hex.EncodeToString(b))
}

func uniqCode(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s%s", prefix, hex.EncodeToString(b))
}

// publishedPlanOpts 定制 publishedPlan 向导入参。
type publishedPlanOpts struct {
	StockTotal       *int
	Visibility       string
	VisibleGroupIDs  []uuid.UUID
	ExtraPrices      []WizardPrice
	AllowNewPurchase *bool
	TrafficGB        *int64
}

// publishedPlan 用 CreatePlanComplete 建一个已发布套餐（默认 public、CNY 月付 9.90、10 GB）。
func publishedPlan(t *testing.T, ctx context.Context, e *env, opts publishedPlanOpts) PlanDetail {
	t.Helper()
	traffic := int64(10)
	if opts.TrafficGB != nil {
		traffic = *opts.TrafficGB
	}
	vis := VisibilityPublic
	if opts.Visibility != "" {
		vis = opts.Visibility
	}
	allowNew := true
	if opts.AllowNewPurchase != nil {
		allowNew = *opts.AllowNewPurchase
	}
	prices := []WizardPrice{
		{
			BillingInterval: "month",
			IntervalCount:   1,
			UnitAmount:      decimal.RequireFromString("9.90"),
			Currency:        "CNY",
		},
	}
	prices = append(prices, opts.ExtraPrices...)

	created, err := e.svc.CreatePlanComplete(ctx, WizardCreate{
		Plan: PlanInput{
			Code:             uniqCode(t, "plan"),
			Name:             "测试套餐",
			Visibility:       vis,
			VisibleGroupIDs:  opts.VisibleGroupIDs,
			AllowNewPurchase: allowNew,
			AllowRenewal:     true,
			AllowUpgrade:     true,
			StockTotal:       opts.StockTotal,
			Highlights:       []string{},
		},
		TrafficGB: &traffic,
		Prices:    prices,
		Publish:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created.Plan
}

func errCode(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

func errFields(err error) map[string]string {
	if err == nil {
		return nil
	}
	return errs.As(err).Fields
}

// execSavepoint 在 SAVEPOINT 里执行 SQL，失败则 ROLLBACK TO SAVEPOINT，避免事务中止。
func execSavepoint(t *testing.T, ctx context.Context, e *env, name, sql string, args ...any) error {
	t.Helper()
	if err := e.exec(ctx, "SAVEPOINT "+name); err != nil {
		return err
	}
	err := e.exec(ctx, sql, args...)
	if err != nil {
		_ = e.exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
		return err
	}
	return e.exec(ctx, "RELEASE SAVEPOINT "+name)
}

// assertRestrictViolation 断言错误为 PostgreSQL restrict_violation (23001)。
func assertRestrictViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，得到 nil")
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		t.Fatalf("期望 *pgconn.PgError，得到 %T: %v", err, err)
	}
	if pg.Code != "23001" {
		t.Fatalf("期望 SQLSTATE 23001，得到 %s: %s", pg.Code, pg.Message)
	}
}

// scanInt64 读单列 int64。
func scanInt64(t *testing.T, ctx context.Context, e *env, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
