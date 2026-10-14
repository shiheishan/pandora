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

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// testCurrency 是测试用的币种：站点币种缺省 CNY，测试不改站点设置。
const testCurrency = money.CNY

// fakeRefs 实现 Referrals：推荐关系与邮箱都在内存里（并发用例会从多个 goroutine 读）。
type fakeRefs struct {
	mu       sync.Mutex
	referrer map[uuid.UUID]uuid.UUID // 被推荐人 → 推荐人
	emails   map[uuid.UUID]string
	deleted  map[uuid.UUID]bool // markDeleted 标记的已注销账号
}

func (f *fakeRefs) AccountDeleted(_ context.Context, userID uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deleted[userID], nil
}

// markDeleted 把账号标成已注销。
func (f *fakeRefs) markDeleted(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleted == nil {
		f.deleted = map[uuid.UUID]bool{}
	}
	f.deleted[id] = true
}

func (f *fakeRefs) ReferrerOf(_ context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.referrer[userID]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (f *fakeRefs) InviteeCount(_ context.Context, referrerID uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, r := range f.referrer {
		if r == referrerID {
			n++
		}
	}
	return n, nil
}

func (f *fakeRefs) ReferralCount(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.referrer)), nil
}

func (f *fakeRefs) Emails(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
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

// FindByEmail 按邮箱精确找人（不分大小写、去首尾空白，同 identity 的规范化）；没有时回 errs.NotFound。
func (f *fakeRefs) FindByEmail(_ context.Context, email string) (identitysvc.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = strings.ToLower(strings.TrimSpace(email))
	for id, e := range f.emails {
		if strings.ToLower(e) == email {
			return identitysvc.User{ID: id, Email: e}, nil
		}
	}
	return identitysvc.User{}, errs.New(errs.NotFound)
}

// fakeSources 实现 SourceMatcher：same 里登记的一对用户算同源；err 非空时查询失败。
type fakeSources struct {
	same map[[2]uuid.UUID]bool
	err  error
}

func (f *fakeSources) SharesSource(_ context.Context, a, b uuid.UUID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.same[[2]uuid.UUID{a, b}], nil
}

// commitTables 是真提交用例前后要清的表（同 order 包的 orderCommitTables，加上本模块的三张表；
// CASCADE 带走订单子表、预留、订阅子表等）。不留会被别的包或解冻扫描扫到的数据。
var commitTables = []string{
	"commission_transfers", "withdrawals", "commission_entries",
	"orders", "plan_purchase_counters",
	"late_payment_cases", "payment_events", "payments", "payment_intents",
	"balance_holds", "coupon_redemptions", "subscriptions",
	"ledger_entries", "ledger_transactions", "ledger_accounts",
}

type env struct {
	svc      *Service
	ledger   *ledgersvc.Service
	settings *settingssvc.Service
	refs     *fakeRefs
	row      func(ctx context.Context, q string, args ...any) *sql.Row
	exec     func(ctx context.Context, q string, args ...any) error
	truncate func(t *testing.T) // 清 commitTables
}

// cleanCommitted 给真提交的用例在开始前、结束后各清一次表（同 order 包的 newCommitEnv）。
// 要在改分销参数之前调用：t.Cleanup 后进先出，参数先还原、表后清。
func (e *env) cleanCommitted(t *testing.T) {
	t.Helper()
	e.truncate(t)
	t.Cleanup(func() { e.truncate(t) })
}

// newEnv 建好全部真依赖（settings、ledger、audit 连测试库），推荐关系用假实现。
// 返回的 ctx 在一个测试结束回滚的事务里；需要真提交的用例用 context.Background() 派生（见 commitCtx）。
func newEnv(t *testing.T) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	_, rdb := testkit.Redis(t)
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
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
	settings := settingssvc.New(settingsrepo.New(d), txm, sealer, rdb, auditSvc)
	ledger := ledgersvc.New(ledgerrepo.New(d), txm, settings, auditSvc)
	svc := New(repo.New(d), txm, ledger, settings, sealer, auditSvc)
	refs := &fakeRefs{referrer: map[uuid.UUID]uuid.UUID{}, emails: map[uuid.UUID]string{}}
	svc.UseReferrals(refs)
	// 缺省谁都不同源；没装配时每条佣金都会转人工复核（失败即关）
	svc.UseSourceMatcher(&fakeSources{})

	e := &env{
		svc:      svc,
		ledger:   ledger,
		settings: settings,
		refs:     refs,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
		truncate: func(t *testing.T) {
			t.Helper()
			testkit.Truncate(t, d, commitTables...)
		},
	}
	return e, withMeta(testkit.TxCtx(t, d))
}

// commitCtx 是不在测试事务里的 ctx：service 自己开事务、真提交（并发用例用）。用它的用例先调 cleanCommitted。
func commitCtx() context.Context { return withMeta(context.Background()) }

func withMeta(ctx context.Context) context.Context {
	ctx = reqid.With(ctx, reqid.New())
	return reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.9", UserAgent: "commission-test/1.0"})
}

// userCtx 把门户用户放进 ctx 作为登录主体。
func userCtx(ctx context.Context, userID uuid.UUID) context.Context {
	return authn.With(ctx, authn.Principal{UserID: userID, SessionID: "s-user", Audience: authn.AudiencePublic})
}

// adminCtx 把管理员放进 ctx 作为登录主体。
func adminCtx(ctx context.Context, adminID uuid.UUID) context.Context {
	return authn.With(ctx, authn.Principal{UserID: adminID, SessionID: "s-admin", Audience: authn.AudienceAdmin})
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func dec2(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// newUser 插一个用户并登记邮箱。
func newUser(t *testing.T, e *env, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := fmt.Sprintf("cm-%s@example.test", randHex(16))
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.refs.mu.Lock()
	e.refs.emails[id] = email
	e.refs.mu.Unlock()
	return id
}

// refer 登记 referee 由 referrer 推荐。
func refer(e *env, referee, referrer uuid.UUID) {
	e.refs.mu.Lock()
	e.refs.referrer[referee] = referrer
	e.refs.mu.Unlock()
}

// setConfig 以管理员身份改分销参数。
func setConfig(t *testing.T, e *env, ctx context.Context, admin uuid.UUID, rate, freeze int, minWithdraw, scope string) {
	t.Helper()
	m := dec2(minWithdraw)
	if err := e.settings.SetCommission(adminCtx(ctx, admin), settingssvc.CommissionUpdate{
		RatePercent: &rate, FreezeDays: &freeze, MinWithdraw: &m, Scope: &scope,
	}); err != nil {
		t.Fatal(err)
	}
}

// newPaidOrder 给 user 插一张已付的新购订单（金额 total、币种 testCurrency），返回交给 PaidHook 的摘要。
func newPaidOrder(t *testing.T, e *env, ctx context.Context, user uuid.UUID, total string) ordersvc.PaidOrder {
	t.Helper()
	return newPaidOrderIn(t, e, ctx, user, total, testCurrency)
}

// newPaidOrderIn 同 newPaidOrder，币种由调用方给。
func newPaidOrderIn(t *testing.T, e *env, ctx context.Context, user uuid.UUID, total string, cur money.Currency) ordersvc.PaidOrder {
	t.Helper()
	no := "CM-" + strings.ToUpper(randHex(16))
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO orders (order_no, user_id, kind, status, currency, subtotal_amount, total_amount,
		payable_amount, paid_amount, paid_at, expires_at)
		VALUES (?, ?, 'new', 'paid', ?, ?, ?, ?, ?, now(), now() + interval '30 minutes') RETURNING id`,
		no, user, string(cur), total, total, total, total).Scan(&id); err != nil {
		t.Fatalf("建订单: %v", err)
	}
	amt := dec2(total)
	return ordersvc.PaidOrder{
		OrderID:  id,
		OrderNo:  no,
		UserID:   user,
		Kind:     ordersvc.KindNew,
		Currency: cur,
		Total:    amt,
		Paid:     amt,
		PaidAt:   time.Now().UTC(),
	}
}

// accrueAvailable 让 referrer 通过 referee 的一张订单拿到一笔已解冻的佣金（冻结天数须为 0），返回佣金额。
func accrueAvailable(t *testing.T, e *env, ctx context.Context, referrer, referee uuid.UUID, total string) decimal.Decimal {
	t.Helper()
	return accrueAvailableIn(t, e, ctx, referrer, referee, total, testCurrency)
}

// accrueAvailableIn 同 accrueAvailable，订单币种由调用方给。
func accrueAvailableIn(t *testing.T, e *env, ctx context.Context, referrer, referee uuid.UUID, total string, cur money.Currency) decimal.Decimal {
	t.Helper()
	refer(e, referee, referrer)
	o := newPaidOrderIn(t, e, ctx, referee, total, cur)
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatalf("计提: %v", err)
	}
	if err := e.exec(ctx, `UPDATE commission_entries SET frozen_until = now() - interval '1 second' WHERE order_id = ?`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatalf("解冻: %v", err)
	}
	var amt decimal.Decimal
	if err := e.row(ctx, `SELECT commission_amount FROM commission_entries WHERE order_id = ? AND status = 'available'`, o.OrderID).Scan(&amt); err != nil {
		t.Fatalf("读解冻后的条目: %v", err)
	}
	return amt
}

// accountBalance 读用户某类科目（testCurrency）的余额缓存（没有科目时为 0）。
func accountBalance(t *testing.T, e *env, ctx context.Context, user uuid.UUID, typ ledgersvc.AccountType) decimal.Decimal {
	t.Helper()
	return accountBalanceIn(t, e, ctx, user, typ, testCurrency)
}

// accountBalanceIn 同 accountBalance，币种由调用方给。
func accountBalanceIn(t *testing.T, e *env, ctx context.Context, user uuid.UUID, typ ledgersvc.AccountType, cur money.Currency) decimal.Decimal {
	t.Helper()
	var bal decimal.Decimal
	if err := e.row(ctx, `SELECT COALESCE((SELECT balance FROM ledger_accounts WHERE account_type = ? AND owner_user_id = ? AND currency = ?), 0)`,
		string(typ), user, string(cur)).Scan(&bal); err != nil {
		t.Fatal(err)
	}
	return bal
}

func errCode(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}
