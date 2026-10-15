package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/commission/repo"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identityrepo "github.com/aegispanel/aegis/internal/module/identity/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	orderrepo "github.com/aegispanel/aegis/internal/module/order/repo"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
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
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// oeEpayPID、oeEpayKey 是测试易支付渠道的商户号与商户密钥（虚构）。
const (
	oeEpayPID = "1001"
	oeEpayKey = "commission-e2e-key"
)

const oeGib = int64(1) << 30

// oeEnv 是经真实订单结算的端到端测试环境（order、payment、commission 等同 wire 装配）。
type oeEnv struct {
	order      *ordersvc.Service
	payments   *paymentsvc.Service
	commission *Service
	settings   *settingssvc.Service
	audit      *auditsvc.Service
	plans      []uuid.UUID // oeMkPlan 建的套餐，真提交用例结束时下架
	row        func(ctx context.Context, q string, args ...any) *sql.Row
	exec       func(ctx context.Context, q string, args ...any) error
}

// oePromoCatalog 是 promo 的 CatalogReader 适配（同 order 测试里的 promoCatalog）。
type oePromoCatalog struct{ c *catalogsvc.Service }

func (a oePromoCatalog) MissingPlans(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
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

func (a oePromoCatalog) PriceSubtotal(ctx context.Context, planID, priceID uuid.UUID) (string, decimal.Decimal, error) {
	req := catalogsvc.OfferRequest{Use: catalogsvc.UseNew, PlanID: &planID}
	if p, ok := authn.From(ctx); ok {
		uid := p.UserID
		req.UserID = &uid
	}
	o, err := a.c.Offer(ctx, priceID, req)
	if err != nil {
		return "", decimal.Zero, err
	}
	return string(o.Price.Currency), o.Price.UnitAmount, nil
}

func (a oePromoCatalog) PriceInterval(ctx context.Context, planID, priceID uuid.UUID) (string, error) {
	req := catalogsvc.OfferRequest{Use: catalogsvc.UseNew, PlanID: &planID}
	if p, ok := authn.From(ctx); ok {
		uid := p.UserID
		req.UserID = &uid
	}
	o, err := a.c.Offer(ctx, priceID, req)
	if err != nil {
		return "", err
	}
	return o.Price.BillingInterval, nil
}

func (a oePromoCatalog) PackSubtotal(ctx context.Context, packID uuid.UUID) (string, decimal.Decimal, error) {
	p, err := a.c.PackOffer(ctx, packID, false)
	if err != nil {
		return "", decimal.Zero, err
	}
	return string(p.Currency), p.UnitAmount, nil
}

// newOEEnv 按 wire.go 装配真 service；commit 为 false 时整例在测试事务里回滚。
func newOEEnv(t *testing.T, commit bool) (e *oeEnv, ctx context.Context) {
	t.Helper()
	d := testkit.DB(t)
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
	settings := settingssvc.New(settingsrepo.New(d), txm, sealer, rdb, auditSvc)
	identitySvc := identitysvc.New(identityrepo.New(d), txm, settings, auditSvc, keys, rdb, ratelimit.New(rdb), &config.Config{})
	ledgerSvc := ledgersvc.New(ledgerrepo.New(d), txm, settings, auditSvc)
	catalogSvc := catalogsvc.New(catalogrepo.New(d), txm, auditSvc, identitySvc)
	promoSvc := promosvc.New(promorepo.New(d), txm, auditSvc, settings)
	promoSvc.UseUsers(identitySvc)
	promoSvc.UseCatalog(oePromoCatalog{c: catalogSvc})
	paymentSvc := paymentsvc.New(paymentrepo.New(d), txm, ledgerSvc, identitySvc, settings, auditSvc, sealer, paymentsvc.Config{
		PublicBaseURL:     "https://panel.example.test",
		AllowPrivateHosts: true,
	})
	subSvc := subsvc.New(subrepo.New(d), txm, auditSvc, settings, catalogSvc, identitySvc, sealer)
	idemSvc := idemsvc.New(idemrepo.New(d), txm)
	orderSvc := ordersvc.New(orderrepo.New(d), txm, catalogSvc, ledgerSvc, promoSvc, subSvc, paymentSvc, identitySvc, idemSvc, auditSvc)
	commissionSvc := New(repo.New(d), txm, ledgerSvc, settings, sealer, auditSvc)
	commissionSvc.UseReferrals(identitySvc)
	commissionSvc.UseSourceMatcher(auditSvc)
	paymentSvc.UseSettler(orderSvc)
	orderSvc.UsePaidHook(commissionSvc)

	ctx = context.Background()
	if !commit {
		ctx = testkit.TxCtx(t, d)
	}
	ctx = withMeta(ctx)

	e = &oeEnv{
		order:      orderSvc,
		payments:   paymentSvc,
		commission: commissionSvc,
		settings:   settings,
		audit:      auditSvc,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	if commit {
		// 真提交：前后都清（同 order 包的 newCommitEnv），不给共享测试库留启用的渠道、在售套餐、待解冻条目
		clean := func() {
			testkit.Truncate(t, d, commitTables...)
			bg := context.Background()
			if err := d.Conn(bg).Exec(`DELETE FROM payment_providers WHERE code <> 'offline'`).Error; err != nil {
				t.Fatal(err)
			}
			if len(e.plans) > 0 {
				if err := d.Conn(bg).Exec(`UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE plan_id IN ? AND status = 'active'`, e.plans).Error; err != nil {
					t.Fatal(err)
				}
				if err := d.Conn(bg).Exec(`UPDATE plans SET status = 'archived' WHERE id IN ? AND status <> 'archived'`, e.plans).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
		clean()
		t.Cleanup(clean)
	}
	return e, ctx
}

func (e *oeEnv) oeMustExec(t *testing.T, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func oeUser(t *testing.T, e *oeEnv, ctx context.Context) uuid.UUID {
	t.Helper()
	email := fmt.Sprintf("oe-%s@example.test", randHex(16))
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func oeRefer(t *testing.T, e *oeEnv, ctx context.Context, referee, referrer uuid.UUID) {
	t.Helper()
	if err := e.exec(ctx, `INSERT INTO referrals (referee_user_id, referrer_user_id) VALUES (?, ?)`, referee, referrer); err != nil {
		t.Fatal(err)
	}
}

func (e *oeEnv) oeMkPlan(t *testing.T, ctx context.Context, unitCents int64) (planID, priceID uuid.UUID) {
	t.Helper()
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`,
		"oe-"+randHex(12), "测试套餐-"+randHex(4)).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	var versionID uuid.UUID
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 1, 3) RETURNING id`,
		planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	e.oeMustExec(t, ctx, `INSERT INTO quota_definitions (plan_version_id, metric, period, limit_value) VALUES (?, ?, ?, ?)`,
		versionID, catalogsvc.MetricTraffic, "cycle", 100*oeGib)
	e.oeMustExec(t, ctx, `UPDATE plan_versions SET status = 'published', frozen_at = now() WHERE id = ?`, versionID)
	e.oeMustExec(t, ctx, `UPDATE plans SET status = 'active', current_version_id = ? WHERE id = ?`, versionID, planID)
	if err := e.row(ctx, `INSERT INTO prices (plan_id, currency, unit_amount, billing_interval, interval_count) VALUES (?, 'CNY', ?, 'month', 1) RETURNING id`,
		planID, money.FromCents(unitCents)).Scan(&priceID); err != nil {
		t.Fatal(err)
	}
	e.plans = append(e.plans, planID)
	return planID, priceID
}

// oeMkPayProvider 插入收款测试用的易支付渠道；回调由 oeEpayInbound 签名直接推给 payment，不访问 base_url。
func (e *oeEnv) oeMkPayProvider(t *testing.T, ctx context.Context, code string) {
	t.Helper()
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	plain, err := json.Marshal(map[string]string{"merchant_id": oeEpayPID, "key": oeEpayKey})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.Seal(plain, []byte("payment_provider:"+id.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.oeMustExec(t, ctx, `
INSERT INTO payment_providers (id, code, adapter, display_name, credentials_sealed, currencies, base_url, methods, default_method, min_amount, enabled, accepting_new)
VALUES (?, ?, 'epay', '易支付测试', ?, '["CNY"]'::jsonb, 'https://pay.example.test', '["alipay"]'::jsonb, 'alipay', 0.01, true, true)`,
		id, code, sealed)
}

func oeEpayInbound(paymentRef, outTradeNo string, cents int64) provider.Inbound {
	params := map[string]string{
		"pid":          oeEpayPID,
		"trade_no":     paymentRef,
		"out_trade_no": outTradeNo,
		"type":         "alipay",
		"name":         "测试订单",
		"money":        money.FormatProvider(money.FromCents(cents)),
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = epay.Sign(params, oeEpayKey)
	params["sign_type"] = "MD5"
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return provider.Inbound{Method: http.MethodGet, Query: q}
}

// oeAckOK 判断易支付回调是否被接受：HTTP 恒为 200，接没接受看正文 success。
func oeAckOK(ack provider.AckOutput) bool {
	return ack.HTTPStatus == http.StatusOK && string(ack.Body) == "success"
}

func oeBuy(t *testing.T, e *oeEnv, ctx context.Context, user, planID, priceID uuid.UUID) uuid.UUID {
	t.Helper()
	c, err := e.order.CreateNewOrder(userCtx(ctx, user), ordersvc.NewOrderInput{
		PlanID: planID, PriceID: priceID, Checkout: ordersvc.Checkout{},
	})
	if err != nil {
		t.Fatalf("新购: %v", err)
	}
	return c.OrderID
}

func oePay(t *testing.T, e *oeEnv, ctx context.Context, user, orderID uuid.UUID, code string) (outTradeNo string, cents int64) {
	t.Helper()
	intent, err := e.order.Pay(userCtx(ctx, user), ordersvc.PayInput{OrderID: orderID, Provider: code})
	if err != nil {
		t.Fatalf("发起支付: %v", err)
	}
	cents, err = money.ToCents(intent.Amount)
	if err != nil {
		t.Fatal(err)
	}
	return intent.OutTradeNo, cents
}

func oeCount(t *testing.T, e *oeEnv, ctx context.Context, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func oeCNYBalance(t *testing.T, e *oeEnv, ctx context.Context, user uuid.UUID, typ ledgersvc.AccountType) decimal.Decimal {
	t.Helper()
	var bal decimal.Decimal
	if err := e.row(ctx, `SELECT COALESCE((SELECT balance FROM ledger_accounts WHERE account_type = ? AND owner_user_id = ? AND currency = 'CNY'), 0)`,
		string(typ), user).Scan(&bal); err != nil {
		t.Fatal(err)
	}
	return bal
}

func oeSetConfig(t *testing.T, e *oeEnv, ctx context.Context, admin uuid.UUID, rate, freeze int, minWithdraw, scope string) {
	t.Helper()
	m := dec2(minWithdraw)
	if err := e.settings.SetCommission(adminCtx(ctx, admin), settingssvc.CommissionUpdate{
		RatePercent: &rate, FreezeDays: &freeze, MinWithdraw: &m, Scope: &scope,
	}); err != nil {
		t.Fatal(err)
	}
}

func oeAssertNoDrift(t *testing.T, e *oeEnv, ctx context.Context) {
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

// oeFlakyHook 在 inner 成功后再按 fail 模拟结算链后续步骤失败。
type oeFlakyHook struct {
	inner *Service
	fail  atomic.Bool
}

func (h *oeFlakyHook) OnOrderPaid(ctx context.Context, o ordersvc.PaidOrder) error {
	if err := h.inner.OnOrderPaid(ctx, o); err != nil {
		return err
	}
	if h.fail.Load() {
		return errors.New("模拟佣金之后的失败")
	}
	return nil
}

func TestOrderSettlementAccruesCommission(t *testing.T) {
	e, ctx := newOEEnv(t, false)
	code := "epay-" + randHex(8)
	e.oeMkPayProvider(t, ctx, code)
	admin := oeUser(t, e, ctx)
	referrer := oeUser(t, e, ctx)
	buyer := oeUser(t, e, ctx)
	oeRefer(t, e, ctx, buyer, referrer)
	oeSetConfig(t, e, ctx, admin, 10, 3, "10.00", "every_order")
	planID, priceID := e.oeMkPlan(t, ctx, 10000)
	orderID := oeBuy(t, e, ctx, buyer, planID, priceID)
	outNo, cents := oePay(t, e, ctx, buyer, orderID, code)
	payRef := "pay-" + randHex(8)
	in := oeEpayInbound(payRef, outNo, cents)
	ack, err := e.payments.HandleWebhook(ctx, code, in)
	if err != nil || !oeAckOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	var ostatus string
	if err := e.row(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&ostatus); err != nil {
		t.Fatal(err)
	}
	if ostatus != ordersvc.StatusFulfilled {
		t.Fatalf("订单 status=%s", ostatus)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, orderID); n != 1 {
		t.Fatalf("佣金条目 %d 行", n)
	}
	var (
		gotReferrer uuid.UUID
		commAmt     decimal.Decimal
		currency    string
		reviewReq   bool
	)
	if err := e.row(ctx, `SELECT referrer_user_id, commission_amount, currency, review_required FROM commission_entries WHERE order_id = ?`, orderID).
		Scan(&gotReferrer, &commAmt, &currency, &reviewReq); err != nil {
		t.Fatal(err)
	}
	if gotReferrer != referrer || !commAmt.Equal(dec2("10.00")) || currency != "CNY" || reviewReq {
		t.Fatalf("条目 referrer=%s amount=%s currency=%s review=%v", gotReferrer, commAmt, currency, reviewReq)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, orderID); n != 1 {
		t.Fatalf("commission_accrued %d 笔", n)
	}
	if bal := oeCNYBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("10.00")) {
		t.Fatalf("待结佣金 %s", bal)
	}
	// 同一回调原样重推：结算只发生一次，佣金也只计一次
	ack2, err := e.payments.HandleWebhook(ctx, code, in)
	if err != nil || !oeAckOK(ack2) {
		t.Fatalf("重放回调 %s %v", ack2.Body, err)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, orderID); n != 1 {
		t.Fatalf("重放后条目 %d 行", n)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, orderID); n != 1 {
		t.Fatalf("重放后 commission_accrued %d 笔", n)
	}
	oeAssertNoDrift(t, e, ctx)
}

func TestOrderSettlementHookFailureRollsBack(t *testing.T) {
	e, ctx := newOEEnv(t, true)
	admin := oeUser(t, e, ctx)
	prev, err := e.settings.Commission(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.settings.SetCommission(adminCtx(commitCtx(), admin), settingssvc.CommissionUpdate{
			RatePercent: &prev.RatePercent, FreezeDays: &prev.FreezeDays, MinWithdraw: &prev.MinWithdraw, Scope: &prev.Scope,
		}); err != nil {
			t.Errorf("还原分销参数: %v", err)
		}
	})
	code := "epay-" + randHex(8)
	e.oeMkPayProvider(t, ctx, code)
	referrer := oeUser(t, e, ctx)
	buyer := oeUser(t, e, ctx)
	oeRefer(t, e, ctx, buyer, referrer)
	oeSetConfig(t, e, ctx, admin, 10, 3, "10.00", "every_order")
	planID, priceID := e.oeMkPlan(t, ctx, 10000)
	orderID := oeBuy(t, e, ctx, buyer, planID, priceID)
	outNo, cents := oePay(t, e, ctx, buyer, orderID, code)
	hook := &oeFlakyHook{inner: e.commission}
	hook.fail.Store(true)
	e.order.UsePaidHook(hook)
	payRef := "pay-hook-" + randHex(6)
	in := oeEpayInbound(payRef, outNo, cents)
	ack, err := e.payments.HandleWebhook(ctx, code, in)
	if err == nil && oeAckOK(ack) {
		t.Fatal("hook 失败时回执不应是成功")
	}
	var ostatus string
	if err := e.row(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&ostatus); err != nil {
		t.Fatal(err)
	}
	if ostatus != ordersvc.StatusPending {
		t.Fatalf("订单应仍待付，得到 %s", ostatus)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, orderID); n != 0 {
		t.Fatalf("佣金条目应回滚，得到 %d", n)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, orderID); n != 0 {
		t.Fatalf("commission_accrued 应回滚，得到 %d", n)
	}
	if bal := oeCNYBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("待结佣金应为 0，得到 %s", bal)
	}
	hook.fail.Store(false)
	ack, err = e.payments.HandleWebhook(ctx, code, in)
	if err != nil || !oeAckOK(ack) {
		t.Fatalf("重发回调 %s %v", ack.Body, err)
	}
	if err := e.row(ctx, `SELECT status FROM orders WHERE id = ?`, orderID).Scan(&ostatus); err != nil {
		t.Fatal(err)
	}
	if ostatus != ordersvc.StatusFulfilled {
		t.Fatalf("重发后订单 %s", ostatus)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM commission_entries WHERE order_id = ?`, orderID); n != 1 {
		t.Fatalf("条目 %d 行", n)
	}
	if n := oeCount(t, e, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_accrued' AND source_id = ?`, orderID); n != 1 {
		t.Fatalf("commission_accrued %d 笔", n)
	}
	if bal := oeCNYBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.Equal(dec2("10.00")) {
		t.Fatalf("待结佣金 %s", bal)
	}
	oeAssertNoDrift(t, e, ctx)
}

func TestOrderSettlementSameSourceNeedsReview(t *testing.T) {
	e, ctx := newOEEnv(t, false)
	code := "epay-" + randHex(8)
	e.oeMkPayProvider(t, ctx, code)
	admin := oeUser(t, e, ctx)
	referrer := oeUser(t, e, ctx)
	buyer := oeUser(t, e, ctx)
	oeRefer(t, e, ctx, buyer, referrer)
	oeSetConfig(t, e, ctx, admin, 10, 3, "10.00", "every_order")
	// 两人在同一来源地址（withMeta 的同一个 IP）各留一条审计
	for _, u := range []uuid.UUID{referrer, buyer} {
		if err := e.audit.Write(userCtx(ctx, u), auditsvc.Event{Action: "test.same_source"}); err != nil {
			t.Fatal(err)
		}
	}
	planID, priceID := e.oeMkPlan(t, ctx, 10000)
	orderID := oeBuy(t, e, ctx, buyer, planID, priceID)
	outNo, cents := oePay(t, e, ctx, buyer, orderID, code)
	ack, err := e.payments.HandleWebhook(ctx, code, oeEpayInbound("pay-"+randHex(8), outNo, cents))
	if err != nil || !oeAckOK(ack) {
		t.Fatalf("回调 %s %v", ack.Body, err)
	}
	var review bool
	var reason sql.NullString
	var estatus string
	if err := e.row(ctx, `SELECT review_required, review_reason, status FROM commission_entries WHERE order_id = ?`, orderID).
		Scan(&review, &reason, &estatus); err != nil {
		t.Fatal(err)
	}
	if !review || reason.String != reviewSameSource || estatus != EntryPending {
		t.Fatalf("同源复核: review=%v reason=%v status=%s", review, reason, estatus)
	}
}
