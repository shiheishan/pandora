package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakeUsers 实现 Users（也给 catalog 用）。
type fakeUsers struct {
	mu     sync.Mutex
	emails map[uuid.UUID]string
}

func (f *fakeUsers) Emails(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uuid.UUID]string, len(ids))
	for _, id := range ids {
		if e, ok := f.emails[id]; ok {
			out[id] = e
		}
	}
	return out, nil
}

func (f *fakeUsers) UserGroupID(context.Context, uuid.UUID) (*uuid.UUID, error) { return nil, nil }

// fakeSite 实现 SiteSettings。
type fakeSite struct{}

func (fakeSite) Site(context.Context) (settingssvc.Site, error) {
	return settingssvc.Site{Name: "测试站", Timezone: "UTC", Currency: "CNY"}, nil
}

// spyRoster 记下名单通知。
type spyRoster struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (s *spyRoster) SubscriptionsChanged(_ context.Context, ids []uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, ids...)
}

func (s *spyRoster) count(id uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, x := range s.ids {
		if x == id {
			n++
		}
	}
	return n
}

// env 只经闭包取连接：service 包的测试不 import pkg/db（depguard）。
type env struct {
	rawRow  func(ctx context.Context, q string, args ...any) *sql.Row
	rawExec func(ctx context.Context, q string, args ...any) error
	txCtx   func() context.Context
	svc     *Service
	users   *fakeUsers
	roster  *spyRoster
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testkit.DB(t)
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x02
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	users := &fakeUsers{emails: map[uuid.UUID]string{}}
	cat := catalogsvc.New(catalogrepo.New(d), txm, auditSvc, users)
	svc := New(repo.New(d), txm, auditSvc, fakeSite{}, cat, users, sealer)
	roster := &spyRoster{}
	svc.UseRosterNotifier(roster)
	return &env{
		rawRow:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
		rawExec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		txCtx:   func() context.Context { return testkit.TxCtx(t, d) },
		svc:     svc,
		users:   users,
		roster:  roster,
	}
}

func (e *env) row(ctx context.Context, q string, args ...any) *sql.Row {
	return e.rawRow(ctx, q, args...)
}

func (e *env) exec(t *testing.T, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := e.rawExec(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newUser(t *testing.T, e *env, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := fmt.Sprintf("sub-%s@example.test", randHex(16))
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.users.mu.Lock()
	e.users.emails[id] = email
	e.users.mu.Unlock()
	return id
}

// quotaSpec 是建测试版本时的一条配额定义；limit 为 nil 表示不限。
type quotaSpec struct {
	metric string
	period string
	limit  *int64
}

func i64(n int64) *int64 { return &n }

// gib 是 1 GiB。
const gib = int64(1) << 30

// newPlan 用 SQL 建一个套餐与一个已发布的版本（配额按 quotas），返回套餐与版本 id。
func newPlan(t *testing.T, e *env, ctx context.Context, quotas ...quotaSpec) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var planID, versionID uuid.UUID
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`,
		"s-"+randHex(8), "测试套餐-"+randHex(4)).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 1, 3) RETURNING id`,
		planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	for _, q := range quotas {
		e.exec(t, ctx, `INSERT INTO quota_definitions (plan_version_id, metric, period, limit_value) VALUES (?, ?, ?, ?)`,
			versionID, q.metric, q.period, q.limit)
	}
	e.exec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, versionID)
	return planID, versionID
}

// monthly 是 CNY 月付 10.00 的价格快照（没有价格档）。
func monthly() PriceSnapshot {
	return PriceSnapshot{Currency: money.CNY, Amount: decimal.RequireFromString("10.00"), Interval: "month", IntervalCount: 1}
}

func adminCtx(ctx context.Context, adminID uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{UserID: adminID, SessionID: "s-admin", Audience: authn.AudienceAdmin})
	return reqid.With(ctx, reqid.New())
}

func userCtx(ctx context.Context, userID uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{UserID: userID, SessionID: "s-user", Audience: authn.AudiencePublic})
	return reqid.With(ctx, reqid.New())
}

// provision 用默认参数给用户开一份订阅。
func provision(t *testing.T, e *env, ctx context.Context, userID, planID, versionID uuid.UUID, label string) Subscription {
	t.Helper()
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: userID, PlanID: planID, PlanVersionID: versionID,
		Price: monthly(), Label: label, Actor: Actor{Kind: ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通: %v", err)
	}
	return sub
}

// quotaOf 取订阅某指标某周期的配额行。
func quotaOf(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, metric, period string) Quota {
	t.Helper()
	qs, err := e.svc.Quotas(ctx, subID)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if q.Metric == metric && q.Period == period {
			return q
		}
	}
	t.Fatalf("没有配额行 %s/%s", metric, period)
	return Quota{}
}

// setConsumed 直接改配额已用量（模拟节点扣量）。
func setConsumed(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, metric, period string, n int64) {
	t.Helper()
	e.exec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = ? AND period = ?`, n, subID, metric, period)
}

// countRows 数一条 SQL 的结果。
func countRows(t *testing.T, e *env, ctx context.Context, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
