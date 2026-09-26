// [INPUT]: 依赖 checkout.go 的 CreateOrder/HandlePaymentWebhook、renewal.go 的 CreateRenewal、plan_change.go 的 CreatePlanChange、manual_order.go 的 MarkOrderPaid、release.go 的 CancelOrder、reservation_expiry.go 的 ExpireDueReservations、late_payment.go 的 ApplyLatePaymentToBalance、unexpected_payment.go 的 quarantineUnexpectedPayment，复用 order_release_pg18_test.go 的一次性租户夹具与挂账断言，依赖迁移 00095
// [OUTPUT]: 对外提供 TestIneligibleSubscriptionSettlementPG18（run-pg18-gates.sh 的 plan_change 域）
// [POS]: billing 订阅终态结算（R117）的 PG18 集成门禁：续费 / 变更单待支付期间订阅被改成 expired 或 cancelled 时，渠道回调与后台标记已付的钱进挂账、订单与订阅不动、回执成功；订单之后照常取消或过期、退回余额冻结，挂账能转入余额；数据库守卫拒绝把仍可续的订阅或新购单归为这类挂账
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

// TestIneligibleSubscriptionSettlementPG18 证明 R117：订阅在支付窗口里被改成终态
// （产品里没有这条路径，只有手工 SQL 能造出来），结算不再撞状态机整笔回滚——
// 钱按 ineligible_subscription 隔离进挂账，其余一概不动。
func TestIneligibleSubscriptionSettlementPG18(t *testing.T) {
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
	notified := 0
	service.SetUsersChangedNotifier(func(context.Context, string) { notified++ })
	defer service.SetUsersChangedNotifier(nil)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	operator := uuid.NewString()
	must(`INSERT INTO users(id,tenant_id,email,display_name,status)
		VALUES($1,$2,$3,'Ineligible Operator','active')`,
		operator, fx.tenant, "ine-operator-"+fx.suffix[:8]+"@example.test")
	// 两个可互相变更、不限量的套餐
	seedPlan := func(code string, price int64) (string, string) {
		t.Helper()
		product, plan, version, priceID := uuid.NewString(), uuid.NewString(),
			uuid.NewString(), uuid.NewString()
		must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($1,$2,$3,$3,'active')`,
			product, fx.tenant, code+"-"+fx.suffix[:8])
		must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status,allow_upgrade)
			VALUES($1,$2,$3,$4,$4,'draft',true)`, plan, fx.tenant, product,
			code+"-plan-"+fx.suffix[:8])
		must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`,
			version, fx.tenant, plan)
		must(`UPDATE plan_versions SET frozen_at=now(),status='published' WHERE id=$1`, version)
		must(`UPDATE plans SET current_version_id=$2,status='active' WHERE id=$1`, plan, version)
		must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,
			interval_count,status) VALUES($1,$2,$3,'CNY',$4,'month',1,'active')`,
			priceID, fx.tenant, product, price)
		return plan, priceID
	}
	planA, priceA := seedPlan("ine-basic", 1000)
	planB, priceB := seedPlan("ine-pro", 3000)

	webhook := func(eventLabel, paymentLabel, orderID string, amount int64) (*PaymentWebhookOutput, error) {
		return service.HandlePaymentWebhook(ctx, fx.tenant, PaymentWebhookInput{
			ProviderCode: fx.providerCode, ProviderEventID: eventLabel + "-event-" + fx.suffix,
			ProviderPaymentID: paymentLabel + "-payment-" + fx.suffix, EventType: "payment.succeeded",
			OrderID: orderID, Amount: amount, Currency: "CNY",
			RawPayload: map[string]any{"fixture": "ineligible-subscription"}, SignatureVerified: true,
		})
	}
	buy := func(t *testing.T, label string) string {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, CheckoutIdempotencyScope, label)
		order, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
			UserID: fx.buyer, PlanID: planA, PriceID: priceA, Claim: claim,
		})
		if err != nil {
			t.Fatalf("buy %s: %v", label, err)
		}
		out, err := webhook(label, label, order.OrderID, 1000)
		if err != nil || out == nil || !out.Processed || out.SubscriptionID == "" {
			t.Fatalf("settle purchase %s out=%#v err=%v", label, out, err)
		}
		return out.SubscriptionID
	}
	renew := func(t *testing.T, label, subID string, useBalance int64) *CreateOrderOutput {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, RenewalIdempotencyScope, label)
		order, err := service.CreateRenewal(ctx, fx.tenant, CreateRenewalInput{
			UserID: fx.buyer, SubscriptionID: subID, UseBalance: useBalance, Claim: claim,
		})
		if err != nil || order.Status != "pending_payment" || order.PayableAmount != 1000-useBalance {
			t.Fatalf("renewal %s=%+v err=%v", label, order, err)
		}
		return order
	}
	type subSnap struct {
		status, plan string
		end          time.Time
	}
	readSub := func(t *testing.T, subID string) subSnap {
		t.Helper()
		var s subSnap
		if err := admin.QueryRow(ctx, `SELECT status, plan_id::text, current_period_end
			FROM subscriptions WHERE id=$1::uuid`, subID).Scan(&s.status, &s.plan, &s.end); err != nil {
			t.Fatalf("read subscription: %v", err)
		}
		return s
	}
	// 订阅改成终态之后，除状态外一概不许变
	wantSubUntouched := func(t *testing.T, subID string, before subSnap, status string) {
		t.Helper()
		got := readSub(t, subID)
		if got.status != status || got.plan != before.plan || !got.end.Equal(before.end) {
			t.Fatalf("subscription touched by quarantined settlement: %+v before=%+v want status %s",
				got, before, status)
		}
	}
	wantNoRevenue := func(t *testing.T, orderID string) {
		t.Helper()
		var postings int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions
			WHERE source_type='order' AND source_id=$1::uuid AND kind='order_paid'`,
			orderID).Scan(&postings); err != nil || postings != 0 {
			t.Fatalf("quarantined order recognised revenue postings=%d err=%v", postings, err)
		}
	}
	wantQuarantined := func(t *testing.T, step string, out *PaymentWebhookOutput, err error) {
		t.Helper()
		if err != nil || out == nil || !out.Processed || out.AlreadyHandled ||
			out.QuarantineKind != "ineligible_subscription" || out.SubscriptionID != "" ||
			out.PaymentID == "" || out.LedgerTxnID == "" {
			t.Fatalf("%s out=%#v err=%v", step, out, err)
		}
	}
	wantConflict := func(t *testing.T, step string, err error, fragment string) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeConflict || !strings.Contains(he.Message, fragment) {
			t.Fatalf("%s err=%v want 409 containing %q", step, err, fragment)
		}
	}
	lateCase := func(t *testing.T, orderID string) (string, string) {
		t.Helper()
		var id, status string
		if err := admin.QueryRow(ctx, `SELECT id::text, status FROM late_payment_cases
			WHERE order_id=$1::uuid`, orderID).Scan(&id, &status); err != nil {
			t.Fatalf("read late payment case: %v", err)
		}
		return id, status
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

	// 1) 续费单（余额抵 300、渠道付 700）待支付期间订阅被改成 expired：
	//    钱进挂账、订单与订阅不动、回执成功、不通知节点。
	subExpired := buy(t, "ine-exp-buy")
	orderReleasePG18FundBalance(t, ctx, pool, fx.tenant, fx.buyer, 300)
	expiredRenewal := renew(t, "ine-exp", subExpired, 300)
	before := readSub(t, subExpired)
	must(`UPDATE subscriptions SET status='expired' WHERE id=$1::uuid`, subExpired)
	walletHeld := balance(t)
	notified = 0
	out, err := webhook("ine-exp", "ine-exp", expiredRenewal.OrderID, 700)
	wantQuarantined(t, "expired renewal", out, err)
	if notified != 0 {
		t.Fatalf("quarantined renewal notified nodes %d times", notified)
	}
	orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, expiredRenewal.OrderID,
		"ine-exp-payment-"+fx.suffix, fx.providerCode, "ineligible_subscription", 700, 0, 1, 0)
	orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, expiredRenewal.OrderID, "pending_payment")
	wantSubUntouched(t, subExpired, before, "expired")
	wantNoRevenue(t, expiredRenewal.OrderID)
	if got := balance(t); got != walletHeld {
		t.Fatalf("quarantine moved the held balance: %d want %d", got, walletHeld)
	}
	// 同一事件重投、同一笔收款换事件号重投：都按已处理回执，不多记一分钱
	if out, err := webhook("ine-exp", "ine-exp", expiredRenewal.OrderID, 700); err != nil ||
		out == nil || !out.AlreadyHandled {
		t.Fatalf("same-event replay out=%#v err=%v", out, err)
	}
	if out, err := webhook("ine-exp-replay", "ine-exp", expiredRenewal.OrderID, 700); err != nil ||
		out == nil || !out.AlreadyHandled {
		t.Fatalf("same-payment new-event replay out=%#v err=%v", out, err)
	}
	orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, expiredRenewal.OrderID,
		"ine-exp-payment-"+fx.suffix, fx.providerCode, "ineligible_subscription", 700, 0, 1, 1)
	t.Log("marker=ineligible_pg18_expired_renewal_quarantined_ok")

	// 订单照常可取消：余额冻结退回、挂账留着；转入余额后 1000 全部回到用户手里
	if _, err := service.CancelOrder(ctx, fx.tenant, fx.buyer, expiredRenewal.OrderID); err != nil {
		t.Fatalf("cancel quarantined renewal: %v", err)
	}
	orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, expiredRenewal.OrderID, "cancelled")
	if got := balance(t); got != walletHeld+300 {
		t.Fatalf("cancelled renewal returned balance %d want %d", got, walletHeld+300)
	}
	caseID, caseStatus := lateCase(t, expiredRenewal.OrderID)
	if caseStatus != "suspense" {
		t.Fatalf("late case status after release=%s", caseStatus)
	}
	if _, err := service.ApplyLatePaymentToBalance(ctx, fx.tenant, ApplyLatePaymentInput{
		CaseID: caseID, ActorID: operator, Reason: "订阅已结束，续费款转入余额",
	}); err != nil {
		t.Fatalf("apply ineligible late payment: %v", err)
	}
	if got := balance(t); got != walletHeld+1000 {
		t.Fatalf("applied late payment balance %d want %d", got, walletHeld+1000)
	}
	if _, status := lateCase(t, expiredRenewal.OrderID); status != "applied" {
		t.Fatalf("late case status after apply=%s", status)
	}
	wantSubUntouched(t, subExpired, before, "expired")
	t.Log("marker=ineligible_pg18_release_and_apply_ok")

	// 2) cancelled：同样进挂账；订单由过期扫描照常释放。
	subCancelled := buy(t, "ine-can-buy")
	cancelledRenewal := renew(t, "ine-can", subCancelled, 0)
	before = readSub(t, subCancelled)
	must(`UPDATE subscriptions SET status='cancelled' WHERE id=$1::uuid`, subCancelled)
	out, err = webhook("ine-can", "ine-can", cancelledRenewal.OrderID, 1000)
	wantQuarantined(t, "cancelled renewal", out, err)
	orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, cancelledRenewal.OrderID,
		"ine-can-payment-"+fx.suffix, fx.providerCode, "ineligible_subscription", 1000, 0, 1, 0)
	orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, cancelledRenewal.OrderID, "pending_payment")
	wantSubUntouched(t, subCancelled, before, "cancelled")
	wantNoRevenue(t, cancelledRenewal.OrderID)
	orderReleasePG18Backdate(t, ctx, admin, fx.tenant, cancelledRenewal.OrderID)
	if n, err := service.ExpireDueReservations(ctx, fx.tenant, 10); err != nil || n < 1 {
		t.Fatalf("expire quarantined renewal n=%d err=%v", n, err)
	}
	orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, cancelledRenewal.OrderID, "expired")
	if _, status := lateCase(t, cancelledRenewal.OrderID); status != "suspense" {
		t.Fatalf("late case status after expiry=%s", status)
	}
	t.Log("marker=ineligible_pg18_cancelled_renewal_quarantined_ok")

	// 3) 变更套餐单：订阅 expired → 补差款进挂账，套餐不换。
	subUp := buy(t, "ine-up-buy")
	claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, PlanChangeIdempotencyScope, "ine-up")
	change, err := service.CreatePlanChange(ctx, fx.tenant, PlanChangeInput{
		UserID: fx.buyer, SubscriptionID: subUp, PlanID: planB, PriceID: priceB, Claim: claim,
	})
	if err != nil || change.Status != "pending_payment" || change.PayableAmount <= 0 {
		t.Fatalf("pending plan change=%+v err=%v", change, err)
	}
	before = readSub(t, subUp)
	must(`UPDATE subscriptions SET status='expired' WHERE id=$1::uuid`, subUp)
	out, err = webhook("ine-up", "ine-up", change.OrderID, change.PayableAmount)
	wantQuarantined(t, "expired plan change", out, err)
	orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, change.OrderID,
		"ine-up-payment-"+fx.suffix, fx.providerCode, "ineligible_subscription",
		change.PayableAmount, 0, 1, 0)
	orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, change.OrderID, "pending_payment")
	wantSubUntouched(t, subUp, before, "expired")
	if before.plan != planA {
		t.Fatalf("plan change fixture bought plan %s want %s", before.plan, planA)
	}
	wantNoRevenue(t, change.OrderID)
	t.Log("marker=ineligible_pg18_plan_change_quarantined_ok")

	// 4) 后台标记已付：入账进挂账并留审计，接口回 409 说明去向；同一凭证再标一次
	//    回 409，不多记一笔。
	subMark := buy(t, "ine-mark-buy")
	markRenewal := renew(t, "ine-mark", subMark, 0)
	before = readSub(t, subMark)
	must(`UPDATE subscriptions SET status='cancelled' WHERE id=$1::uuid`, subMark)
	reference := "BANK-" + fx.suffix[:8]
	_, err = service.MarkOrderPaid(ctx, fx.tenant, MarkOrderPaidInput{
		OrderID: markRenewal.OrderID, ActorID: operator, Reason: "客户银行转账续费", Reference: reference,
	})
	wantConflict(t, "mark-paid on cancelled subscription", err, "订阅已结束，款项已转入挂账")
	orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, markRenewal.OrderID,
		"offline:"+reference, OfflineProviderCode, "ineligible_subscription", 1000, 0, 1, 0)
	var markAudits int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE tenant_id=$1 AND action='order.marked_paid' AND resource_id=$2::uuid
		  AND after_digest->>'quarantined'='ineligible_subscription'
		  AND after_digest->>'reference'=$3`,
		fx.tenant, markRenewal.OrderID, reference).Scan(&markAudits); err != nil || markAudits != 1 {
		t.Fatalf("mark-paid quarantine audits=%d err=%v", markAudits, err)
	}
	_, err = service.MarkOrderPaid(ctx, fx.tenant, MarkOrderPaidInput{
		OrderID: markRenewal.OrderID, ActorID: operator, Reason: "客户银行转账续费", Reference: reference,
	})
	wantConflict(t, "mark-paid replay", err, "已经入过账")
	orderReleasePG18AssertPaymentShape(t, ctx, pool, fx.tenant, markRenewal.OrderID, 1, 1)
	orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, markRenewal.OrderID, "pending_payment")
	wantSubUntouched(t, subMark, before, "cancelled")
	wantNoRevenue(t, markRenewal.OrderID)
	t.Log("marker=ineligible_pg18_mark_paid_conflict_ok")

	// 5) 守卫：订阅仍可续时，数据库拒绝把续费款归为 ineligible_subscription；
	//    新购单也不能。
	forge := func(t *testing.T, label, orderID string) error {
		t.Helper()
		return pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
			var eventID string
			if err := tx.QueryRow(ctx, `INSERT INTO payment_events
				(tenant_id,provider_id,provider_event_id,event_type,provider_payment_id,
				 raw_payload,signature_verified)
				VALUES ($1,$2::uuid,$3,'payment.succeeded',$4,'{}'::jsonb,true)
				RETURNING id::text`, fx.tenant, fx.provider, label+"-event-"+fx.suffix,
				label+"-payment-"+fx.suffix).Scan(&eventID); err != nil {
				return err
			}
			_, err := service.quarantineUnexpectedPayment(ctx, tx, fx.tenant, eventID,
				fx.provider, orderID, fx.buyer, "pending_payment", fx.providerCode,
				"ineligible_subscription", PaymentWebhookInput{
					ProviderPaymentID: label + "-payment-" + fx.suffix,
					Currency:          "CNY", Amount: 1000,
				})
			return err
		})
	}
	subActive := buy(t, "ine-guard-buy")
	activeRenewal := renew(t, "ine-guard", subActive, 0)
	if state := orderReleasePG18SQLState(forge(t, "ine-forge-renew", activeRenewal.OrderID)); state != "23514" {
		t.Fatalf("forged ineligible case on renewable subscription SQLSTATE=%q", state)
	}
	claim = orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, CheckoutIdempotencyScope, "ine-guard-new")
	pendingNew, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
		UserID: fx.buyer, PlanID: planA, PriceID: priceA, Claim: claim,
	})
	if err != nil || pendingNew.Status != "pending_payment" {
		t.Fatalf("pending new order=%+v err=%v", pendingNew, err)
	}
	if state := orderReleasePG18SQLState(forge(t, "ine-forge-new", pendingNew.OrderID)); state != "23514" {
		t.Fatalf("forged ineligible case on a new order SQLSTATE=%q", state)
	}
	orderReleasePG18AssertPaymentShape(t, ctx, pool, fx.tenant, activeRenewal.OrderID, 0, 0)
	orderReleasePG18AssertPaymentShape(t, ctx, pool, fx.tenant, pendingNew.OrderID, 0, 0)
	// 守卫之外的默认行为不变：订阅可续时同一张续费单照常履约
	if out, err := webhook("ine-guard", "ine-guard", activeRenewal.OrderID, 1000); err != nil ||
		out == nil || !out.Processed || out.QuarantineKind != "" || out.SubscriptionID != subActive {
		t.Fatalf("eligible renewal out=%#v err=%v", out, err)
	}
	t.Log("marker=ineligible_pg18_guard_ok")
}
