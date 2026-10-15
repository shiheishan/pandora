package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	orderrepo "github.com/aegispanel/aegis/internal/module/order/repo"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/provider/epay"
	paymentrepo "github.com/aegispanel/aegis/internal/module/payment/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	promorepo "github.com/aegispanel/aegis/internal/module/promo/repo"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
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

// testEpayPID 是测试易支付渠道的商户号（虚构）。
const testEpayPID = "1001"

// testEpayKey 是测试易支付渠道的商户密钥（虚构）。
const testEpayKey = "epay-test-key"

// gib 是 1 GiB。
const gib = int64(1) << 30

// orderCommitTables 是真提交用例前后要清的表（CASCADE 带走订单行、预留、订阅子表等）。
var orderCommitTables = []string{
	"orders", "plan_purchase_counters",
	"late_payment_cases", "payment_events", "payments", "payment_intents",
	"balance_holds", "coupon_redemptions", "subscriptions",
	"ledger_entries", "ledger_transactions", "ledger_accounts",
}

// fakeUsers 代替 identity：邮箱、用户组（恒无组）与按邮箱搜用户。
type fakeUsers struct {
	mu      sync.Mutex
	emails  map[uuid.UUID]string
	deleted map[uuid.UUID]bool // markDeleted 标记的已注销账号
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

func (f *fakeUsers) IDsByEmail(_ context.Context, q string, limit int) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []uuid.UUID{}
	for id, e := range f.emails {
		if strings.Contains(e, strings.ToLower(q)) && len(out) < limit {
			out = append(out, id)
		}
	}
	return out, nil
}

// promoCatalog 是 promo 的 CatalogReader 最小适配（同 app/adapters.go）。
type promoCatalog struct{ c *catalogsvc.Service }

func (a promoCatalog) MissingPlans(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	found, err := a.c.PlansByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	missing := []uuid.UUID{}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (a promoCatalog) PriceSubtotal(ctx context.Context, planID, priceID uuid.UUID) (string, decimal.Decimal, error) {
	req := catalogsvc.OfferRequest{Use: catalogsvc.UseNew, PlanID: &planID}
	if p, ok := authn.From(ctx); ok {
		uid := p.UserID
		req.UserID = &uid
	}
	o, err := a.c.Offer(ctx, priceID, req)
	if err != nil {
		return "", decimal.Zero, errs.New(errs.NotFound)
	}
	return string(o.Price.Currency), o.Price.UnitAmount, nil
}

func (a promoCatalog) PackSubtotal(ctx context.Context, packID uuid.UUID) (string, decimal.Decimal, error) {
	p, err := a.c.PackOffer(ctx, packID, false)
	if err != nil {
		return "", decimal.Zero, errs.New(errs.NotFound)
	}
	return string(p.Currency), p.UnitAmount, nil
}

// env 只经闭包取连接：service 包的测试不 import pkg/db（depguard）。
type env struct {
	svc      *Service
	payments *paymentsvc.Service
	ledger   *ledgersvc.Service
	promo    *promosvc.Service
	subs     *subsvc.Service
	catalog  *catalogsvc.Service
	idem     *idemsvc.Service
	users    *fakeUsers
	txm      *tx.Manager
	row      func(ctx context.Context, q string, args ...any) *sql.Row
	exec     func(ctx context.Context, q string, args ...any) error
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

// setupEnv 搭 order 与它依赖的真 service（audit、settings、ledger、catalog、promo、subscription、payment、idempotency）。
func setupEnv(t *testing.T, commit bool) (*env, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	if commit {
		clean := func() {
			testkit.Truncate(t, d, orderCommitTables...)
			if err := d.Conn(context.Background()).Exec(`DELETE FROM payment_providers WHERE code <> 'offline'`).Error; err != nil {
				t.Fatal(err)
			}
		}
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
	promoSvc := promosvc.New(promorepo.New(d), txm, auditSvc, settings)
	promoSvc.UseUsers(users)
	promoSvc.UseCatalog(promoCatalog{c: catalogSvc})
	subSvc := subsvc.New(subrepo.New(d), txm, auditSvc, settings, catalogSvc, users, sealer)
	paymentSvc := paymentsvc.New(paymentrepo.New(d), txm, ledgerSvc, nil, settings, auditSvc, sealer, paymentsvc.Config{
		PublicBaseURL:     "https://panel.example.test",
		AllowPrivateHosts: true,
	})
	idemSvc := idemsvc.New(idemrepo.New(d), txm)

	svc := &Service{
		repo:     orderrepo.New(d),
		tx:       txm,
		catalog:  catalogSvc,
		ledger:   ledgerSvc,
		promo:    promoSvc,
		subs:     subSvc,
		payments: paymentSvc,
		users:    users,
		idem:     idemSvc,
		audit:    auditSvc,
		now:      time.Now,
	}
	paymentSvc.UseSettler(svc)

	ctx := context.Background()
	if !commit {
		ctx = testkit.TxCtx(t, d)
	}
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.42", UserAgent: "order-pg-test/1.0"})

	return &env{
		svc:      svc,
		payments: paymentSvc,
		ledger:   ledgerSvc,
		promo:    promoSvc,
		subs:     subSvc,
		catalog:  catalogSvc,
		idem:     idemSvc,
		users:    users,
		txm:      txm,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}, ctx
}

func randHex(n int) string {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func intp(n int) *int { return &n }

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// mustExec 执行一条 SQL，失败即终止用例。
func (e *env) mustExec(t *testing.T, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *env) mkUser(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	email := fmt.Sprintf("order-%s@example.test", randHex(16))
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
func asAdmin(ctx context.Context, admin uuid.UUID) context.Context {
	ctx = authn.With(ctx, authn.Principal{
		UserID: admin, SessionID: "s-admin", Audience: authn.AudienceAdmin, ReauthAt: time.Now(),
	})
	return reqid.With(ctx, reqid.New())
}

// mkPlan 用 SQL 建一个在售套餐（一个已发布版本，带 100 GiB 周期流量配额）与一个 CNY 价格；stock、limit 为 nil 表示不限。
// 目录的后台建套餐要走草稿、发布与发布闸门，测试只要在售状态，所以直接写库（同 subscription 的测试）。
func (e *env) mkPlan(t *testing.T, ctx context.Context, unitCents int64, interval string, stock, limit *int) (planID, versionID, priceID uuid.UUID) {
	t.Helper()
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`,
		"o-"+randHex(12), "测试套餐-"+randHex(4)).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 1, 3) RETURNING id`,
		planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `INSERT INTO quota_definitions (plan_version_id, metric, period, limit_value) VALUES (?, ?, ?, ?)`,
		versionID, catalogsvc.MetricTraffic, "cycle", 100*gib)
	e.mustExec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, versionID)
	e.mustExec(t, ctx, `UPDATE plans SET status = 'active', current_version_id = ?, stock_total = ?, purchase_limit_per_user = ? WHERE id = ?`,
		versionID, stock, limit, planID)
	if err := e.row(ctx, `INSERT INTO prices (plan_id, currency, unit_amount, billing_interval, interval_count) VALUES (?, 'CNY', ?, ?, 1) RETURNING id`,
		planID, money.FromCents(unitCents), interval).Scan(&priceID); err != nil {
		t.Fatal(err)
	}
	return planID, versionID, priceID
}

// mkPack 用 SQL 建一个在售的 CNY 流量包。
func (e *env) mkPack(t *testing.T, ctx context.Context, unitCents int64, bytes int64) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO traffic_packs (name, traffic_bytes, currency, unit_amount) VALUES (?, ?, 'CNY', ?) RETURNING id`,
		"测试流量包-"+randHex(4), bytes, money.FromCents(unitCents)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// mkCoupon 用 SQL 建一张 CNY 立减券（不限套餐、每人一次）。
func (e *env) mkCoupon(t *testing.T, ctx context.Context, code string, amountOffCents int64) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO coupons (code, name, amount_off, currency) VALUES (?, ?, ?, 'CNY') RETURNING id`,
		code, "测试券", money.FromCents(amountOffCents)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// couponCode 生成一个不撞的券码。
func couponCode() string { return "T" + strings.ToUpper(randHex(10)) }

// giveBalance 经 ledger.Post 给用户记一笔余额（无来源的充值）。
func (e *env) giveBalance(t *testing.T, ctx context.Context, user uuid.UUID, cents int64) {
	t.Helper()
	amt := money.FromCents(cents)
	_, err := e.ledger.Post(ctx, ledgersvc.Posting{
		Kind:     ledgersvc.KindBalanceTopup,
		Currency: money.CNY,
		Legs: []ledgersvc.Leg{
			{Account: ledgersvc.PlatformAccount(ledgersvc.ChannelCash, "test"), Direction: ledgersvc.Debit, Amount: amt},
			{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, user), Direction: ledgersvc.Credit, Amount: amt},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
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

// mkEpayProvider 用 SQL 插一条指向 baseURL 的 epay 渠道（允许内网地址，只在测试里），查单会打到 baseURL。
func (e *env) mkEpayProvider(t *testing.T, ctx context.Context, code, baseURL string) {
	t.Helper()
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	plain, err := json.Marshal(map[string]string{"merchant_id": testEpayPID, "key": testEpayKey})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.Seal(plain, []byte("payment_provider:"+id.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, ctx, `
INSERT INTO payment_providers (id, code, adapter, display_name, credentials_sealed, currencies, base_url, methods, default_method,
                               allow_private_host, min_amount, enabled, accepting_new)
VALUES (?, ?, 'epay', '易支付测试', ?, '["CNY"]'::jsonb, ?, '["alipay"]'::jsonb, 'alipay', true, 0.01, true, true)`,
		id, code, sealed, baseURL)
}

// mkPayProvider 插一条在收款的易支付渠道；回调由 epayInbound 签名直接推给 payment，不会访问 baseURL。
func (e *env) mkPayProvider(t *testing.T, ctx context.Context, code string) {
	t.Helper()
	e.mkEpayProvider(t, ctx, code, "https://pay.example.test")
}

// payCode 生成一个不撞的渠道代码。
func payCode() string { return "epay-" + randHex(8) }

// epayInbound 构造易支付成功回调（GET 查询串 + MD5 签名）；同一 paymentRef 重推得到同一个事件号。
func epayInbound(paymentRef, outTradeNo string, cents int64) provider.Inbound {
	params := map[string]string{
		"pid":          testEpayPID,
		"trade_no":     paymentRef,
		"out_trade_no": outTradeNo,
		"type":         "alipay",
		"name":         "测试订单",
		"money":        money.FormatProvider(money.FromCents(cents)),
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = epay.Sign(params, testEpayKey)
	params["sign_type"] = "MD5"
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return provider.Inbound{Method: http.MethodGet, Query: q}
}

// ackOK 判断易支付回执是否表示已接受：HTTP 恒为 200，看正文 success / fail。
func ackOK(ack provider.AckOutput) bool {
	return ack.HTTPStatus == http.StatusOK && string(ack.Body) == "success"
}

// payAndNotify 以订单主人身份发起支付，再按应付金额推一笔易支付成功回调；返回回执。
func (e *env) payAndNotify(t *testing.T, ctx context.Context, user, orderID uuid.UUID, code, ref string) provider.AckOutput {
	t.Helper()
	intent, err := e.svc.Pay(asUser(ctx, user), PayInput{OrderID: orderID, Provider: code})
	if err != nil {
		t.Fatalf("发起支付: %v", err)
	}
	cents, err := money.ToCents(intent.Amount)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := e.payments.HandleWebhook(ctx, code, epayInbound(ref, intent.OutTradeNo, cents))
	if err != nil {
		t.Fatalf("回调: %v", err)
	}
	return ack
}

func (e *env) count(t *testing.T, ctx context.Context, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// order 读订单行。
func (e *env) order(t *testing.T, ctx context.Context, id uuid.UUID) model.Order {
	t.Helper()
	o, ok, err := e.svc.repo.OrderByID(ctx, id, orderrepo.NoLock)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("订单 %s 不存在", id)
	}
	return o
}

// reservationState 读订单预留的状态。
func (e *env) reservationState(t *testing.T, ctx context.Context, orderID uuid.UUID) string {
	t.Helper()
	var st string
	if err := e.row(ctx, `SELECT state FROM order_reservations WHERE order_id = ?`, orderID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// stock 读套餐的已预留与已售。
func (e *env) stock(t *testing.T, ctx context.Context, planID uuid.UUID) (reserved, sold int) {
	t.Helper()
	if err := e.row(ctx, `SELECT stock_reserved, stock_sold FROM plans WHERE id = ?`, planID).Scan(&reserved, &sold); err != nil {
		t.Fatal(err)
	}
	return reserved, sold
}

// counter 读限购计数（没有行时为 0、0）。
func (e *env) counter(t *testing.T, ctx context.Context, planID, user uuid.UUID) (purchased, reserved int) {
	t.Helper()
	err := e.row(ctx, `SELECT purchased, reserved FROM plan_purchase_counters WHERE plan_id = ? AND user_id = ?`, planID, user).
		Scan(&purchased, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return purchased, reserved
}

// strCol 读一个可空文本（没有行或为 NULL 时回空串）。
func (e *env) strCol(t *testing.T, ctx context.Context, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	err := e.row(ctx, q, args...).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return s.String
}

// txnCount 数某订单名下某种账务交易的笔数。
func (e *env) txnCount(t *testing.T, ctx context.Context, kind ledgersvc.Kind, orderID uuid.UUID) int64 {
	t.Helper()
	return e.count(t, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = ? AND source_type = 'order' AND source_id = ?`,
		string(kind), orderID)
}

// assertNoDrift 核对每个科目的余额缓存等于分录合计（同 payment 的测试）。
func (e *env) assertNoDrift(t *testing.T, ctx context.Context) {
	t.Helper()
	var agg sql.NullString
	err := e.row(ctx, `
SELECT string_agg(d.id, ',') FROM (
  SELECT a.id::text AS id
  FROM ledger_accounts a
  LEFT JOIN ledger_entries e ON e.account_id = a.id
  GROUP BY a.id, a.account_type, a.balance
  HAVING a.balance <> (
    CASE WHEN a.account_type IN ('channel_cash', 'platform_fee_expense', 'platform_promo_expense', 'suspense') THEN
      COALESCE(SUM(CASE WHEN e.direction = 'debit' THEN e.amount ELSE 0 END), 0)
      - COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE 0 END), 0)
    ELSE
      COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE 0 END), 0)
      - COALESCE(SUM(CASE WHEN e.direction = 'debit' THEN e.amount ELSE 0 END), 0)
    END
  )
) d`).Scan(&agg)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if agg.Valid && strings.TrimSpace(agg.String) != "" {
		t.Fatalf("科目余额缓存与分录合计不一致: %s", agg.String)
	}
}

// codeOf 取错误的对外码。
func codeOf(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

// mustEq 比较两个金额。
func mustEq(t *testing.T, what string, got, want decimal.Decimal) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s：得到 %s，期望 %s", what, got.StringFixed(2), want.StringFixed(2))
	}
}

// buyNew 以用户身份新购，失败即终止。
func (e *env) buyNew(t *testing.T, ctx context.Context, user, planID, priceID uuid.UUID, co Checkout) Created {
	t.Helper()
	c, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, Checkout: co})
	if err != nil {
		t.Fatalf("新购: %v", err)
	}
	return c
}

// provision 直接给用户开一份订阅（续费、流量包用例的前置），价格快照指向 priceID。
func (e *env) provision(t *testing.T, ctx context.Context, user, planID, versionID, priceID uuid.UUID, cents int64) subsvc.Subscription {
	t.Helper()
	pid := priceID
	sub, err := e.subs.Provision(ctx, subsvc.Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: subsvc.PriceSnapshot{PriceID: &pid, Currency: money.CNY, Amount: money.FromCents(cents), Interval: "month", IntervalCount: 1},
		Actor: subsvc.Actor{Kind: subsvc.ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通订阅: %v", err)
	}
	return sub
}
