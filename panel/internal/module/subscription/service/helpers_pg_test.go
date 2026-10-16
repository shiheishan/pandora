package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
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
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakeUsers 实现 Users（也给 catalog 用）。
type fakeUsers struct {
	mu      sync.Mutex
	emails  map[uuid.UUID]string
	deleted map[uuid.UUID]bool       // markDeleted 标记的已注销账号
	groups  map[uuid.UUID]*uuid.UUID // setGroup 设的用户组；没设为 nil
}

// IsDeleted 报告 markDeleted 标过的账号。
func (f *fakeUsers) IsDeleted(_ context.Context, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleted[id], nil
}

// markDeleted 把账号标成已注销。
func (f *fakeUsers) markDeleted(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleted == nil {
		f.deleted = map[uuid.UUID]bool{}
	}
	f.deleted[id] = true
}

func (f *fakeUsers) setGroup(id uuid.UUID, g *uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groups == nil {
		f.groups = make(map[uuid.UUID]*uuid.UUID)
	}
	f.groups[id] = g
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

func (f *fakeUsers) UserGroupID(_ context.Context, id uuid.UUID) (*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groups[id], nil
}

// fakeSite 实现 SiteSettings。
type fakeSite struct{}

func (fakeSite) Site(context.Context) (settingssvc.Site, error) {
	return settingssvc.Site{Name: "测试站", Timezone: "UTC"}, nil
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
	// replica 在 session_replication_role = replica 的事务里执行 q：orders、subscription_events 挡删除，
	// 真提交的用例靠它删自己建的订单及引用它的事件（测试库是超级用户）。
	replica func(q string, args ...any) error
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
		replica: func(q string, args ...any) error {
			g := d.Base().Begin()
			if g.Error != nil {
				return g.Error
			}
			defer g.Rollback()
			if err := g.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
				return err
			}
			if err := g.Exec(q, args...).Error; err != nil {
				return err
			}
			return g.Commit().Error
		},
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

// newOrder 给 user 插一张待付订单（10.00 元）并返回 ID：subscription_events.order_id、subscriptions.source_order_id
// 有指向 orders 的外键。kind 是 renewal / upgrade 时 subID 必填（库里要求指向同一用户的一份订阅），new 时为 nil。
// 订单号满足 ^[A-Z0-9-]{1,64}$，随机取，全局唯一。订单真落库时（ctx 里没有事务，或所在事务已提交）登记清理：
// 先删引用它的事件、清掉订阅上的来源单，再删订单。
func newOrder(t *testing.T, e *env, ctx context.Context, user uuid.UUID, kind string, subID *uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	no := "T-" + strings.ToUpper(randHex(16))
	if err := e.row(ctx, `INSERT INTO orders (order_no, user_id, kind, subtotal_amount, total_amount, payable_amount,
		subscription_id, expires_at)
		VALUES (?, ?, ?, 10, 10, 10, ?, now() + interval '30 minutes') RETURNING id`, no, user, kind, subID).Scan(&id); err != nil {
		t.Fatalf("建订单: %v", err)
	}
	tx.AfterCommit(ctx, "", func(context.Context) {
		t.Cleanup(func() {
			for _, q := range []string{
				`DELETE FROM subscription_events WHERE order_id = ?`,
				`UPDATE subscriptions SET source_order_id = NULL WHERE source_order_id = ?`,
				`DELETE FROM orders WHERE id = ?`,
			} {
				if err := e.replica(q, id); err != nil {
					t.Errorf("清订单 %q: %v", q, err)
				}
			}
		})
	})
	return id
}

// fulfilOrder 把 newOrder 建的待付单改成已履约：同一订阅同时只许一张未完结的续费或变更单
// （orders_one_open_subscription_change），连续续费的用例每续一次要把上一张关掉。
func fulfilOrder(t *testing.T, e *env, ctx context.Context, id uuid.UUID) {
	t.Helper()
	e.exec(t, ctx, `UPDATE orders SET status = 'fulfilled', paid_amount = total_amount, paid_at = now(), fulfilled_at = now() WHERE id = ?`, id)
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

// newPlan 用 SQL 建一个订阅套餐与一个已发布的版本（配额按 quotas），返回套餐与版本 id。
func newPlan(t *testing.T, e *env, ctx context.Context, quotas ...quotaSpec) (uuid.UUID, uuid.UUID) {
	t.Helper()
	return newPlanOfKind(t, e, ctx, catalogsvc.KindSubscription, quotas...)
}

// newOneTimePlan 用 SQL 建一个一次性套餐（不许续费）与一个已发布的版本，返回套餐与版本 id；流量配额应写 total 周期。
func newOneTimePlan(t *testing.T, e *env, ctx context.Context, quotas ...quotaSpec) (uuid.UUID, uuid.UUID) {
	t.Helper()
	return newPlanOfKind(t, e, ctx, catalogsvc.KindOneTime, quotas...)
}

// newPlanOfKind 建一个指定类型的套餐与它的第一个已发布版本。
func newPlanOfKind(t *testing.T, e *env, ctx context.Context, kind string, quotas ...quotaSpec) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var planID, versionID uuid.UUID
	if err := e.row(ctx, `INSERT INTO plans (code, name, kind, allow_renewal) VALUES (?, ?, ?, ?) RETURNING id`,
		"s-"+randHex(8), "测试套餐-"+randHex(4), kind, kind == catalogsvc.KindSubscription).Scan(&planID); err != nil {
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

// monthly 是月付 10.00 的价格快照（没有价格档）。
func monthly() PriceSnapshot {
	return PriceSnapshot{Amount: decimal.RequireFromString("10.00"), Interval: "month", IntervalCount: 1}
}

// oneTime 是一次性 20.00 的价格快照（没有价格档）；days 为 nil 表示永久。
func oneTime(days *int) PriceSnapshot {
	return PriceSnapshot{Amount: decimal.RequireFromString("20.00"), Interval: "one_time", IntervalCount: 1, ValidityDays: days}
}

// intp 取 int 的指针。
func intp(n int) *int { return &n }

// newVersion 给套餐再建一个已发布的版本（版本号 n），并设为套餐当前版本；返回版本 id。
func newVersion(t *testing.T, e *env, ctx context.Context, planID uuid.UUID, n int, quotas ...quotaSpec) uuid.UUID {
	t.Helper()
	var versionID uuid.UUID
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, ?, 3) RETURNING id`,
		planID, n).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	for _, q := range quotas {
		e.exec(t, ctx, `INSERT INTO quota_definitions (plan_version_id, metric, period, limit_value) VALUES (?, ?, ?, ?)`,
			versionID, q.metric, q.period, q.limit)
	}
	e.exec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, versionID)
	e.exec(t, ctx, `UPDATE plans SET current_version_id = ? WHERE id = ?`, versionID, planID)
	return versionID
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

// assertNoQuota 断言这一份没有 (metric, period) 的配额行。
func assertNoQuota(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, metric, period string) {
	t.Helper()
	qs, err := e.svc.Quotas(ctx, subID)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if q.Metric == metric && q.Period == period {
			t.Fatalf("不应再有配额行 %s/%s：%+v", metric, period, q)
		}
	}
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
