// [INPUT]: 依赖 release.go / release_locks.go、checkout.go / settlement.go、unexpected_payment.go、reservation_expiry.go 的释放、结算与挂账路径，依赖 order_release_pg18_fixture_test.go 的夹具与故障注入、order_release_pg18_assert_test.go 的共用断言，依赖迁移 00036 / 00040
// [OUTPUT]: 对外提供 TestOrderReleasePG18（run-pg18-gates.sh 的 order_release 域）
// [POS]: billing 订单释放与迟到收款隔离的 PG18 集成门禁：释放故障整图回滚、取消与过期的保守与幂等、两个过期工人、迟到收款隔离、佣金与提现、RLS。整个文件只有一个 832 行的测试函数，子测试共享同一组连接与夹具，纯挪动拆不开，由行数守卫单独豁免
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// TestOrderReleasePG18 is deliberately an opt-in PostgreSQL 18 gate. The
// postgres DSN is used only to construct isolated, immutable fixture state;
// every business transition and every RLS assertion uses the aegis_app DSN.
func TestOrderReleasePG18(t *testing.T) {
	appDSN := os.Getenv("AEGIS_ORDER_RELEASE_PG18_DSN")
	adminDSN := os.Getenv("AEGIS_ORDER_RELEASE_PG18_ADMIN_DSN")
	if appDSN == "" || adminDSN == "" {
		t.Skip("AEGIS_ORDER_RELEASE_PG18_DSN and AEGIS_ORDER_RELEASE_PG18_ADMIN_DSN are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	pool, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open aegis_app PostgreSQL 18 pool: %v", err)
	}
	defer pool.Close()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open postgres fixture connection: %v", err)
	}
	defer admin.Close(ctx)

	var version int
	if err := admin.QueryRow(ctx,
		`SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatalf("read PostgreSQL version: %v", err)
	}
	if version/10000 != 18 {
		t.Fatalf("TestOrderReleasePG18 requires PostgreSQL 18, got server_version_num=%d", version)
	}
	orderReleasePG18AssertRuntimeTarget(t, ctx, pool, admin)

	fx := orderReleasePG18Seed(t, ctx, admin)
	service := NewService(pool, nil)
	orderReleasePG18FundBalance(t, ctx, pool, fx.tenant, fx.buyer, 5000)

	newTopup := func(t *testing.T, userID, label string, amount int64) string {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, userID,
			TopupIdempotencyScope, label)
		out, err := service.CreateTopup(ctx, fx.tenant, CreateTopupInput{
			UserID: userID, Amount: amount, Currency: "CNY", Claim: claim,
		})
		if err != nil {
			t.Fatalf("CreateTopup(%s): %v", label, err)
		}
		return out.OrderID
	}
	newOrder := func(t *testing.T, userID, label string) string {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, userID,
			CheckoutIdempotencyScope, label)
		out, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
			UserID: userID, PlanID: fx.plan, PriceID: fx.price, Claim: claim,
		})
		if err != nil {
			t.Fatalf("CreateOrder(%s): %v", label, err)
		}
		return out.OrderID
	}
	newResourceOrder := func(t *testing.T, userID, label string) string {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, userID,
			CheckoutIdempotencyScope, label)
		out, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
			UserID: userID, PlanID: fx.plan, PriceID: fx.price,
			CouponCode: fx.couponCode, UseBalance: 200, Claim: claim,
		})
		if err != nil {
			t.Fatalf("CreateOrder(%s): %v", label, err)
		}
		if out.Status != "pending_payment" || out.DiscountAmount != 100 ||
			out.TotalAmount != 900 || out.BalanceApplied != 200 || out.PayableAmount != 700 {
			t.Fatalf("CreateOrder(%s) resource output: %#v", label, out)
		}
		orderReleasePG18AssertHeldResources(t, ctx, pool, fx, userID, out.OrderID, 5000)
		return out.OrderID
	}
	webhook := func(orderID, eventID, paymentID string, amount int64) PaymentWebhookInput {
		return PaymentWebhookInput{
			ProviderCode: fx.providerCode, ProviderEventID: eventID,
			ProviderPaymentID: paymentID, EventType: "payment.succeeded",
			OrderID: orderID, Amount: amount, Currency: "CNY", FeeAmount: 7,
			RawPayload:        map[string]any{"fixture": "order-release-pg18", "event": eventID},
			SignatureVerified: true,
		}
	}

	t.Run("late release fault rolls back whole graph and same request retries", func(t *testing.T) {
		orderID := newResourceOrder(t, fx.buyer, "cancel-fault-retry")
		orderReleasePG18InsertActiveIntent(t, ctx, admin, fx, orderID)
		orderReleasePG18AssertSchema40Used(t, ctx, admin, false)
		cleanup := orderReleasePG18InstallReleaseFault(t, ctx, pool, admin, fx, orderID)

		faultCtx := httpx.WithRequestID(ctx, "orp18-cancel-fault-"+fx.suffix)
		before := orderReleasePG18BusinessSnapshot(t, faultCtx, admin, fx, orderID)
		if out, err := service.CancelOrder(faultCtx, fx.tenant, fx.buyer, orderID); err == nil {
			t.Fatalf("faulted CancelOrder unexpectedly succeeded: %#v", out)
		} else {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
				t.Fatalf("faulted CancelOrder error=%v pg=%#v want SQLSTATE P0001", err, pgErr)
			}
		}
		orderReleasePG18AssertFaultAttempt(t, faultCtx, admin, fx, 1)
		if after := orderReleasePG18BusinessSnapshot(t, faultCtx, admin, fx, orderID); after != before {
			t.Fatalf("faulted release did not roll back the complete business graph\nbefore=%s\nafter=%s",
				before, after)
		}
		orderReleasePG18AssertSchema40Used(t, ctx, admin, false)
		orderReleasePG18AssertHeldResources(t, ctx, pool, fx, fx.buyer, orderID, 5000)

		first, err := service.CancelOrder(faultCtx, fx.tenant, fx.buyer, orderID)
		if err != nil {
			t.Fatalf("same-request retry CancelOrder: %v", err)
		}
		if first.Status != "cancelled" || first.AlreadyTerminal {
			t.Fatalf("same-request retry disposition: %#v", first)
		}
		orderReleasePG18AssertFaultAttempt(t, faultCtx, admin, fx, 2)
		succeeded := orderReleasePG18BusinessSnapshot(t, faultCtx, admin, fx, orderID)
		second, err := service.CancelOrder(faultCtx, fx.tenant, fx.buyer, orderID)
		if err != nil {
			t.Fatalf("idempotent retry CancelOrder: %v", err)
		}
		if second.Status != "cancelled" || !second.AlreadyTerminal {
			t.Fatalf("idempotent retry disposition: %#v", second)
		}
		orderReleasePG18AssertFaultAttempt(t, faultCtx, admin, fx, 2)
		orderReleasePG18AssertReleased(t, ctx, pool, fx.tenant, orderID,
			"cancelled", "cancel", 2)
		orderReleasePG18AssertReleasedResources(t, ctx, pool, fx, fx.buyer, orderID,
			"cancelled", 5000)
		orderReleasePG18AssertFaultRetryEvidence(t, faultCtx, admin, fx, orderID)
		orderReleasePG18AssertSchema40Used(t, ctx, admin, true)
		if after := orderReleasePG18BusinessSnapshot(t, faultCtx, admin, fx, orderID); after != succeeded {
			t.Fatalf("idempotent same-request retry mutated successful state\nbefore=%s\nafter=%s",
				succeeded, after)
		}
		if err := cleanup(); err != nil {
			t.Fatalf("release fault cleanup: %v", err)
		}
		t.Log("marker=order_release_pg18_fault_rollback_same_request_retry_ok")
	})

	var cancelledOrder string
	t.Run("four resource cancel is conservative and idempotent", func(t *testing.T) {
		cancelledOrder = newResourceOrder(t, fx.buyer, "cancel-four-resource")
		first, err := service.CancelOrder(ctx, fx.tenant, fx.buyer, cancelledOrder)
		if err != nil {
			t.Fatalf("first CancelOrder: %v", err)
		}
		if first.Status != "cancelled" || first.AlreadyTerminal {
			t.Fatalf("first cancellation disposition: %#v", first)
		}
		before := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, cancelledOrder)
		second, err := service.CancelOrder(ctx, fx.tenant, fx.buyer, cancelledOrder)
		if err != nil {
			t.Fatalf("second CancelOrder: %v", err)
		}
		if second.Status != "cancelled" || !second.AlreadyTerminal {
			t.Fatalf("second cancellation disposition: %#v", second)
		}
		orderReleasePG18AssertReleased(t, ctx, pool, fx.tenant, cancelledOrder,
			"cancelled", "cancel", 2)
		orderReleasePG18AssertReleasedResources(t, ctx, pool, fx, fx.buyer, cancelledOrder,
			"cancelled", 5000)
		if after := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, cancelledOrder); after != before {
			t.Fatalf("idempotent cancellation mutated state\nbefore=%s\nafter=%s", before, after)
		}
		t.Log("marker=order_release_pg18_cancel_idempotent_ok")
		t.Log("marker=order_release_pg18_cancel_four_resource_conservation_ok")
	})

	t.Run("admin cancel uses CAS and preserves terminal idempotency", func(t *testing.T) {
		orderID := newResourceOrder(t, fx.buyer, "admin-cancel-cas")
		var version int64
		if err := admin.QueryRow(ctx, `SELECT state_version FROM orders WHERE tenant_id=$1 AND id=$2`,
			fx.tenant, orderID).Scan(&version); err != nil {
			t.Fatalf("read admin cancel baseline version: %v", err)
		}
		before := orderReleasePG18BusinessSnapshot(t, ctx, admin, fx, orderID)
		if out, err := service.AdminCancelOrder(ctx, fx.tenant, AdminCancelOrderInput{
			OrderID: orderID, ActorID: fx.buyer, ExpectedStateVersion: version + 1,
			Reason: "管理员确认取消未支付订单",
		}); err == nil || out != nil {
			t.Fatalf("stale admin cancel unexpectedly succeeded: out=%#v err=%v", out, err)
		}
		if after := orderReleasePG18BusinessSnapshot(t, ctx, admin, fx, orderID); after != before {
			t.Fatal("stale admin cancel changed business state")
		}

		first, err := service.AdminCancelOrder(ctx, fx.tenant, AdminCancelOrderInput{
			OrderID: orderID, ActorID: fx.buyer, ExpectedStateVersion: version,
			Reason: "管理员确认取消未支付订单",
		})
		if err != nil || first.Status != "cancelled" || first.AlreadyTerminal ||
			first.StateVersion != version+1 || first.CancelReason == nil ||
			*first.CancelReason != "管理员确认取消未支付订单" || first.CancelledAt == nil {
			t.Fatalf("admin cancel result=%#v err=%v", first, err)
		}
		fingerprint := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID)
		second, err := service.AdminCancelOrder(ctx, fx.tenant, AdminCancelOrderInput{
			OrderID: orderID, ActorID: fx.buyer, ExpectedStateVersion: version,
			Reason: "管理员确认取消未支付订单",
		})
		if err != nil || !second.AlreadyTerminal || second.StateVersion != version+1 {
			t.Fatalf("terminal admin cancel replay=%#v err=%v", second, err)
		}
		if after := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID); after != fingerprint {
			t.Fatal("terminal admin cancel replay changed business state")
		}
		t.Log("marker=order_release_pg18_admin_cancel_cas_idempotent_ok")
	})

	t.Run("four resource expiry is conservative and idempotent", func(t *testing.T) {
		orderID := newResourceOrder(t, fx.buyer, "expiry-four-resource")
		orderReleasePG18Backdate(t, ctx, admin, fx.tenant, orderID)
		first, err := service.ExpireDueReservations(ctx, fx.tenant, 10)
		if err != nil || first != 1 {
			t.Fatalf("first ExpireDueReservations count=%d err=%v", first, err)
		}
		before := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID)
		second, err := service.ExpireDueReservations(ctx, fx.tenant, 10)
		if err != nil || second != 0 {
			t.Fatalf("second ExpireDueReservations count=%d err=%v", second, err)
		}
		orderReleasePG18AssertReleased(t, ctx, pool, fx.tenant, orderID,
			"expired", "expire", 2)
		orderReleasePG18AssertReleasedResources(t, ctx, pool, fx, fx.buyer, orderID,
			"expired", 5000)
		if after := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID); after != before {
			t.Fatalf("idempotent expiry mutated state\nbefore=%s\nafter=%s", before, after)
		}
		t.Log("marker=order_release_pg18_expiry_idempotent_ok")
		t.Log("marker=order_release_pg18_expiry_four_resource_conservation_ok")
	})

	t.Run("two expiry workers drain more than one batch exactly once", func(t *testing.T) {
		const total = 8
		orders := make([]string, 0, total)
		for i := 0; i < total; i++ {
			orderID := newTopup(t, fx.buyer, fmt.Sprintf("expiry-multibatch-%d", i), 500)
			orderReleasePG18Backdate(t, ctx, admin, fx.tenant, orderID)
			orders = append(orders, orderID)
		}

		locked1 := make(chan []string, 1)
		locked2 := make(chan []string, 1)
		release1 := make(chan struct{})
		release2 := make(chan struct{})
		ctx1 := context.WithValue(ctx, reservationExpiryCandidatesLockedHookKey{},
			reservationExpiryCandidatesLockedHook(func(ids []string) error {
				locked1 <- ids
				<-release1
				return nil
			}))
		ctx2 := context.WithValue(ctx, reservationExpiryCandidatesLockedHookKey{},
			reservationExpiryCandidatesLockedHook(func(ids []string) error {
				locked2 <- ids
				<-release2
				return nil
			}))
		type expiryResult struct {
			count int
			err   error
		}
		result1 := make(chan expiryResult, 1)
		result2 := make(chan expiryResult, 1)
		go func() {
			count, err := service.ExpireDueReservations(ctx1, fx.tenant, 3)
			result1 <- expiryResult{count: count, err: err}
		}()
		first := orderReleasePG18WaitLockedBatch(t, locked1, "worker 1")
		go func() {
			count, err := service.ExpireDueReservations(ctx2, fx.tenant, 3)
			result2 <- expiryResult{count: count, err: err}
		}()
		second := orderReleasePG18WaitLockedBatch(t, locked2, "worker 2")
		if len(first) != 3 || len(second) != 3 {
			t.Fatalf("locked batch sizes=%d/%d want=3/3", len(first), len(second))
		}
		seen := make(map[string]struct{}, 6)
		for _, id := range first {
			seen[id] = struct{}{}
		}
		for _, id := range second {
			if _, exists := seen[id]; exists {
				t.Fatalf("workers locked overlapping order %s", id)
			}
			seen[id] = struct{}{}
		}
		close(release1)
		close(release2)
		firstResult := <-result1
		secondResult := <-result2
		if firstResult.err != nil || secondResult.err != nil ||
			firstResult.count != 3 || secondResult.count != 3 {
			t.Fatalf("first wave results=%+v/%+v", firstResult, secondResult)
		}

		start := make(chan struct{})
		result1 = make(chan expiryResult, 1)
		result2 = make(chan expiryResult, 1)
		go func() {
			<-start
			count, err := service.ExpireDueReservations(ctx, fx.tenant, 3)
			result1 <- expiryResult{count: count, err: err}
		}()
		go func() {
			<-start
			count, err := service.ExpireDueReservations(ctx, fx.tenant, 3)
			result2 <- expiryResult{count: count, err: err}
		}()
		close(start)
		firstResult = <-result1
		secondResult = <-result2
		if firstResult.err != nil || secondResult.err != nil ||
			firstResult.count+secondResult.count != 2 {
			t.Fatalf("second wave results=%+v/%+v", firstResult, secondResult)
		}
		if final, err := service.ExpireDueReservations(ctx, fx.tenant, 3); err != nil || final != 0 {
			t.Fatalf("final expiry drain count=%d err=%v", final, err)
		}
		for _, orderID := range orders {
			orderReleasePG18AssertReleased(t, ctx, pool, fx.tenant, orderID,
				"expired", "expire", 2)
		}
		t.Log("marker=order_release_pg18_expiry_two_workers_multibatch_exact_once_ok")
	})

	t.Run("cancel and expiry have one winner", func(t *testing.T) {
		orderID := newTopup(t, fx.buyer, "cancel-expiry-race", 500)
		orderReleasePG18Backdate(t, ctx, admin, fx.tenant, orderID)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var cancelOut *ReleaseOrderOutput
		var cancelErr, expiryErr error
		var expired int
		go func() {
			defer wg.Done()
			<-start
			cancelOut, cancelErr = service.CancelOrder(ctx, fx.tenant, fx.buyer, orderID)
		}()
		go func() {
			defer wg.Done()
			<-start
			expired, expiryErr = service.ExpireDueReservations(ctx, fx.tenant, 10)
		}()
		close(start)
		wg.Wait()
		if expiryErr != nil {
			t.Fatalf("expiry racer failed: %v", expiryErr)
		}
		cancelWon := cancelErr == nil && cancelOut != nil &&
			cancelOut.Status == "cancelled" && !cancelOut.AlreadyTerminal
		expiryWon := expired == 1 && cancelErr != nil
		if cancelWon == expiryWon || (cancelWon && expired != 0) {
			t.Fatalf("race has no single winner: cancel=%#v cancelErr=%v expired=%d",
				cancelOut, cancelErr, expired)
		}
		wantStatus, wantEvent := "expired", "expire"
		if cancelWon {
			wantStatus, wantEvent = "cancelled", "cancel"
		}
		orderReleasePG18AssertReleased(t, ctx, pool, fx.tenant, orderID,
			wantStatus, wantEvent, 2)
		t.Log("marker=order_release_pg18_cancel_expiry_concurrent_single_winner_ok")
	})

	t.Run("released order late payment is quarantined exactly once", func(t *testing.T) {
		before := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, cancelledOrder)
		in := webhook(cancelledOrder, "released-event-1-"+fx.suffix,
			"released-payment-"+fx.suffix, 500)
		out, err := service.HandlePaymentWebhook(ctx, fx.tenant, in)
		if err != nil || out == nil || !out.Processed || out.AlreadyHandled {
			t.Fatalf("released quarantine output=%#v err=%v", out, err)
		}
		once := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, cancelledOrder)
		replay, err := service.HandlePaymentWebhook(ctx, fx.tenant, in)
		if err != nil || replay == nil || !replay.AlreadyHandled || replay.Processed {
			t.Fatalf("released exact-event replay output=%#v err=%v", replay, err)
		}
		if got := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, cancelledOrder); got != once {
			t.Fatalf("released exact-event replay mutated business state before=%s after=%s", once, got)
		}
		duplicate := in
		duplicate.ProviderEventID = "released-event-2-" + fx.suffix
		duplicate.RawPayload = map[string]any{"fixture": "released-provider-payment-replay"}
		replayedPayment, err := service.HandlePaymentWebhook(ctx, fx.tenant, duplicate)
		if err != nil || replayedPayment == nil || !replayedPayment.AlreadyHandled || replayedPayment.Processed {
			t.Fatalf("released provider-payment replay output=%#v err=%v", replayedPayment, err)
		}
		orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, cancelledOrder,
			in.ProviderPaymentID, fx.providerCode, "released_order", 500, 7, 1, 1)
		after := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, cancelledOrder)
		if !strings.HasPrefix(after, "cancelled/released/") || before == after {
			t.Fatalf("late payment must add only quarantine evidence: before=%s after=%s", before, after)
		}
		t.Log("marker=order_release_pg18_released_late_payment_quarantine_exactly_once_ok")
	})

	t.Run("paid topup distinct capture is quarantined exactly once", func(t *testing.T) {
		orderID := newTopup(t, fx.buyer, "paid-distinct", 600)
		captured := webhook(orderID, "paid-original-event-"+fx.suffix,
			"paid-original-payment-"+fx.suffix, 600)
		out, err := service.HandlePaymentWebhook(ctx, fx.tenant, captured)
		if err != nil || out == nil || !out.Processed || out.AlreadyHandled {
			t.Fatalf("topup capture output=%#v err=%v", out, err)
		}
		// 充值单结算即履约（settlement.go 的 topup 分支），所以停在 fulfilled；
		// 之后到达的第二笔捕获对 paid 与 fulfilled 同样按 excess_capture 隔离。
		orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, orderID, "fulfilled")
		late := webhook(orderID, "paid-late-event-1-"+fx.suffix,
			"paid-late-payment-"+fx.suffix, 600)
		lateOut, err := service.HandlePaymentWebhook(ctx, fx.tenant, late)
		if err != nil || lateOut == nil || !lateOut.Processed || lateOut.AlreadyHandled {
			t.Fatalf("paid quarantine output=%#v err=%v", lateOut, err)
		}
		once := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID)
		if replay, err := service.HandlePaymentWebhook(ctx, fx.tenant, late); err != nil ||
			replay == nil || !replay.AlreadyHandled || replay.Processed {
			t.Fatalf("paid quarantine replay output=%#v err=%v", replay, err)
		}
		if got := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID); got != once {
			t.Fatalf("paid quarantine exact-event replay mutated state before=%s after=%s", once, got)
		}
		late2 := late
		late2.ProviderEventID = "paid-late-event-2-" + fx.suffix
		if replay, err := service.HandlePaymentWebhook(ctx, fx.tenant, late2); err != nil ||
			replay == nil || !replay.AlreadyHandled || replay.Processed {
			t.Fatalf("paid provider-payment replay output=%#v err=%v", replay, err)
		}
		orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, orderID,
			late.ProviderPaymentID, fx.providerCode, "excess_capture", 600, 7, 1, 1)
		orderReleasePG18AssertPaymentShape(t, ctx, pool, fx.tenant, orderID, 2, 1)
		orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, orderID, "fulfilled")
		t.Log("marker=order_release_pg18_paid_distinct_capture_quarantine_exactly_once_ok")
	})

	t.Run("fulfilled order distinct capture is quarantined exactly once", func(t *testing.T) {
		orderID := newOrder(t, fx.buyer, "fulfilled-distinct")
		captured := webhook(orderID, "fulfilled-original-event-"+fx.suffix,
			"fulfilled-original-payment-"+fx.suffix, 1000)
		out, err := service.HandlePaymentWebhook(ctx, fx.tenant, captured)
		if err != nil || out == nil || !out.Processed || out.AlreadyHandled || out.SubscriptionID == "" {
			t.Fatalf("order capture output=%#v err=%v", out, err)
		}
		orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, orderID, "fulfilled")
		late := webhook(orderID, "fulfilled-late-event-1-"+fx.suffix,
			"fulfilled-late-payment-"+fx.suffix, 1000)
		lateOut, err := service.HandlePaymentWebhook(ctx, fx.tenant, late)
		if err != nil || lateOut == nil || !lateOut.Processed || lateOut.AlreadyHandled {
			t.Fatalf("fulfilled quarantine output=%#v err=%v", lateOut, err)
		}
		once := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID)
		if replay, err := service.HandlePaymentWebhook(ctx, fx.tenant, late); err != nil ||
			replay == nil || !replay.AlreadyHandled || replay.Processed {
			t.Fatalf("fulfilled quarantine replay output=%#v err=%v", replay, err)
		}
		if got := orderReleasePG18Fingerprint(t, ctx, pool, fx.tenant, orderID); got != once {
			t.Fatalf("fulfilled quarantine exact-event replay mutated state before=%s after=%s", once, got)
		}
		late2 := late
		late2.ProviderEventID = "fulfilled-late-event-2-" + fx.suffix
		if replay, err := service.HandlePaymentWebhook(ctx, fx.tenant, late2); err != nil ||
			replay == nil || !replay.AlreadyHandled || replay.Processed {
			t.Fatalf("fulfilled provider-payment replay output=%#v err=%v", replay, err)
		}
		orderReleasePG18AssertQuarantine(t, ctx, pool, fx.tenant, orderID,
			late.ProviderPaymentID, fx.providerCode, "excess_capture", 1000, 7, 1, 1)
		orderReleasePG18AssertPaymentShape(t, ctx, pool, fx.tenant, orderID, 2, 1)
		orderReleasePG18AssertOrderStatus(t, ctx, pool, fx.tenant, orderID, "fulfilled")
		t.Log("marker=order_release_pg18_fulfilled_distinct_capture_quarantine_exactly_once_ok")
	})

	t.Run("commission writes fail closed", func(t *testing.T) {
		orderID := newOrder(t, fx.commissionBuyer, "commission-valid")
		capture := webhook(orderID, "commission-event-"+fx.suffix,
			"commission-payment-"+fx.suffix, 1000)
		if out, err := service.HandlePaymentWebhook(ctx, fx.tenant, capture); err != nil ||
			out == nil || !out.Processed || out.SubscriptionID == "" {
			t.Fatalf("commission-producing capture output=%#v err=%v", out, err)
		}

		var commissionID, accrualID string
		var amount int64
		orderReleasePG18InTx(t, ctx, pool, fx.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id::text,accrual_txn_id::text,commission_amount
				FROM commission_entries WHERE tenant_id=$1 AND order_id=$2::uuid`,
				fx.tenant, orderID).Scan(&commissionID, &accrualID, &amount)
		})
		if amount != 100 {
			t.Fatalf("commission amount=%d want=100", amount)
		}

		forgedOrder := newOrder(t, fx.buyer, "commission-forged-target")
		forgedErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO commission_entries
				(tenant_id,referrer_user_id,referee_user_id,order_id,currency,
				 base_amount,rate_bp,commission_amount,frozen_until,accrual_txn_id)
				VALUES ($1,$2::uuid,$3::uuid,$4::uuid,'CNY',1000,1000,100,now(),$5::uuid)`,
				fx.tenant, fx.referrer, fx.buyer, forgedOrder, accrualID)
			if err == nil {
				_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			}
			return err
		})
		if forgedErr == nil {
			t.Fatal("forged commission insert unexpectedly committed")
		}
		if state := orderReleasePG18SQLState(forgedErr); state != "23514" {
			t.Fatalf("forged commission SQLSTATE=%q want=23514: %v", state, forgedErr)
		}
		orderReleasePG18AssertCommissionCount(t, ctx, pool, fx.tenant, forgedOrder, 0)
		t.Log("marker=order_release_pg18_commission_forged_insert_rejected_ok")

		illegalErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE commission_entries SET status='available'
				WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, commissionID)
			if err == nil {
				_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			}
			return err
		})
		if illegalErr == nil {
			t.Fatal("illegal pending->available commission update unexpectedly committed")
		}
		if state := orderReleasePG18SQLState(illegalErr); state != "23514" {
			t.Fatalf("illegal commission update SQLSTATE=%q want=23514: %v", state, illegalErr)
		}
		orderReleasePG18AssertCommissionState(t, ctx, pool, fx.tenant, commissionID,
			"pending", "", "")
		t.Log("marker=order_release_pg18_commission_illegal_update_rejected_ok")

		orderReleasePG18InTx(t, ctx, pool, fx.tenant, func(tx pgx.Tx) error {
			pending, err := EnsureAccount(ctx, tx, fx.tenant,
				AccountUserCommissionPending, "CNY", &fx.referrer, "")
			if err != nil {
				return err
			}
			sink, err := EnsureAccount(ctx, tx, fx.tenant,
				AccountSuspense, "CNY", nil, "order-release-commission-drain")
			if err != nil {
				return err
			}
			_, err = Post(ctx, tx, fx.tenant, Posting{
				Kind: "commission_test_drain", Currency: "CNY",
				SourceType: "commission_entry", SourceID: &commissionID,
				Memo: "test removes pending funding", ActorKind: "system",
				Entries: []Entry{
					{AccountID: pending, Direction: Debit, Amount: amount},
					{AccountID: sink, Direction: Credit, Amount: amount},
				},
			})
			return err
		})
		settled, settleErr := service.SettleMatured(ctx, fx.tenant)
		if settleErr == nil || settled != 0 {
			t.Fatalf("unfunded settlement count=%d err=%v", settled, settleErr)
		}
		orderReleasePG18AssertCommissionState(t, ctx, pool, fx.tenant, commissionID,
			"pending", accrualID, "")
		t.Log("marker=order_release_pg18_commission_unfunded_maturity_rejected_ok")

		refundedOrder := newOrder(t, fx.commissionBuyer, "commission-refunded")
		refundedCapture := webhook(refundedOrder, "commission-refunded-event-"+fx.suffix,
			"commission-refunded-payment-"+fx.suffix, 1000)
		if out, err := service.HandlePaymentWebhook(ctx, fx.tenant, refundedCapture); err != nil ||
			out == nil || !out.Processed {
			t.Fatalf("refunded commission capture output=%#v err=%v", out, err)
		}
		var refundedCommissionID string
		orderReleasePG18InTx(t, ctx, pool, fx.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id::text FROM commission_entries
				WHERE tenant_id=$1 AND order_id=$2::uuid`, fx.tenant, refundedOrder).
				Scan(&refundedCommissionID)
		})
		// Fault injection establishes a post-refund snapshot without invoking the
		// not-yet-exposed refund API. The maturity worker must still fail closed.
		if _, err := admin.Exec(ctx, `SET session_replication_role='replica'`); err != nil {
			t.Fatalf("disable triggers for refund snapshot: %v", err)
		}
		if _, err := admin.Exec(ctx, `UPDATE orders SET status='refunded',refunded_amount=paid_amount,
			updated_at=now() WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, refundedOrder); err != nil {
			t.Fatalf("establish refund snapshot: %v", err)
		}
		if _, err := admin.Exec(ctx, `SET session_replication_role='origin'`); err != nil {
			t.Fatalf("restore trigger role: %v", err)
		}
		settled, settleErr = service.SettleMatured(ctx, fx.tenant)
		if settleErr == nil || settled != 0 {
			t.Fatalf("refunded settlement count=%d err=%v", settled, settleErr)
		}
		var refundedStatus string
		var reviewRequired bool
		var reviewReason *string
		orderReleasePG18InTx(t, ctx, pool, fx.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status,review_required,review_reason
				FROM commission_entries WHERE tenant_id=$1 AND id=$2::uuid`,
				fx.tenant, refundedCommissionID).Scan(&refundedStatus, &reviewRequired, &reviewReason)
		})
		if refundedStatus != "pending" || !reviewRequired || reviewReason == nil ||
			!strings.Contains(*reviewReason, "not commission-settlement eligible") {
			t.Fatalf("refunded commission state=%s review=%t reason=%v",
				refundedStatus, reviewRequired, reviewReason)
		}
		t.Log("marker=order_release_pg18_commission_refunded_maturity_rejected_ok")
	})

	t.Run("withdrawal ACL lifecycle and ledger evidence", func(t *testing.T) {
		orderID := newOrder(t, fx.commissionBuyer, "withdrawal-funding")
		capture := webhook(orderID, "withdrawal-event-"+fx.suffix,
			"withdrawal-payment-"+fx.suffix, 1000)
		if out, err := service.HandlePaymentWebhook(ctx, fx.tenant, capture); err != nil ||
			out == nil || !out.Processed {
			t.Fatalf("withdrawal funding capture output=%#v err=%v", out, err)
		}
		if settled, err := service.SettleMatured(ctx, fx.tenant); err != nil || settled != 1 {
			t.Fatalf("withdrawal funding settlement count=%d err=%v", settled, err)
		}
		var settledCommissionID string
		if err := admin.QueryRow(ctx, `SELECT id::text FROM commission_entries
			WHERE tenant_id=$1 AND order_id=$2::uuid AND status='available'`,
			fx.tenant, orderID).Scan(&settledCommissionID); err != nil {
			t.Fatalf("load settled commission evidence: %v", err)
		}
		extraSettlementErr := pool.InTx(ctx,
			platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer}, func(tx pgx.Tx) error {
				pending, err := EnsureAccount(ctx, tx, fx.tenant,
					AccountUserCommissionPending, "CNY", &fx.referrer, "")
				if err != nil {
					return err
				}
				available, err := EnsureAccount(ctx, tx, fx.tenant,
					AccountUserCommissionAvailable, "CNY", &fx.referrer, "")
				if err != nil {
					return err
				}
				if _, err = Post(ctx, tx, fx.tenant, Posting{
					Kind: "commission_settled", Currency: "CNY",
					SourceType: "commission_entry", SourceID: &settledCommissionID,
					Memo: "duplicate settlement rejection test", ActorKind: "system",
					Entries: []Entry{
						{AccountID: pending, Direction: Debit, Amount: 100},
						{AccountID: available, Direction: Credit, Amount: 100},
					},
				}); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
				return err
			})
		if state := orderReleasePG18SQLState(extraSettlementErr); state != "23514" {
			t.Fatalf("extra commission settlement SQLSTATE=%q want=23514 err=%v",
				state, extraSettlementErr)
		}
		t.Log("marker=order_release_pg18_extra_commission_settlement_rejected_ok")
		withdrawalID, err := service.RequestWithdrawal(ctx, fx.tenant, fx.referrer, 100, "")
		if err != nil || withdrawalID == "" {
			t.Fatalf("request withdrawal id=%q err=%v", withdrawalID, err)
		}

		identityErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer},
			func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE withdrawals SET amount=101
					WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, withdrawalID)
				return err
			})
		if !orderReleasePG18RejectedByPrivilegeOrGuard(identityErr) {
			t.Fatalf("提现金额被改动了，或拒绝方式不对 SQLSTATE=%q err=%v",
				orderReleasePG18SQLState(identityErr), identityErr)
		}
		deleteErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer},
			func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `DELETE FROM withdrawals
					WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, withdrawalID)
				return err
			})
		if !orderReleasePG18RejectedByPrivilegeOrGuard(deleteErr) {
			t.Fatalf("提现记录被删掉了，或拒绝方式不对 SQLSTATE=%q err=%v",
				orderReleasePG18SQLState(deleteErr), deleteErr)
		}
		illegalErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer},
			func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE withdrawals
					SET status='paid',payout_reference='forged',completed_at=now()
					WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, withdrawalID)
				return err
			})
		if state := orderReleasePG18SQLState(illegalErr); state != "23514" {
			t.Fatalf("withdrawal illegal transition SQLSTATE=%q want=23514 err=%v", state, illegalErr)
		}

		var payoutTxn string
		err = pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer},
			func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `UPDATE withdrawals SET status='approved',updated_at=now()
					WHERE tenant_id=$1 AND id=$2::uuid AND status='requested'`, fx.tenant, withdrawalID)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("approve withdrawal rows=%d", tag.RowsAffected())
				}
				payoutTxn, err = service.PostWithdrawalPayout(ctx, tx, fx.tenant,
					fx.referrer, "CNY", 100, withdrawalID)
				if err != nil {
					return err
				}
				tag, err = tx.Exec(ctx, `UPDATE withdrawals
					SET status='paid',payout_reference='pg18-payout',completed_at=now(),updated_at=now()
					WHERE tenant_id=$1 AND id=$2::uuid AND status='processing'
					  AND payout_txn_id=$3::uuid`, fx.tenant, withdrawalID, payoutTxn)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return fmt.Errorf("complete withdrawal rows=%d", tag.RowsAffected())
				}
				_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
				return err
			})
		if err != nil {
			t.Fatalf("complete withdrawal: %v", err)
		}
		var status, reference string
		var txnCount, entryCount int
		orderReleasePG18InTx(t, ctx, pool, fx.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT w.status,w.payout_reference,
				(SELECT count(*) FROM ledger_transactions lt WHERE lt.id=w.payout_txn_id
				 AND lt.kind='commission_paid' AND lt.source_type='withdrawal'
				 AND lt.source_id=w.id AND lt.actor_kind='admin'),
				(SELECT count(*) FROM ledger_entries le WHERE le.transaction_id=w.payout_txn_id)
				FROM withdrawals w WHERE w.tenant_id=$1 AND w.id=$2::uuid`,
				fx.tenant, withdrawalID).Scan(&status, &reference, &txnCount, &entryCount)
		})
		if status != "paid" || reference != "pg18-payout" || txnCount != 1 || entryCount != 2 {
			t.Fatalf("withdrawal evidence status=%s reference=%s txns=%d entries=%d",
				status, reference, txnCount, entryCount)
		}
		rewriteErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.referrer},
			func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE withdrawals SET payout_reference='rewritten'
					WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, withdrawalID)
				return err
			})
		if state := orderReleasePG18SQLState(rewriteErr); state != "23514" {
			t.Fatalf("withdrawal reference rewrite SQLSTATE=%q want=23514 err=%v", state, rewriteErr)
		}
		t.Log("marker=order_release_pg18_withdrawal_acl_lifecycle_ok")

		detachedErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
			available, err := EnsureAccount(ctx, tx, fx.tenant,
				AccountUserCommissionAvailable, "CNY", &fx.referrer, "")
			if err != nil {
				return err
			}
			channel, err := EnsureAccount(ctx, tx, fx.tenant,
				AccountChannelCash, "CNY", nil, "payout")
			if err != nil {
				return err
			}
			missing := uuid.NewString()
			if _, err = Post(ctx, tx, fx.tenant, Posting{
				Kind: "commission_paid", Currency: "CNY", SourceType: "withdrawal",
				SourceID: &missing, Memo: "detached payout test", ActorKind: "admin",
				Entries: []Entry{
					{AccountID: available, Direction: Debit, Amount: 1},
					{AccountID: channel, Direction: Credit, Amount: 1},
				},
			}); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			return err
		})
		if state := orderReleasePG18SQLState(detachedErr); state != "23514" {
			t.Fatalf("detached payout SQLSTATE=%q want=23514 err=%v", state, detachedErr)
		}
		t.Log("marker=order_release_pg18_withdrawal_detached_ledger_rejected_ok")
	})

	t.Run("forced RLS and cross-tenant service isolation", func(t *testing.T) {
		orderReleasePG18InTx(t, ctx, pool, fx.shadowTenant, func(tx pgx.Tx) error {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM orders
				WHERE tenant_id=$1 AND id=$2::uuid`, fx.tenant, cancelledOrder).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("tenant B observed tenant A order count=%d", count)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM late_payment_cases
				WHERE tenant_id=$1`, fx.tenant).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("tenant B observed tenant A quarantine rows=%d", count)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM commission_entries
				WHERE tenant_id=$1`, fx.tenant).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("tenant B observed tenant A commission rows=%d", count)
			}
			return nil
		})
		if out, err := service.CancelOrder(ctx, fx.shadowTenant, fx.shadowUser,
			cancelledOrder); err == nil || out != nil {
			t.Fatalf("cross-tenant cancellation output=%#v err=%v", out, err)
		}
	})

	// D-F-1 与缺陷 13，见 commission_ledger_pg18_test.go。
	t.Run("commission availability is one ledger rule under one lock", func(t *testing.T) {
		orderReleasePG18CommissionLedgerCases(t, ctx, pool, service, fx, newOrder, webhook)
	})
	t.Run("commission scope first_order accrues only the first order", func(t *testing.T) {
		orderReleasePG18CommissionScopeCases(t, ctx, admin, service, fx, newOrder, webhook)
	})
	t.Run("manual orders settle as a grant or a pending order", func(t *testing.T) {
		orderReleasePG18ManualOrderCases(t, ctx, admin, service, fx)
	})
	// 必须最后：它用超级用户塞了一条绕过触发器的美元挂账探针。
	t.Run("late payment pending amounts stay per currency", func(t *testing.T) {
		orderReleasePG18LatePaymentCurrencyCases(t, ctx, admin, service, fx)
	})
}
