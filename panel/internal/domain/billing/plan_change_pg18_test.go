// [INPUT]: 依赖 plan_change.go 的 PreviewPlanChange/CreatePlanChange、checkout.go 的 CreateOrder、settlement.go 的 HandlePaymentWebhook、renewal.go 的 CreateRenewal、release.go 的 CancelOrder，复用 order_release_pg18_fixture_test.go 的一次性租户夹具与幂等键工具，依赖迁移 00071
// [OUTPUT]: 对外提供 TestPlanChangePG18（run-pg18-gates.sh 的 plan_change 域）
// [POS]: billing 变更套餐的 PG18 集成门禁：真实 SQL 下的折算基数、补差价结算、降级退余额、试算回券面（R76）、零元变更与续费建单即通知节点、续费单待支付期间周期走完仍能履约（expired → active 推断未复现）、与续费互斥、释放与数据库守卫；纯算术边界在 plan_change_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// TestPlanChangePG18 证明变更套餐（D-E-2，迁移 00071）：
//   - 升级：剩余价值取时间比与流量比中较小的（这里流量更小），补差价走外部支付，
//     履约原地换套餐、重建配额、凭据不换、留重置日志与 plan_changed 事件；
//   - 在途变更单与续费互斥，取消后订阅原样不动；
//   - 降级：带优惠券，剩余价值抵完新价当场履约，差额退进余额且有账本分录；
//   - 叠加续费后的折算基数是本周期付费合计；
//   - 同套餐、不允许变更的套餐被拒；数据库拒绝伪造的变更证据。
func TestPlanChangePG18(t *testing.T) {
	appDSN := os.Getenv("AEGIS_PLAN_CHANGE_PG18_DSN")
	adminDSN := os.Getenv("AEGIS_PLAN_CHANGE_PG18_ADMIN_DSN")
	if appDSN == "" || adminDSN == "" {
		t.Skip("AEGIS_PLAN_CHANGE_PG18_DSN and AEGIS_PLAN_CHANGE_PG18_ADMIN_DSN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	pool, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open aegis_app pool: %v", err)
	}
	defer pool.Close()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	defer admin.Close(ctx)
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil ||
		database != os.Getenv("AEGIS_PLAN_CHANGE_PG18_DATABASE") {
		t.Fatalf("refusing unexpected fixture database=%q err=%v", database, err)
	}
	orderReleasePG18AssertRuntimeTarget(t, ctx, pool, admin)

	fx := orderReleasePG18Seed(t, ctx, admin)
	service := NewService(pool, nil)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	// seedPlan 建一个已发布、可购的套餐：traffic 为 0 表示不限量（没有流量配额行）。
	seedPlan := func(code string, price, traffic int64, allowUpgrade bool) (string, string) {
		t.Helper()
		product, plan, version, priceID := uuid.NewString(), uuid.NewString(),
			uuid.NewString(), uuid.NewString()
		must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($1,$2,$3,$3,'active')`,
			product, fx.tenant, code+"-"+fx.suffix[:8])
		must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status,allow_upgrade)
			VALUES($1,$2,$3,$4,$4,'draft',$5)`, plan, fx.tenant, product,
			code+"-plan-"+fx.suffix[:8], allowUpgrade)
		must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`,
			version, fx.tenant, plan)
		if traffic > 0 {
			must(`INSERT INTO quota_definitions(tenant_id,plan_version_id,metric,limit_value,unit,period)
				VALUES($1,$2,'traffic.bytes',$3,'bytes','cycle')`, fx.tenant, version, traffic)
		}
		must(`UPDATE plan_versions SET frozen_at=now(),status='published' WHERE id=$1`, version)
		must(`UPDATE plans SET current_version_id=$2,status='active' WHERE id=$1`, plan, version)
		must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,
			interval_count,status) VALUES($1,$2,$3,'CNY',$4,'month',1,'active')`,
			priceID, fx.tenant, product, price)
		return plan, priceID
	}
	planA, priceA := seedPlan("pc-basic", 1000, 1000, true)
	planB, priceB := seedPlan("pc-pro", 3000, 1000, true)
	planC, priceC := seedPlan("pc-lite", 200, 0, true)
	planD, priceD := seedPlan("pc-locked", 5000, 0, false)
	couponC := "PCLITE-" + fx.suffix[:8]
	must(`INSERT INTO coupons(id,tenant_id,code,name,discount_type,discount_value,currency,
		applicable_plan_ids,max_redemptions,max_redemptions_per_user,status)
		VALUES($1,$2,$3,'Plan change coupon','fixed',50,'CNY',ARRAY[$4::uuid],10,10,'active')`,
		uuid.NewString(), fx.tenant, couponC, planC)

	pay := func(t *testing.T, label, orderID string, amount int64) *PaymentWebhookOutput {
		t.Helper()
		out, err := service.HandlePaymentWebhook(ctx, fx.tenant, PaymentWebhookInput{
			ProviderCode: fx.providerCode, ProviderEventID: label + "-event-" + fx.suffix,
			ProviderPaymentID: label + "-payment-" + fx.suffix, EventType: "payment.succeeded",
			OrderID: orderID, Amount: amount, Currency: "CNY",
			RawPayload: map[string]any{"fixture": "plan-change"}, SignatureVerified: true,
		})
		if err != nil || out == nil || !out.Processed {
			t.Fatalf("settle %s output=%#v err=%v", label, out, err)
		}
		return out
	}
	wantConflict := func(t *testing.T, step string, err error, fragment string) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeConflict || !strings.Contains(he.Message, fragment) {
			t.Fatalf("%s err=%v want 409 containing %q", step, err, fragment)
		}
	}
	type subState struct {
		plan, price, status string
		start, end          time.Time
		tokenHash           string
		credExpires         time.Time
	}
	readSub := func(t *testing.T, subID string) subState {
		t.Helper()
		var s subState
		if err := admin.QueryRow(ctx, `
			SELECT s.plan_id::text, s.price_id::text, s.status, s.current_period_start,
			       s.current_period_end, encode(c.token_hash, 'hex'), c.expires_at
			  FROM subscriptions s
			  JOIN subscription_credentials c ON c.subscription_id = s.id AND c.status = 'active'
			 WHERE s.id = $1::uuid`, subID).Scan(&s.plan, &s.price, &s.status, &s.start,
			&s.end, &s.tokenHash, &s.credExpires); err != nil {
			t.Fatalf("read subscription: %v", err)
		}
		return s
	}
	balance := func(t *testing.T) int64 {
		t.Helper()
		var amount int64
		orderReleasePG18InTx(t, ctx, pool, fx.tenant, func(tx pgx.Tx) error {
			id, err := EnsureAccount(ctx, tx, fx.tenant, AccountUserBalance, "CNY", &fx.buyer, "")
			if err != nil {
				return err
			}
			amount, err = Balance(ctx, tx, id)
			return err
		})
		return amount
	}

	// 0) 买基础版（1000/月，1000 字节流量），用掉 250。
	claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, CheckoutIdempotencyScope, "pc-buy-a")
	bought, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
		UserID: fx.buyer, PlanID: planA, PriceID: priceA, Claim: claim,
	})
	if err != nil {
		t.Fatalf("buy plan A: %v", err)
	}
	subID := pay(t, "pc-buy-a", bought.OrderID, 1000).SubscriptionID
	must(`UPDATE quota_balances SET consumed = 250 WHERE subscription_id = $1::uuid`, subID)
	before := readSub(t, subID)
	changeTo := func(t *testing.T, label, planID, priceID, coupon string, useBalance int64) (*PlanChangeOrderOutput, error) {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer,
			PlanChangeIdempotencyScope, label)
		return service.CreatePlanChange(ctx, fx.tenant, PlanChangeInput{
			UserID: fx.buyer, SubscriptionID: subID, PlanID: planID, PriceID: priceID,
			UseBalance: useBalance, CouponCode: coupon, Claim: claim,
		})
	}
	noSubClaim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer,
		PlanChangeIdempotencyScope, "pc-no-sub")
	if _, err := service.CreatePlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, PlanID: planB, PriceID: priceB, Claim: noSubClaim,
	}); err == nil {
		t.Fatal("plan change without a subscription id must be rejected")
	}

	// 1) 升级到专业版（3000）：时间几乎没过，流量剩 750/1000 更小 → 剩余价值 750。
	preview, err := service.PreviewPlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, SubscriptionID: subID, PlanID: planB, PriceID: priceB,
	})
	if err != nil || preview.Direction != "upgrade" || preview.ProrationCredit != 750 ||
		preview.Total != 2250 || preview.BalanceRefund != 0 || preview.Subtotal != 3000 || preview.CouponFace != nil {
		t.Fatalf("upgrade preview=%+v err=%v", preview, err)
	}
	pending, err := changeTo(t, "pc-up-cancel", planB, priceB, "", 0)
	if err != nil || pending.Status != "pending_payment" || pending.PayableAmount != 2250 ||
		pending.ProrationCredit != 750 {
		t.Fatalf("pending upgrade=%+v err=%v", pending, err)
	}
	t.Log("marker=plan_change_pg18_traffic_bound_credit_ok")

	// 2) 在途变更单与续费、第二张变更单互斥；取消后订阅原样。
	renewClaim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, RenewalIdempotencyScope, "pc-renew-blocked")
	_, err = service.CreateRenewal(ctx, fx.tenant, CreateRenewalInput{
		UserID: fx.buyer, SubscriptionID: subID, Claim: renewClaim,
	})
	wantConflict(t, "renewal during pending plan change", err, "未完成的续费或变更")
	_, err = changeTo(t, "pc-up-second", planB, priceB, "", 0)
	wantConflict(t, "second plan change", err, "未完成的续费或变更")
	if _, err := service.CancelOrder(ctx, fx.tenant, fx.buyer, pending.OrderID); err != nil {
		t.Fatalf("cancel pending upgrade: %v", err)
	}
	if got := readSub(t, subID); got.plan != before.plan || got.price != before.price ||
		got.status != before.status || !got.start.Equal(before.start) ||
		!got.end.Equal(before.end) || got.tokenHash != before.tokenHash {
		t.Fatalf("cancelled upgrade touched the subscription: %+v vs %+v", got, before)
	}
	t.Log("marker=plan_change_pg18_mutual_exclusion_and_release_ok")

	// 3) 再下单并付款：原地换套餐、配额重建、凭据不换。
	upgrade, err := changeTo(t, "pc-up-paid", planB, priceB, "", 0)
	if err != nil || upgrade.PayableAmount != 2250 {
		t.Fatalf("upgrade order=%+v err=%v", upgrade, err)
	}
	if got := pay(t, "pc-up-paid", upgrade.OrderID, 2250).SubscriptionID; got != subID {
		t.Fatalf("upgrade settled onto subscription %s want %s", got, subID)
	}
	after := readSub(t, subID)
	if after.plan != planB || after.price != priceB || after.status != "active" ||
		after.tokenHash != before.tokenHash || !after.start.After(before.start) ||
		!after.credExpires.Equal(after.end) ||
		!after.end.Equal(after.start.UTC().AddDate(0, 1, 0)) {
		t.Fatalf("after upgrade subscription=%+v before=%+v", after, before)
	}
	var (
		consumed, limit   int64
		resetBefore, evts int
	)
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT consumed FROM quota_balances WHERE subscription_id=$1::uuid AND metric='traffic.bytes'),
		       (SELECT limit_value FROM quota_balances WHERE subscription_id=$1::uuid AND metric='traffic.bytes'),
		       (SELECT count(*) FROM traffic_reset_logs WHERE subscription_id=$1::uuid
		           AND reason='plan_change' AND consumed_before=250),
		       (SELECT count(*) FROM subscription_events WHERE subscription_id=$1::uuid
		           AND event_type='plan_changed' AND order_id=$2::uuid)`,
		subID, upgrade.OrderID).Scan(&consumed, &limit, &resetBefore, &evts); err != nil {
		t.Fatalf("read upgrade evidence: %v", err)
	}
	if consumed != 0 || limit != 1000 || resetBefore != 1 || evts != 1 {
		t.Fatalf("upgrade evidence consumed=%d limit=%d resets=%d events=%d",
			consumed, limit, resetBefore, evts)
	}
	t.Log("marker=plan_change_pg18_upgrade_settled_in_place_ok")

	// 4) 降级到轻量版（200，券减 50，不限量）：专业版流量用掉 400 → 剩余价值
	//    floor(3000 × 600/1000) = 1800，抵完 150 还剩 1650 退进余额，当场履约。
	must(`UPDATE quota_balances SET consumed = 400 WHERE subscription_id = $1::uuid`, subID)
	walletBefore := balance(t)
	// 试算回券面（R76），与优惠码试算同形
	if p, err := service.PreviewPlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, SubscriptionID: subID, PlanID: planC, PriceID: priceC, CouponCode: couponC,
	}); err != nil || p.Discount != 50 || p.CouponFace == nil ||
		*p.CouponFace != (CouponFace{Code: strings.ToUpper(couponC), DiscountType: "fixed", DiscountValue: 50}) {
		t.Fatalf("downgrade preview with coupon=%+v err=%v", p, err)
	}
	// 零元变更与零元续费在建单事务里就履约，不经过支付回调：各通知节点一次
	notified := 0
	service.SetUsersChangedNotifier(func(context.Context, string) { notified++ })
	defer service.SetUsersChangedNotifier(nil)
	downgrade, err := changeTo(t, "pc-down", planC, priceC, couponC, 0)
	if err != nil || downgrade.Status != "fulfilled" || downgrade.TotalAmount != 0 ||
		downgrade.DiscountAmount != 50 || downgrade.ProrationCredit != 1800 ||
		downgrade.BalanceRefund != 1650 {
		t.Fatalf("downgrade=%+v err=%v", downgrade, err)
	}
	if notified != 1 {
		t.Fatalf("zero-pay downgrade notified nodes %d times, want 1", notified)
	}
	if got := balance(t) - walletBefore; got != 1650 {
		t.Fatalf("downgrade refunded %d to balance want 1650", got)
	}
	// 轻量版不限量：流量配额行保留（人工调整只许追加，行不能删），上限置空即不限量。
	var limitedRows, refundTxns int
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM quota_balances WHERE subscription_id=$1::uuid
		           AND metric='traffic.bytes' AND (limit_value IS NOT NULL OR consumed <> 0)),
		       (SELECT count(*) FROM ledger_transactions WHERE source_type='order'
		           AND source_id=$2::uuid AND kind='plan_change_refund')`,
		subID, downgrade.OrderID).Scan(&limitedRows, &refundTxns); err != nil {
		t.Fatalf("read downgrade evidence: %v", err)
	}
	if limitedRows != 0 || refundTxns != 1 || readSub(t, subID).plan != planC {
		t.Fatalf("downgrade evidence limited traffic rows=%d refund txns=%d", limitedRows, refundTxns)
	}
	t.Log("marker=plan_change_pg18_downgrade_refunds_balance_ok")

	// 5) 叠一张续费（用余额付清）：折算基数 = 本周期付费合计 150 + 200 = 350，
	//    付费时长两个月，刚过去几秒 → floor(350 × (1 − ε)) = 349。
	renewClaim = orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, RenewalIdempotencyScope, "pc-renew-c")
	renewed, err := service.CreateRenewal(ctx, fx.tenant, CreateRenewalInput{
		UserID: fx.buyer, SubscriptionID: subID, UseBalance: 200, Claim: renewClaim,
	})
	if err != nil || renewed.Status != "fulfilled" || notified != 2 {
		t.Fatalf("renew plan C=%+v notified=%d err=%v", renewed, notified, err)
	}
	stacked, err := service.PreviewPlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, SubscriptionID: subID, PlanID: planB, PriceID: priceB,
	})
	if err != nil || stacked.ProrationCredit != 349 || stacked.Total != 3000-349 {
		t.Fatalf("stacked-period preview=%+v err=%v", stacked, err)
	}
	t.Log("marker=plan_change_pg18_stacked_period_basis_ok")

	// 6) 同套餐、不允许变更的套餐被拒。
	_, err = service.PreviewPlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, SubscriptionID: subID, PlanID: planC, PriceID: priceC,
	})
	wantConflict(t, "same plan", err, "请使用续费")
	_, err = service.PreviewPlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, SubscriptionID: subID, PlanID: planD, PriceID: priceD,
	})
	wantConflict(t, "upgrade disallowed", err, "不允许变更")

	// 7) 数据库守卫：非变更单不能挂 plan_changed 事件或退余额分录。
	forgedEvent := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, actor_kind, order_id)
			VALUES ($1, $2::uuid, 'plan_changed', 'system', $3::uuid)`,
			fx.tenant, subID, bought.OrderID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	})
	if state := orderReleasePG18SQLState(forgedEvent); state != "23514" {
		t.Fatalf("forged plan_changed event SQLSTATE=%q err=%v", state, forgedEvent)
	}
	forgedRefund := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
		accounts, err := prepareAndLockLedgerAccounts(ctx, tx, fx.tenant, []ledgerAccountSpec{
			{Key: "revenue", AccountType: AccountPlatformRevenue, Currency: "CNY", OwnerRef: "main"},
			{Key: "balance", AccountType: AccountUserBalance, Currency: "CNY", UserID: &fx.buyer},
		})
		if err != nil {
			return err
		}
		orderID := bought.OrderID
		if _, err := Post(ctx, tx, fx.tenant, Posting{
			Kind: "plan_change_refund", Currency: "CNY", SourceType: "order", SourceID: &orderID,
			ActorKind: "system", Entries: []Entry{
				{AccountID: accounts["revenue"], Direction: Debit, Amount: 1},
				{AccountID: accounts["balance"], Direction: Credit, Amount: 1},
			},
		}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	})
	if state := orderReleasePG18SQLState(forgedRefund); state != "23514" {
		t.Fatalf("forged plan change refund SQLSTATE=%q err=%v", state, forgedRefund)
	}
	t.Log("marker=plan_change_pg18_guards_ok")

	// 8) 第 3 阶段后端一的推断「续费单待支付的 30 分钟里订阅过期，状态机不许 expired → active，
	//    履约失败」按产品真实路径复现：代码里没有任何地方把订阅状态改成 expired，到期只是
	//    current_period_end 走过去、状态仍是 active。付款照常履约；新周期的起点从付款时刻算
	//    （修前起点留在旧周期，断掉的那段被算进本周期）。
	renewClaim = orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, RenewalIdempotencyScope, "pc-renew-lapse")
	lapse, err := service.CreateRenewal(ctx, fx.tenant, CreateRenewalInput{
		UserID: fx.buyer, SubscriptionID: subID, Claim: renewClaim,
	})
	if err != nil || lapse.Status != "pending_payment" || lapse.PayableAmount != 200 {
		t.Fatalf("pending renewal=%+v err=%v", lapse, err)
	}
	must(`UPDATE subscriptions SET current_period_start = now() - interval '31 days',
		current_period_end = now() - interval '1 minute' WHERE id = $1::uuid`, subID)
	must(`UPDATE subscription_credentials SET expires_at = now() - interval '1 minute' WHERE subscription_id = $1::uuid`, subID)
	paidAt := time.Now().UTC().Add(-time.Second)
	pay(t, "pc-renew-lapse", lapse.OrderID, 200)
	var lapsedFrom string
	if err := admin.QueryRow(ctx, `SELECT from_status FROM subscription_events
		WHERE subscription_id=$1::uuid AND order_id=$2::uuid AND event_type='renewed'`,
		subID, lapse.OrderID).Scan(&lapsedFrom); err != nil {
		t.Fatalf("read lapse renewal event: %v", err)
	}
	if got := readSub(t, subID); got.status != "active" || got.start.Before(paidAt) ||
		!got.end.Equal(got.start.UTC().AddDate(0, 1, 0)) || !got.credExpires.Equal(got.end) || lapsedFrom != "active" {
		t.Fatalf("renewal after lapse subscription=%+v from=%s paid_after=%s", got, lapsedFrom, paidAt)
	}
	t.Log("marker=plan_change_pg18_renewal_after_lapse_ok")
}
