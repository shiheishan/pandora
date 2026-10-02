// [INPUT]: 依赖 payment_query.go 的 QueryOrderPayment、payment_query_patrol.go 的 ReconcileDuePayments、settlement.go 的 HandlePaymentWebhook、checkout.go 的 CreateOrder、payments.go 的 CreatePaymentIntent、release.go 的 CancelOrder，复用 order_release_pg18_fixture_test.go 的一次性租户夹具与 payment_query_stub_test.go 的渠道替身，依赖迁移 00097
// [OUTPUT]: 对外提供 TestPaymentQueryPG18（run-pg18-gates.sh 的 payment_query 域）
// [POS]: billing 主动查单（PAY-009）的 PG18 集成门禁：查到已付补记、补记与回调谁先谁后都只入账一次、已取消订单查到的钱进挂账、金额币种不符整笔拒绝、渠道没付或没查成订单不动、业务错误；巡检只认领到期的单，两个巡检并发时同一单只查一次，渠道不支持查单即停
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestPaymentQueryPG18(t *testing.T) {
	appDSN := os.Getenv("AEGIS_PAYMENT_QUERY_PG18_DSN")
	adminDSN := os.Getenv("AEGIS_PAYMENT_QUERY_PG18_ADMIN_DSN")
	if appDSN == "" || adminDSN == "" {
		t.Skip("AEGIS_PAYMENT_QUERY_PG18_DSN and AEGIS_PAYMENT_QUERY_PG18_ADMIN_DSN are required")
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
		database != os.Getenv("AEGIS_PAYMENT_QUERY_PG18_DATABASE") {
		t.Fatalf("refusing unexpected fixture database=%q err=%v", database, err)
	}
	orderReleasePG18AssertRuntimeTarget(t, ctx, pool, admin)

	fx := orderReleasePG18Seed(t, ctx, admin)
	service := NewService(pool, nil)
	// 巡检在两个 goroutine 里补记，计数用原子量
	var notifiedCount atomic.Int64
	service.SetUsersChangedNotifier(func(context.Context, string) { notifiedCount.Add(1) })
	payments := NewPaymentService(service, pool, nil, []byte("payment-query-pg18-master-key-0"),
		"https://panel.example.test", true)
	stub := newQueryStubProvider(fx.providerCode)
	// 夹具的渠道登记为 demo_hmac；这个实例里把它换成可编排的替身，不打外网
	payments.Factory().RegisterAdapter("demo_hmac",
		func(payment.ProviderRecord) (payment.Provider, error) { return stub, nil })

	must := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("count: %v\nSQL: %s", err, sql)
		}
		return n
	}
	orderStatus := func(orderID string) string {
		t.Helper()
		var status string
		if err := admin.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1::uuid`, orderID).
			Scan(&status); err != nil {
			t.Fatalf("read order status: %v", err)
		}
		return status
	}
	// 每单一个新买家：套餐有每人限购，待支付单也占着限购预留
	newBuyer := func(t *testing.T, label string) string {
		t.Helper()
		id := uuid.NewString()
		must(t, `INSERT INTO users(id,tenant_id,email,display_name,status)
			VALUES($1,$2,$3,'Payment Query Buyer','active')`,
			id, fx.tenant, "pq-"+label+"-"+id[:8]+"@example.test")
		return id
	}
	type pendingOrder struct{ id, no, buyer string }
	newOrder := func(t *testing.T, label string, withIntent bool) pendingOrder {
		t.Helper()
		buyer := newBuyer(t, label)
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, buyer, CheckoutIdempotencyScope, "pq-"+label)
		order, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
			UserID: buyer, PlanID: fx.plan, PriceID: fx.price, Claim: claim,
		})
		if err != nil || order.Status != "pending_payment" || order.PayableAmount != 1000 {
			t.Fatalf("create order %s=%+v err=%v", label, order, err)
		}
		if withIntent {
			if _, err := payments.CreatePaymentIntent(ctx, fx.tenant, CreateIntentInput{
				OrderID: order.OrderID, UserID: buyer, ProviderCode: fx.providerCode,
			}); err != nil {
				t.Fatalf("create payment intent %s: %v", label, err)
			}
		}
		return pendingOrder{id: order.OrderID, no: order.OrderNo, buyer: buyer}
	}
	paid := func(ref string, amount int64, currency string) *payment.QueryResult {
		return &payment.QueryResult{Found: true, PaymentRef: ref, Amount: amount,
			Currency: currency, Status: payment.StatusSucceeded, Method: "alipay"}
	}
	query := func(o pendingOrder) (*OrderPaymentQuery, error) {
		return payments.QueryOrderPayment(ctx, fx.tenant, o.id, o.buyer)
	}
	wantHTTPError := func(t *testing.T, err error, code httpx.Code, message string) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != code || (message != "" && he.Message != message) {
			t.Fatalf("err=%v, want %s %q", err, code, message)
		}
	}
	paymentsOf := func(orderID string) int {
		return count(`SELECT count(*) FROM payments WHERE order_id=$1::uuid`, orderID)
	}
	orderPaidTxns := func(orderID string) int {
		return count(`SELECT count(*) FROM ledger_transactions
			WHERE source_type='order' AND source_id=$1::uuid AND kind='order_paid'`, orderID)
	}

	t.Run("paid order is reconciled once and a late callback is recognised", func(t *testing.T) {
		o := newOrder(t, "reconcile", true)
		ref := "pq-trade-reconcile-" + fx.suffix
		stub.answer(o.no, paid(ref, 1000, "CNY"), nil)
		before := notifiedCount.Load()

		res, err := query(o)
		if err != nil || res.ChannelStatus != ChannelPaid || !res.Reconciled || res.AlreadyRecorded ||
			res.QuarantineKind != "" || res.OrderStatus != "fulfilled" || res.ProviderCode != fx.providerCode {
			t.Fatalf("reconcile res=%+v err=%v", res, err)
		}
		if paymentsOf(o.id) != 1 || orderPaidTxns(o.id) != 1 || notifiedCount.Load() != before+1 ||
			count(`SELECT count(*) FROM orders WHERE id=$1::uuid AND subscription_id IS NOT NULL`, o.id) != 1 ||
			count(`SELECT count(*) FROM payment_intents WHERE order_id=$1::uuid AND status='succeeded'`, o.id) != 1 ||
			count(`SELECT count(*) FROM payment_events WHERE provider_event_id=$1
				AND processing_status='processed' AND signature_verified`, ref+":RECONCILED") != 1 {
			t.Fatalf("reconciled evidence missing (notified %d→%d)", before, notifiedCount.Load())
		}

		// 真实回调晚到：事件号不同、渠道流水号相同，认作已记过
		late, err := service.HandlePaymentWebhook(ctx, fx.tenant, PaymentWebhookInput{
			ProviderCode: fx.providerCode, ProviderEventID: ref + ":TRADE_SUCCESS",
			ProviderPaymentID: ref, EventType: "payment.succeeded", OrderNo: o.no,
			Amount: 1000, Currency: "CNY", RawPayload: map[string]any{"fixture": "late-callback"},
			SignatureVerified: true,
		})
		if err != nil || late == nil || !late.AlreadyHandled || late.Processed || late.PaymentID == "" {
			t.Fatalf("late callback out=%+v err=%v", late, err)
		}
		again, err := query(o)
		if err != nil || again.ChannelStatus != ChannelPaid || again.Reconciled || !again.AlreadyRecorded {
			t.Fatalf("repeat query res=%+v err=%v", again, err)
		}
		if paymentsOf(o.id) != 1 || orderPaidTxns(o.id) != 1 || notifiedCount.Load() != before+1 ||
			count(`SELECT count(*) FROM late_payment_cases WHERE order_id=$1::uuid`, o.id) != 0 ||
			count(`SELECT count(*) FROM subscriptions WHERE user_id=$1::uuid`, o.buyer) != 1 {
			t.Fatal("late callback or repeat query recorded the same money twice")
		}
	})

	t.Run("callback first then query is not recorded again", func(t *testing.T) {
		o := newOrder(t, "callback-first", true)
		ref := "pq-trade-callback-first-" + fx.suffix
		if out, err := service.HandlePaymentWebhook(ctx, fx.tenant, PaymentWebhookInput{
			ProviderCode: fx.providerCode, ProviderEventID: ref + ":TRADE_SUCCESS",
			ProviderPaymentID: ref, EventType: "payment.succeeded", OrderNo: o.no,
			Amount: 1000, Currency: "CNY", RawPayload: map[string]any{"fixture": "callback"},
			SignatureVerified: true,
		}); err != nil || !out.Processed {
			t.Fatalf("callback out=%+v err=%v", out, err)
		}
		stub.answer(o.no, paid(ref, 1000, "CNY"), nil)
		res, err := query(o)
		if err != nil || res.Reconciled || !res.AlreadyRecorded || res.OrderStatus != "fulfilled" {
			t.Fatalf("query after callback res=%+v err=%v", res, err)
		}
		if paymentsOf(o.id) != 1 || orderPaidTxns(o.id) != 1 {
			t.Fatal("query after callback recorded the money twice")
		}
	})

	t.Run("cancelled order found paid is quarantined, not fulfilled", func(t *testing.T) {
		o := newOrder(t, "cancelled", true)
		if _, err := service.CancelOrder(ctx, fx.tenant, o.buyer, o.id); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		stub.answer(o.no, paid("pq-trade-cancelled-"+fx.suffix, 1000, "CNY"), nil)
		res, err := query(o)
		if err != nil || res.ChannelStatus != ChannelPaid || !res.Reconciled ||
			res.QuarantineKind != "released_order" || res.OrderStatus != "cancelled" {
			t.Fatalf("cancelled res=%+v err=%v", res, err)
		}
		if count(`SELECT count(*) FROM late_payment_cases
			WHERE order_id=$1::uuid AND case_kind='released_order' AND status='suspense'`, o.id) != 1 ||
			orderPaidTxns(o.id) != 0 ||
			count(`SELECT count(*) FROM orders WHERE id=$1::uuid AND subscription_id IS NULL`, o.id) != 1 ||
			count(`SELECT count(*) FROM subscriptions WHERE user_id=$1::uuid`, o.buyer) != 0 {
			t.Fatal("cancelled order money must sit in suspense without fulfilment")
		}
	})

	t.Run("amount or currency mismatch is rejected and nothing is recorded", func(t *testing.T) {
		o := newOrder(t, "mismatch", true)
		for _, res := range []*payment.QueryResult{
			paid("pq-trade-short-"+fx.suffix, 999, "CNY"),
			paid("pq-trade-usd-"+fx.suffix, 1000, "USD"),
		} {
			stub.answer(o.no, res, nil)
			out, err := query(o)
			if out != nil {
				t.Fatalf("mismatch must not return a result: %+v", out)
			}
			wantHTTPError(t, err, httpx.CodeConflict, "支付币种或金额与订单不一致")
			if count(`SELECT count(*) FROM payment_events WHERE provider_payment_id=$1`, res.PaymentRef) != 0 {
				t.Fatal("rejected query left payment evidence behind")
			}
		}
		if orderStatus(o.id) != "pending_payment" || paymentsOf(o.id) != 0 {
			t.Fatal("mismatched query changed the order")
		}
	})

	t.Run("unpaid, not found and channel failure leave the order alone", func(t *testing.T) {
		o := newOrder(t, "unpaid", true)
		stub.answer(o.no, nil, nil)
		if res, err := query(o); err != nil || res.ChannelStatus != ChannelNotFound ||
			res.Reconciled || res.OrderStatus != "pending_payment" {
			t.Fatalf("not found res=%+v err=%v", res, err)
		}
		stub.answer(o.no, &payment.QueryResult{Found: true, PaymentRef: "pq-unpaid", Amount: 1000,
			Currency: "CNY", Status: payment.StatusPending}, nil)
		if res, err := query(o); err != nil || res.ChannelStatus != ChannelUnpaid ||
			res.Reconciled || res.OrderStatus != "pending_payment" {
			t.Fatalf("unpaid res=%+v err=%v", res, err)
		}
		stub.answer(o.no, nil, errors.New("epay: 查询请求失败: i/o timeout"))
		_, err := query(o)
		wantHTTPError(t, err, httpx.CodeUnavailable, "渠道查单失败，请稍后再试")
		if orderStatus(o.id) != "pending_payment" || paymentsOf(o.id) != 0 ||
			count(`SELECT count(*) FROM payment_intents WHERE order_id=$1::uuid
				AND status='requires_action'`, o.id) != 1 {
			t.Fatal("a query that found nothing changed the order or its intent")
		}
	})

	t.Run("business errors", func(t *testing.T) {
		noIntent := newOrder(t, "no-intent", false)
		_, err := query(noIntent)
		wantHTTPError(t, err, httpx.CodeConflict, "该订单从未发起过支付，无法向渠道查单")

		withIntent := newOrder(t, "foreign", true)
		_, err = payments.QueryOrderPayment(ctx, fx.tenant, withIntent.id, noIntent.buyer)
		wantHTTPError(t, err, httpx.CodeNotFound, "")
		_, err = payments.QueryOrderPayment(ctx, fx.tenant, "not-a-uuid", "")
		wantHTTPError(t, err, httpx.CodeNotFound, "")

		must(t, `UPDATE payment_providers SET enabled=false WHERE id=$1`, fx.provider)
		payments.Factory().Invalidate(fx.tenant, fx.providerCode)
		_, err = payments.QueryOrderPayment(ctx, fx.tenant, withIntent.id, "")
		wantHTTPError(t, err, httpx.CodeUnavailable, "该支付渠道已停用，无法向渠道查单")
		must(t, `UPDATE payment_providers SET enabled=true WHERE id=$1`, fx.provider)
		payments.Factory().Invalidate(fx.tenant, fx.providerCode)

		stub.answer(withIntent.no, nil, payment.ErrNotSupported)
		_, err = payments.QueryOrderPayment(ctx, fx.tenant, withIntent.id, "")
		wantHTTPError(t, err, httpx.CodeConflict, "该支付渠道不支持主动查单")
		if orderStatus(withIntent.id) != "pending_payment" {
			t.Fatal("failed queries changed the order")
		}
	})

	t.Run("patrol claims only due intents and concurrent patrols never query one order twice", func(t *testing.T) {
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := admin.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("fixture: %v\nSQL: %s", err, sql)
			}
		}
		// created_at 被支付意图守卫当作不可变列：夹具改它要临时关掉用户触发器，
		// 与 orderReleasePG18Backdate 同一手法
		backdate := func(orderID string) {
			t.Helper()
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatalf("begin backdate: %v", err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
				t.Fatalf("disable user triggers for fixture backdate: %v", err)
			}
			if tag, err := tx.Exec(ctx, `UPDATE payment_intents SET created_at=now()-interval '10 minutes'
				WHERE order_id=$1::uuid AND status='requires_action'`, orderID); err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("backdate intent tag=%v err=%v", tag, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit backdate: %v", err)
			}
		}
		var due []pendingOrder
		for _, label := range []string{"due-a", "due-b", "due-c", "due-d"} {
			o := newOrder(t, label, true)
			backdate(o.id)
			due = append(due, o)
		}
		stub.answer(due[0].no, paid("pq-trade-patrol-"+fx.suffix, 1000, "CNY"), nil)
		unsupported := newOrder(t, "due-unsupported", true)
		backdate(unsupported.id)
		stub.answer(unsupported.no, nil, payment.ErrNotSupported)

		// 不该被认领的：刚发起、次数已满、还没到下一次、订单已过期
		fresh := newOrder(t, "fresh", true)
		exhausted := newOrder(t, "exhausted", true)
		backdate(exhausted.id)
		// 排程列走守卫的白名单（00097），夹具照常写得进去
		exec(`UPDATE payment_intents SET query_attempts=6 WHERE order_id=$1::uuid`, exhausted.id)
		leased := newOrder(t, "leased", true)
		backdate(leased.id)
		exec(`UPDATE payment_intents SET query_attempts=1, next_query_at=now()+interval '3 minutes'
			WHERE order_id=$1::uuid`, leased.id)
		expired := newOrder(t, "expired", true)
		backdate(expired.id)
		orderReleasePG18Backdate(t, ctx, admin, fx.tenant, expired.id)

		patrol := DefaultPaymentQueryPatrol
		patrol.Spacing = 0
		stub.delay = 150 * time.Millisecond
		defer func() { stub.delay = 0 }()
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		var wg sync.WaitGroup
		stats := make([]PaymentQueryPatrolStats, 2)
		errs := make([]error, 2)
		for i := range stats {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				stats[i], errs[i] = payments.ReconcileDuePayments(ctx, fx.tenant, patrol, log)
			}(i)
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("patrol errors: %v / %v", errs[0], errs[1])
		}
		total := PaymentQueryPatrolStats{
			Queried:    stats[0].Queried + stats[1].Queried,
			Reconciled: stats[0].Reconciled + stats[1].Reconciled,
			Failed:     stats[0].Failed + stats[1].Failed,
		}
		if total.Queried != 5 || total.Reconciled != 1 || total.Failed != 1 {
			t.Fatalf("patrol stats=%+v / %+v", stats[0], stats[1])
		}
		for _, o := range append(due, unsupported) {
			if n := stub.callCount(o.no); n != 1 {
				t.Fatalf("order %s queried %d times by concurrent patrols, want 1", o.no, n)
			}
		}
		for _, o := range []pendingOrder{fresh, exhausted, leased, expired} {
			if n := stub.callCount(o.no); n != 0 {
				t.Fatalf("order %s was not due but queried %d times", o.no, n)
			}
		}
		if orderStatus(due[0].id) != "fulfilled" || paymentsOf(due[0].id) != 1 {
			t.Fatal("patrol did not reconcile the paid order")
		}
		for _, o := range due[1:] {
			if count(`SELECT count(*) FROM payment_intents WHERE order_id=$1::uuid
				AND status='requires_action' AND query_attempts=1 AND next_query_at > now()`, o.id) != 1 {
				t.Fatalf("order %s was not rescheduled after its query", o.no)
			}
		}
		if count(`SELECT count(*) FROM payment_intents WHERE order_id=$1::uuid
			AND query_attempts=$2`, unsupported.id, patrol.MaxAttempts) != 1 {
			t.Fatal("an order whose channel cannot be queried must not be patrolled again")
		}

		// 刚查过的单都排到了以后：紧接着再跑一轮什么也认领不到
		again, err := payments.ReconcileDuePayments(ctx, fx.tenant, patrol, log)
		if err != nil || again.Queried != 0 {
			t.Fatalf("immediate second round=%+v err=%v", again, err)
		}
	})
}
