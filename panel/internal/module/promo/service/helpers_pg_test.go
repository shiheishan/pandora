package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/promo/repo"
	settingsvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type fakePrice struct {
	plan     uuid.UUID
	currency string
	amount   decimal.Decimal
	interval string // 空按 month
}

// fakeCatalog 实现 CatalogReader，供试算与建券时校验套餐。
type fakeCatalog struct {
	plans  map[uuid.UUID]bool
	prices map[uuid.UUID]fakePrice
	packs  map[uuid.UUID]fakePrice
}

func (f *fakeCatalog) MissingPlans(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	var missing []uuid.UUID
	for _, id := range ids {
		if !f.plans[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (f *fakeCatalog) PriceSubtotal(_ context.Context, planID, priceID uuid.UUID) (string, decimal.Decimal, error) {
	p, ok := f.prices[priceID]
	if !ok || p.plan != planID {
		return "", decimal.Zero, errs.New(errs.NotFound)
	}
	return p.currency, p.amount, nil
}

func (f *fakeCatalog) PriceInterval(_ context.Context, planID, priceID uuid.UUID) (string, error) {
	p, ok := f.prices[priceID]
	if !ok || p.plan != planID {
		return "", errs.New(errs.NotFound)
	}
	if p.interval == "" {
		return "month", nil
	}
	return p.interval, nil
}

func (f *fakeCatalog) PackSubtotal(_ context.Context, packID uuid.UUID) (string, decimal.Decimal, error) {
	p, ok := f.packs[packID]
	if !ok {
		return "", decimal.Zero, errs.New(errs.NotFound)
	}
	return p.currency, p.amount, nil
}

// fakeUsers 实现 UserDirectory。
type fakeUsers struct {
	emails map[uuid.UUID]string
	groups map[uuid.UUID]uuid.UUID
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
	if g, ok := f.groups[userID]; ok {
		gid := g
		return &gid, nil
	}
	return nil, nil
}

// fakeSite 实现 SiteSettings，供建券默认币种测试。
type fakeSite struct {
	currency string
}

func (f *fakeSite) Site(context.Context) (settingsvc.Site, error) {
	return settingsvc.Site{Name: "测试站", Timezone: "UTC", Currency: f.currency}, nil
}

type env struct {
	row   func(ctx context.Context, q string, args ...any) *sql.Row
	exec  func(ctx context.Context, q string, args ...any) error
	txCtx func() context.Context
	svc   *Service
	txm   *tx.Manager
	cat   *fakeCatalog
	users *fakeUsers
	site  *fakeSite
	// replica 在 session_replication_role = replica 的事务里执行 q：orders、coupon_redemptions 有禁删触发器，
	// 真提交的用例靠它删自己建的订单与占用（测试库是超级用户）。
	replica func(q string, args ...any) error
}

func newEnv(t *testing.T) *env {
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
	txm := tx.NewManager(d)
	site := &fakeSite{currency: "CNY"}
	svc := New(repo.New(d), txm, auditSvc, site)
	cat := &fakeCatalog{
		plans:  map[uuid.UUID]bool{},
		prices: map[uuid.UUID]fakePrice{},
		packs:  map[uuid.UUID]fakePrice{},
	}
	users := &fakeUsers{
		emails: map[uuid.UUID]string{},
		groups: map[uuid.UUID]uuid.UUID{},
	}
	svc.UseCatalog(cat)
	svc.UseUsers(users)
	return &env{
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
		txCtx: func() context.Context { return testkit.TxCtx(t, d) },
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
		svc:   svc,
		txm:   txm,
		cat:   cat,
		users: users,
		site:  site,
	}
}

func newUser(t *testing.T, e *env, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := fmt.Sprintf("promo-%s@example.test", randHex(16))
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.users.emails[id] = email
	return id
}

// newPlan 在 plans 表里建一个草稿套餐（coupon_plans.plan_id 有外键），并登记到假 catalog。
func newPlan(t *testing.T, e *env, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx,
		`INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`,
		"p-"+randHex(8), "测试套餐",
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.cat.plans[id] = true
	return id
}

func newGroup(t *testing.T, e *env, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	code := "g-" + randHex(8)
	if err := e.row(ctx,
		`INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`,
		code, "测试组",
	).Scan(&id); err != nil {
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

func randCode(prefix string) string {
	return strings.ToUpper(prefix + randHex(8))
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func percentSpec(bp int64) Spec {
	return Spec{
		Name:       "测试券",
		Face:       Face{Kind: KindPercent, PercentBP: bp},
		Currency:   "CNY",
		MinOrder:   decimal.Zero,
		MaxPerUser: 1,
	}
}

func fixedSpec(amount string) Spec {
	amt, err := decimal.NewFromString(amount)
	if err != nil {
		panic(err)
	}
	return Spec{
		Name:       "测试券",
		Face:       Face{Kind: KindFixed, AmountOff: amt},
		Currency:   "CNY",
		MinOrder:   decimal.Zero,
		MaxPerUser: 1,
	}
}

func target(user uuid.UUID, subtotal string) Target {
	amt, err := decimal.NewFromString(subtotal)
	if err != nil {
		panic(err)
	}
	return Target{
		UserID:   user,
		PlanID:   nil,
		Currency: "CNY",
		Subtotal: amt,
	}
}

// newOrder 给 user 插一张待付的新购订单（10.00），返回订单引用：coupon_redemptions.order_id 有指向 orders 的外键，
// 占用里的订单号快照也用这张单的号（满足 ^[A-Z0-9-]{1,64}$，随机取，全局唯一）。
// 订单真落库时（ctx 里没有事务，或所在事务已提交）登记清理：先删引用它的占用，再删订单。
func newOrder(t *testing.T, e *env, ctx context.Context, user uuid.UUID) OrderRef {
	t.Helper()
	o := OrderRef{No: "T-" + strings.ToUpper(randHex(16))}
	if err := e.row(ctx, `INSERT INTO orders (order_no, user_id, kind, currency, subtotal_amount, total_amount, payable_amount, expires_at)
		VALUES (?, ?, 'new', 'CNY', 10, 10, 10, now() + interval '30 minutes') RETURNING id`, o.No, user).Scan(&o.ID); err != nil {
		t.Fatalf("建订单: %v", err)
	}
	tx.AfterCommit(ctx, "", func(context.Context) {
		t.Cleanup(func() {
			if err := e.replica(`DELETE FROM coupon_redemptions WHERE order_id = ?`, o.ID); err != nil {
				t.Errorf("清占用: %v", err)
			}
			if err := e.replica(`DELETE FROM orders WHERE id = ?`, o.ID); err != nil {
				t.Errorf("清订单: %v", err)
			}
		})
	})
	return o
}

func errCode(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

func createCoupon(t *testing.T, e *env, ctx context.Context, adminID uuid.UUID, code string, spec Spec) {
	t.Helper()
	_, _, err := e.svc.CreateCoupon(adminCtx(ctx, adminID), code, spec)
	if err != nil {
		t.Fatal(err)
	}
}

func execSQL(t *testing.T, e *env, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
