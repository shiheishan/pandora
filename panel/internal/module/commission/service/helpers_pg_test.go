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

// testCurrency 是测试用的币种：站点币种缺省 USD，测试不改站点设置。
const testCurrency = money.USD

// fakeRefs 实现 Referrals：推荐关系与邮箱都在内存里（并发用例会从多个 goroutine 读）。
type fakeRefs struct {
	mu       sync.Mutex
	referrer map[uuid.UUID]uuid.UUID // 被推荐人 → 推荐人
	emails   map[uuid.UUID]string
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

// fakeSources 实现 SourceMatcher：same 里登记的一对用户算同源。
type fakeSources struct{ same map[[2]uuid.UUID]bool }

func (f *fakeSources) SharesSource(_ context.Context, a, b uuid.UUID) (bool, error) {
	return f.same[[2]uuid.UUID{a, b}], nil
}

type env struct {
	svc      *Service
	ledger   *ledgersvc.Service
	settings *settingssvc.Service
	refs     *fakeRefs
	row      func(ctx context.Context, q string, args ...any) *sql.Row
	exec     func(ctx context.Context, q string, args ...any) error
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
	}
	return e, withMeta(testkit.TxCtx(t, d))
}

// commitCtx 是不在测试事务里的 ctx：service 自己开事务、真提交（并发用例用）。数据靠随机邮箱与订单号互不干扰，不清理。
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
	no := "CM-" + strings.ToUpper(randHex(16))
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO orders (order_no, user_id, kind, status, currency, subtotal_amount, total_amount,
		payable_amount, paid_amount, paid_at, expires_at)
		VALUES (?, ?, 'new', 'paid', ?, ?, ?, ?, ?, now(), now() + interval '30 minutes') RETURNING id`,
		no, user, string(testCurrency), total, total, total, total).Scan(&id); err != nil {
		t.Fatalf("建订单: %v", err)
	}
	amt := dec2(total)
	return ordersvc.PaidOrder{
		OrderID:  id,
		OrderNo:  no,
		UserID:   user,
		Kind:     ordersvc.KindNew,
		Currency: testCurrency,
		Total:    amt,
		Paid:     amt,
		PaidAt:   time.Now().UTC(),
	}
}

// accrueAvailable 让 referrer 通过 referee 的一张订单拿到一笔已解冻的佣金（冻结天数须为 0），返回佣金额。
func accrueAvailable(t *testing.T, e *env, ctx context.Context, referrer, referee uuid.UUID, total string) decimal.Decimal {
	t.Helper()
	refer(e, referee, referrer)
	o := newPaidOrder(t, e, ctx, referee, total)
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

// accountBalance 读用户某类科目的余额缓存（没有科目时为 0）。
func accountBalance(t *testing.T, e *env, ctx context.Context, user uuid.UUID, typ ledgersvc.AccountType) decimal.Decimal {
	t.Helper()
	var bal decimal.Decimal
	if err := e.row(ctx, `SELECT COALESCE((SELECT balance FROM ledger_accounts WHERE account_type = ? AND owner_user_id = ? AND currency = ?), 0)`,
		string(typ), user, string(testCurrency)).Scan(&bal); err != nil {
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
