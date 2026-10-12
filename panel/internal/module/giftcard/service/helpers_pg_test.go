package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/giftcard/repo"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subrepo "github.com/aegispanel/aegis/internal/module/subscription/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// gib 是 1 GiB。
const gib = int64(1) << 30

// giftCommitTables 是真提交用例前后要清的表（CASCADE 带走码、批次、兑换流水、订阅子表等）。
var giftCommitTables = []string{
	"gift_card_redemptions", "gift_card_codes", "gift_card_batches", "gift_card_templates",
	"subscriptions", "ledger_entries", "ledger_transactions", "ledger_accounts",
}

// fakeUsers 代替 identity：邮箱与用户组（恒无组）。
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

// fakeValuer 代替 order 的换套餐折算：固定退 value。
type fakeValuer struct {
	value decimal.Decimal
	lost  int
}

func (f fakeValuer) RemainingValue(context.Context, uuid.UUID, time.Time) (decimal.Decimal, money.Currency, int, error) {
	return f.value, money.CNY, f.lost, nil
}

// fakePaid 代替 order 的付费记录查询。
type fakePaid struct{ paid map[uuid.UUID]bool }

func (f fakePaid) HasPaidOrder(_ context.Context, user uuid.UUID) (bool, error) {
	return f.paid[user], nil
}

// env 只经闭包取连接：service 包的测试不 import pkg/db（depguard）。
type env struct {
	svc    *Service
	subs   *subsvc.Service
	ledger *ledgersvc.Service
	idem   *idemsvc.Service
	users  *fakeUsers
	admin  uuid.UUID
	row    func(ctx context.Context, q string, args ...any) *sql.Row
	exec   func(ctx context.Context, q string, args ...any) error
}

// newEnv 的用例整例回滚（testkit.TxCtx）。
func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	return setupEnv(t, false)
}

// newCommitEnv 的用例真提交（并发用），前后清表。
func newCommitEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	return setupEnv(t, true)
}

// setupEnv 搭 giftcard 与它依赖的真 service（audit、settings、ledger、catalog、subscription、idempotency）。
func setupEnv(t *testing.T, commit bool) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	if commit {
		clean := func() { testkit.Truncate(t, d, giftCommitTables...) }
		clean()
		t.Cleanup(clean)
	}
	_, rdb := testkit.Redis(t)

	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x05}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	users := &fakeUsers{emails: map[uuid.UUID]string{}}
	settings := settingssvc.New(settingsrepo.New(d), txm, sealer, rdb, auditSvc)
	ledgerSvc := ledgersvc.New(ledgerrepo.New(d), txm, settings, auditSvc)
	catalogSvc := catalogsvc.New(catalogrepo.New(d), txm, auditSvc, users)
	subSvc := subsvc.New(subrepo.New(d), txm, auditSvc, settings, catalogSvc, users, sealer)
	idemSvc := idemsvc.New(idemrepo.New(d), txm)
	svc := New(repo.New(d), txm, auditSvc, idemSvc, catalogSvc, subSvc, ledgerSvc, users, keys, sealer)

	ctx := context.Background()
	if !commit {
		ctx = testkit.TxCtx(t, d)
	}
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.42", UserAgent: "giftcard-pg-test/1.0"})

	e := &env{
		svc:    svc,
		subs:   subSvc,
		ledger: ledgerSvc,
		idem:   idemSvc,
		users:  users,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	e.admin = e.mkUser(t, ctx)
	return e, ctx
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func mustEq(t *testing.T, what string, got, want decimal.Decimal) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s = %s，期望 %s", what, got, want)
	}
}

// mustCode 断言 err 是给定错误码。
func mustCode(t *testing.T, err error, code errs.Code) {
	t.Helper()
	e := errs.As(err)
	if e == nil || e.Code != code {
		t.Fatalf("期望错误码 %s，得到 %v", code, err)
	}
}

// mustExec 执行一条 SQL，失败即终止用例。
func (e *env) mustExec(t *testing.T, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *env) mkUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	email := fmt.Sprintf("gift-%s@example.test", randHex(16))
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.users.mu.Lock()
	e.users.emails[id] = email
	e.users.mu.Unlock()
	return id
}

// asUser 放入门户登录主体。
func asUser(ctx context.Context, user uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{UserID: user, SessionID: "s-user", Audience: authn.AudiencePublic})
	return reqid.With(ctx, reqid.New())
}

// asAdmin 放入后台登录主体（刚重认证过）。
func (e *env) asAdmin(ctx context.Context) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID: e.admin, SessionID: "s-admin", Audience: authn.AudienceAdmin, ReauthAt: time.Now(),
	})
	return reqid.With(ctx, reqid.New())
}

// mkPlan 用 SQL 建一个在售套餐（一个已发布版本，带 100 GiB 周期流量配额）与一个 CNY 月付价格（同 order 的测试）。
func (e *env) mkPlan(t *testing.T, ctx context.Context, unitCents int64) (planID, versionID, priceID uuid.UUID) {
	t.Helper()
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`,
		"g-"+randHex(12), "测试套餐-"+randHex(4)).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 1, 3) RETURNING id`,
		planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `INSERT INTO quota_definitions (plan_version_id, metric, period, limit_value) VALUES (?, ?, ?, ?)`,
		versionID, catalogsvc.MetricTraffic, "cycle", 100*gib)
	e.mustExec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, versionID)
	e.mustExec(t, ctx, `UPDATE plans SET status = 'active', current_version_id = ? WHERE id = ?`, versionID, planID)
	if err := e.row(ctx, `INSERT INTO prices (plan_id, currency, unit_amount, billing_interval, interval_count) VALUES (?, 'CNY', ?, 'month', 1) RETURNING id`,
		planID, money.FromCents(unitCents)).Scan(&priceID); err != nil {
		t.Fatal(err)
	}
	return planID, versionID, priceID
}

// mkSub 直接经 subscription 开一份生效中的订阅（月付 30 元）。
func (e *env) mkSub(t *testing.T, ctx context.Context, user, planID, versionID, priceID uuid.UUID) subsvc.Subscription {
	t.Helper()
	pid := priceID
	sub, err := e.subs.Provision(ctx, subsvc.Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: subsvc.PriceSnapshot{PriceID: &pid, Currency: money.CNY, Amount: dec("30"), Interval: "month", IntervalCount: 1},
		Actor: subsvc.Actor{Kind: subsvc.ActorSystem},
	})
	if err != nil {
		t.Fatalf("开订阅: %v", err)
	}
	return sub
}

// mkTemplate 以管理员身份建模板。
func (e *env) mkTemplate(t *testing.T, ctx context.Context, in TemplateInput) Template {
	t.Helper()
	if in.Name == "" {
		in.Name = "测试卡-" + randHex(8)
	}
	tpl, err := e.svc.SaveTemplate(e.asAdmin(ctx), in)
	if err != nil {
		t.Fatalf("建模板: %v", err)
	}
	return tpl
}

// mkCodes 给模板生 n 张码（n ≤ 4 时样例就是全部明文），返回明文。
func (e *env) mkCodes(t *testing.T, ctx context.Context, templateID uuid.UUID, n int, expires *time.Time) []string {
	t.Helper()
	g, err := e.svc.GenerateCodes(e.asAdmin(ctx), GenerateInput{TemplateID: templateID, Count: n, Prefix: "T", ExpiresAt: expires})
	if err != nil {
		t.Fatalf("生码: %v", err)
	}
	if n <= generateSampleSize && len(g.Sample) != n {
		t.Fatalf("样例 %d 张，期望 %d", len(g.Sample), n)
	}
	return g.Sample
}

// balanceOf 取用户可用余额（CNY）。
func (e *env) balanceOf(t *testing.T, ctx context.Context, user uuid.UUID) decimal.Decimal {
	t.Helper()
	b, err := e.ledger.Balance(ctx, user, money.CNY)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// subOf 读一份订阅。
func (e *env) subOf(t *testing.T, ctx context.Context, id uuid.UUID) subsvc.Subscription {
	t.Helper()
	s, err := e.subs.Get(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// codeStatus 按明文查码的状态（经 HMAC 查，库里没有明文）。
func (e *env) codeStatus(t *testing.T, ctx context.Context, code string) string {
	t.Helper()
	var st string
	if err := e.row(ctx, `SELECT status FROM gift_card_codes WHERE code_hash = ?`, e.svc.hashCode(code)).Scan(&st); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("码 %s 不在库里", MaskCode(code))
		}
		t.Fatal(err)
	}
	return st
}

// redeem 以 user 身份兑换。
func (e *env) redeem(ctx context.Context, user uuid.UUID, code string, choice *Choice) (RedeemResult, error) {
	return e.svc.Redeem(asUser(ctx, user), RedeemInput{Code: code, Choice: choice})
}

// count 数一条 SQL 的结果。
func (e *env) count(t *testing.T, ctx context.Context, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
