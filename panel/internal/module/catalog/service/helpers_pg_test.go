package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
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
	// committed 为真时数据真实提交；publishedPlan 建的套餐记进 plans，用例结束时删掉。
	committed bool
	plans     []uuid.UUID
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
		committed: true,
	}
	t.Cleanup(func() { deleteCommittedPlans(t, e) })
	return e, ctx
}

// deleteCommittedPlans 删掉提交态用例自己建的套餐及其版本、权益、配额、价格、可见组，
// 免得读全表的用例（门户、换套餐列表）看到别人的套餐。已发布快照与价格有冻结触发器，
// 这里在事务里临时 session_replication_role = replica 跳过（触发器与外键都不触发，删除顺序无所谓）。
// 用户行与审计行留着：审计只追加，且引用用户。
func deleteCommittedPlans(t *testing.T, e *env) {
	t.Helper()
	if len(e.plans) == 0 {
		return
	}
	ids := e.plans
	stmts := []string{
		`SET LOCAL session_replication_role = replica`,
		`DELETE FROM entitlements WHERE plan_version_id IN (SELECT id FROM plan_versions WHERE plan_id IN ?)`,
		`DELETE FROM quota_definitions WHERE plan_version_id IN (SELECT id FROM plan_versions WHERE plan_id IN ?)`,
		`DELETE FROM prices WHERE plan_id IN ?`,
		`DELETE FROM plan_visible_groups WHERE plan_id IN ?`,
		`DELETE FROM plan_versions WHERE plan_id IN ?`,
		`DELETE FROM plans WHERE id IN ?`,
	}
	err := e.svc.tx.Do(context.Background(), func(ctx context.Context) error {
		for i, q := range stmts {
			var err error
			if i == 0 {
				err = e.exec(ctx, q)
			} else {
				err = e.exec(ctx, q, ids)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("清理提交态套餐: %v", err)
	}
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
	if e.committed {
		e.plans = append(e.plans, created.Plan.ID)
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

// inSavepoint 在 SAVEPOINT 里调 fn，fn 出错则 ROLLBACK TO SAVEPOINT。
// 用例整体跑在一个事务里，service 撞到库约束（唯一、外键、触发器）会让整个事务中止，
// 之后还要继续读写的用例必须把会失败的调用包在这里。
func inSavepoint(t *testing.T, ctx context.Context, e *env, name string, fn func() error) error {
	t.Helper()
	if err := e.exec(ctx, "SAVEPOINT "+name); err != nil {
		t.Fatal(err)
	}
	err := fn()
	if err != nil {
		if rerr := e.exec(ctx, "ROLLBACK TO SAVEPOINT "+name); rerr != nil {
			t.Fatal(rerr)
		}
		return err
	}
	if rerr := e.exec(ctx, "RELEASE SAVEPOINT "+name); rerr != nil {
		t.Fatal(rerr)
	}
	return nil
}

// execBypassTriggers 在测试事务里临时关掉触发器（冻结守卫、外键）执行一条 SQL，
// 只用来造产品代码造不出来的状态（例如已发布价格的窗口已过期）。CHECK 约束仍然生效。
func execBypassTriggers(t *testing.T, ctx context.Context, e *env, q string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, q, args...); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, "SET LOCAL session_replication_role = origin"); err != nil {
		t.Fatal(err)
	}
}

// assertRestrictViolation 断言错误为 PostgreSQL restrict_violation (23001)。
func assertRestrictViolation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望错误，得到 nil")
	}
	if code := testkit.SQLState(err); code != "23001" {
		t.Fatalf("期望 SQLSTATE 23001，得到 %q: %v", code, err)
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
