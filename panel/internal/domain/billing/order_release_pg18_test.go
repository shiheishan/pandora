// [INPUT]: 依赖 release.go、checkout.go、unexpected_payment.go、reservation_expiry.go 的释放、结算与挂账路径，依赖 platform/db、middleware 幂等声明，依赖迁移 00036 / 00040
// [OUTPUT]: 对外提供 TestOrderReleasePG18（run-pg18-gates.sh 的 order_release 域），包内提供一次性租户夹具 orderReleasePG18Seed 与释放、挂账、佣金的共用断言（orderReleasePG18*），供 plan_change、traffic_pack 等 PG18 测试复用
// [POS]: billing 订单释放与迟到收款隔离的 PG18 集成门禁，也是本包 PG18 测试的夹具库
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"crypto/sha256"
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

	"github.com/aegispanel/aegis/internal/middleware"
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
		// 充值单结算即履约（checkout.go 的 topup 分支），所以停在 fulfilled；
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

func orderReleasePG18AssertRuntimeTarget(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, admin *pgx.Conn) {
	t.Helper()
	var adminDatabase, adminOID, systemIdentifier string
	if err := admin.QueryRow(ctx, `SELECT current_database(),
		(SELECT oid::text FROM pg_database WHERE datname=current_database()),
		(SELECT system_identifier::text FROM pg_control_system())`).Scan(
		&adminDatabase, &adminOID, &systemIdentifier); err != nil {
		t.Fatalf("read PostgreSQL target identity: %v", err)
	}
	tables := []string{
		"users", "products", "plans", "plan_versions", "prices",
		"entitlements", "quota_definitions", "orders", "order_items",
		"order_reservations", "order_reservation_events", "order_stock_reservations",
		"order_purchase_limit_reservations", "plan_purchase_counters", "coupons",
		"coupon_redemptions", "balance_holds", "ledger_accounts",
		"ledger_transactions", "ledger_entries", "audit_events", "idempotency_keys",
		"payment_providers", "payment_intents", "payment_events", "payments",
		"late_payment_cases", "subscriptions", "quota_balances", "referrals",
		"system_settings", "commission_entries", "withdrawals",
	}
	orderReleasePG18InTx(t, ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		var user, database, oid string
		var superuser, bypassRLS bool
		if err := tx.QueryRow(ctx, `SELECT current_user,current_database(),
			(SELECT oid::text FROM pg_database WHERE datname=current_database()),
			rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(
			&user, &database, &oid, &superuser, &bypassRLS); err != nil {
			return err
		}
		if user != "aegis_app" || superuser || bypassRLS ||
			database != adminDatabase || oid != adminOID {
			t.Fatalf("runtime target user=%s super=%t bypass_rls=%t database=%s/%s oid=%s/%s system=%s",
				user, superuser, bypassRLS, database, adminDatabase, oid, adminOID, systemIdentifier)
		}
		var protected int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname='public' AND c.relname=ANY($1::text[])
			  AND c.relrowsecurity AND c.relforcerowsecurity
			  AND pg_get_userbyid(c.relowner)<>current_user`, tables).Scan(&protected); err != nil {
			return err
		}
		if protected != len(tables) {
			t.Fatalf("runtime protected table count=%d want=%d", protected, len(tables))
		}
		return nil
	})
	t.Logf("marker=order_release_pg18_target_identity_ok database=%s oid=%s system_identifier=%s role=aegis_app rls=forced",
		adminDatabase, adminOID, systemIdentifier)
}

func orderReleasePG18WaitLockedBatch(t *testing.T, batches <-chan []string,
	worker string) []string {
	t.Helper()
	select {
	case ids := <-batches:
		return ids
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not reach the candidate-lock synchronization point", worker)
		return nil
	}
}

type orderReleasePG18Fixture struct {
	tenant, shadowTenant, buyer, commissionBuyer, referrer, shadowUser string
	product, plan, version, price, coupon, couponCode                  string
	provider, providerCode, suffix                                     string
}

func orderReleasePG18Seed(t *testing.T, ctx context.Context, admin *pgx.Conn) orderReleasePG18Fixture {
	t.Helper()
	compact := strings.ReplaceAll(uuid.NewString(), "-", "")
	fx := orderReleasePG18Fixture{
		tenant: uuid.NewString(), shadowTenant: uuid.NewString(), buyer: uuid.NewString(),
		commissionBuyer: uuid.NewString(), referrer: uuid.NewString(), shadowUser: uuid.NewString(),
		product: uuid.NewString(), plan: uuid.NewString(), version: uuid.NewString(),
		price: uuid.NewString(), coupon: uuid.NewString(), couponCode: "ORP100-" + compact[:8],
		provider: uuid.NewString(), providerCode: "orp18-" + compact[:12],
		suffix: compact[:16],
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture: %v", err)
	}
	defer tx.Rollback(ctx)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed fixture: %v\nSQL: %s", err, query)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency)
		VALUES($1,$2,$3,'CNY'),($4,$5,$6,'CNY')`,
		fx.tenant, "orp18-"+compact[:12], "Order Release PG18",
		fx.shadowTenant, "orp18-shadow-"+compact[:8], "Order Release PG18 Shadow")
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		($1,$2,$3,'Release Buyer','active'),
		($4,$2,$5,'Commission Buyer','active'),
		($6,$2,$7,'Commission Referrer','active'),
		($8,$9,$10,'Shadow User','active')`,
		fx.buyer, fx.tenant, "buyer-"+compact[:12]+"@example.test",
		fx.commissionBuyer, "commission-buyer-"+compact[:12]+"@example.test",
		fx.referrer, "referrer-"+compact[:12]+"@example.test",
		fx.shadowUser, fx.shadowTenant, "shadow-"+compact[:12]+"@example.test")
	must(`INSERT INTO products(id,tenant_id,code,name,status)
		VALUES($1,$2,$3,'Order Release Product','active')`, fx.product, fx.tenant, "orp-"+compact[:10])
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status,
		purchase_limit_per_user,stock_total)
		VALUES($1,$2,$3,$4,'Order Release Plan','draft',10,50)`,
		fx.plan, fx.tenant, fx.product, "orp-plan-"+compact[:8])
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`,
		fx.version, fx.tenant, fx.plan)
	must(`UPDATE plan_versions SET frozen_at=now(),status='published'
		WHERE tenant_id=$1 AND id=$2`, fx.tenant, fx.version)
	must(`UPDATE plans SET current_version_id=$3,status='active' WHERE tenant_id=$1 AND id=$2`,
		fx.tenant, fx.plan, fx.version)
	must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status)
		VALUES($1,$2,$3,'CNY',1000,'month',1,'active')`, fx.price, fx.tenant, fx.product)
	must(`INSERT INTO coupons(id,tenant_id,code,name,discount_type,discount_value,
		currency,applicable_plan_ids,max_redemptions,max_redemptions_per_user,status)
		VALUES($1,$2,$3,'Order Release Coupon','fixed',100,'CNY',ARRAY[$4::uuid],10,10,'active')`,
		fx.coupon, fx.tenant, fx.couponCode, fx.plan)
	must(`INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
		VALUES($1,$2,$3,'demo_hmac','Order Release Provider',true),
		      ($4,$5,$6,'demo_hmac','Shadow Provider',true)`,
		fx.provider, fx.tenant, fx.providerCode, uuid.NewString(), fx.shadowTenant, "shadow-"+fx.providerCode)
	must(`INSERT INTO referrals(tenant_id,referee_user_id,referrer_user_id,channel,campaign)
		VALUES($1,$2,$3,'pg18','order-release')`, fx.tenant, fx.commissionBuyer, fx.referrer)
	must(`INSERT INTO system_settings(tenant_id,key,value,value_schema) VALUES
		($1,'commission.rate_percent','10'::jsonb,'{"type":"integer","minimum":0,"maximum":50}'::jsonb),
		($1,'commission.freeze_days','0'::jsonb,'{"type":"integer","minimum":0,"maximum":90}'::jsonb),
		($1,'commission.min_withdraw','100'::jsonb,'{"type":"integer","minimum":0}'::jsonb)`, fx.tenant)
	must(`SET CONSTRAINTS ALL IMMEDIATE`)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	return fx
}

func orderReleasePG18Claim(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID, actorID, logicalScope, label string) middleware.IdempotencyClaim {
	t.Helper()
	id := uuid.NewString()
	key := "orp18-" + label + "-" + id[:8]
	hash := sha256.Sum256([]byte("order-release-pg18:" + label + ":" + id))
	actorHash := sha256.Sum256([]byte(actorID))
	storageScope := fmt.Sprintf("%s:actor:%x", logicalScope, actorHash[:12])
	lease := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO idempotency_keys
		(id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
		VALUES($1,$2,$3,$4,$5,'in_flight',$6,$7)`,
		id, tenantID, storageScope, key, hash[:], actorID, lease); err != nil {
		t.Fatalf("insert idempotency claim %s: %v", label, err)
	}
	return middleware.IdempotencyClaim{
		ID: id, TenantID: tenantID, ActorID: actorID, Scope: logicalScope,
		StorageScope: storageScope, Key: key, RequestHash: hash,
		Generation: 1, LockedUntil: lease,
	}
}

func orderReleasePG18Backdate(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID, orderID string) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin backdate: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable user triggers for fixture backdate: %v", err)
	}
	if tag, err := tx.Exec(ctx, `UPDATE orders SET expires_at=now()-interval '1 minute'
		WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, orderID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("backdate order tag=%v err=%v", tag, err)
	}
	if tag, err := tx.Exec(ctx, `UPDATE order_reservations SET expires_at=now()-interval '1 minute'
		WHERE tenant_id=$1 AND order_id=$2::uuid`, tenantID, orderID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("backdate reservation tag=%v err=%v", tag, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit backdate: %v", err)
	}
}

func orderReleasePG18InsertActiveIntent(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, orderID string) {
	t.Helper()
	intentID := uuid.NewString()
	providerRef := "orp18-fault-" + fx.suffix
	if tag, err := admin.Exec(ctx, `INSERT INTO payment_intents
		(id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
		VALUES($1,$2,$3::uuid,$4,'CNY',700,'requires_action',$5)`,
		intentID, fx.tenant, orderID, fx.provider, providerRef); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("insert active fault-test payment intent tag=%v err=%v", tag, err)
	}
}

func orderReleasePG18AssertSchema40Used(t *testing.T, ctx context.Context,
	admin *pgx.Conn, want bool) {
	t.Helper()
	var got bool
	if err := admin.QueryRow(ctx, `SELECT used FROM app.order_release_00040_meta WHERE singleton`).
		Scan(&got); err != nil {
		t.Fatalf("read schema40 order-release watermark: %v", err)
	}
	if got != want {
		t.Fatalf("schema40 order-release watermark=%t want=%t", got, want)
	}
}

func orderReleasePG18AssertFaultAttempt(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, want int64) {
	t.Helper()
	sequence := pgx.Identifier{"orp_fault_" + fx.suffix, "attempt_seq"}.Sanitize()
	var got int64
	var called bool
	if err := admin.QueryRow(ctx, "SELECT last_value,is_called FROM "+sequence).
		Scan(&got, &called); err != nil {
		t.Fatalf("read release fault attempt: %v", err)
	}
	if got != want || !called {
		t.Fatalf("release fault attempt=%d/%t want=%d/true", got, called, want)
	}
}

func orderReleasePG18InstallReleaseFault(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, admin *pgx.Conn, fx orderReleasePG18Fixture,
	orderID string) func() error {
	t.Helper()

	schemaName := "orp_fault_" + fx.suffix
	tableName := "control"
	sequenceName := "attempt_seq"
	functionName := "fail_first_cancel"
	triggerName := "trg_orp_fault_" + fx.suffix
	schemaID := pgx.Identifier{schemaName}.Sanitize()
	tableID := pgx.Identifier{tableName}.Sanitize()
	sequenceID := pgx.Identifier{sequenceName}.Sanitize()
	functionID := pgx.Identifier{functionName}.Sanitize()
	triggerID := pgx.Identifier{triggerName}.Sanitize()
	qualifiedTable := schemaID + "." + tableID
	qualifiedSequence := schemaID + "." + sequenceID
	qualifiedFunction := schemaID + "." + functionID

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin release fault installation: %v", err)
	}
	defer tx.Rollback(ctx)
	must := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(ctx, query, args...); execErr != nil {
			t.Fatalf("install release fault: %v\nSQL: %s", execErr, query)
		}
	}
	must("CREATE SCHEMA " + schemaID)
	must("REVOKE ALL ON SCHEMA " + schemaID + " FROM PUBLIC, aegis_app")
	must("CREATE TABLE " + qualifiedTable + ` (
		singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
		tenant_id uuid NOT NULL, order_id uuid NOT NULL, action text NOT NULL)`)
	must("INSERT INTO "+qualifiedTable+"(tenant_id,order_id,action) VALUES($1,$2::uuid,'order.cancelled')",
		fx.tenant, orderID)
	must("CREATE SEQUENCE " + qualifiedSequence + " START WITH 1 INCREMENT BY 1 NO CYCLE")
	functionSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger
		LANGUAGE plpgsql SECURITY DEFINER
		SET search_path = pg_catalog
		AS $fault$
		DECLARE target_tenant uuid; target_order uuid; target_action text;
		BEGIN
		  IF TG_RELID <> 'public.audit_events'::pg_catalog.regclass OR TG_OP <> 'INSERT' THEN
		    RAISE EXCEPTION 'order release fault trigger target mismatch' USING ERRCODE='55000';
		  END IF;
		  SELECT tenant_id,order_id,action INTO STRICT target_tenant,target_order,target_action
		    FROM %s WHERE singleton;
		  IF NEW.tenant_id=target_tenant AND NEW.resource_id=target_order
		     AND NEW.resource_type='order' AND NEW.action=target_action
		     AND pg_catalog.nextval('%s'::pg_catalog.regclass)=1 THEN
		    RAISE EXCEPTION 'order release rollback test fault' USING ERRCODE='P0001';
		  END IF;
		  RETURN NULL;
		END
		$fault$`, qualifiedFunction, qualifiedTable, qualifiedSequence)
	must(functionSQL)
	must("REVOKE ALL ON TABLE " + qualifiedTable + " FROM PUBLIC, aegis_app")
	must("REVOKE ALL ON SEQUENCE " + qualifiedSequence + " FROM PUBLIC, aegis_app")
	must("REVOKE ALL ON FUNCTION " + qualifiedFunction + "() FROM PUBLIC, aegis_app")
	must(fmt.Sprintf(`CREATE CONSTRAINT TRIGGER %s
		AFTER INSERT ON public.audit_events
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
		EXECUTE FUNCTION %s()`, triggerID, qualifiedFunction))
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit release fault installation: %v", err)
	}

	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupTx, beginErr := admin.Begin(ctx)
			if beginErr != nil {
				cleanupErr = fmt.Errorf("begin: %w", beginErr)
				return
			}
			defer cleanupTx.Rollback(ctx)
			statements := []string{
				"DROP TRIGGER " + triggerID + " ON public.audit_events",
				"DROP FUNCTION " + qualifiedFunction + "()",
				"DROP SEQUENCE " + qualifiedSequence,
				"DROP TABLE " + qualifiedTable,
				"DROP SCHEMA " + schemaID,
			}
			for _, statement := range statements {
				if _, execErr := cleanupTx.Exec(ctx, statement); execErr != nil {
					cleanupErr = fmt.Errorf("%s: %w", statement, execErr)
					return
				}
			}
			var remaining int
			if scanErr := cleanupTx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM pg_trigger WHERE tgname=$1 AND NOT tgisinternal)+
				(SELECT count(*) FROM pg_namespace WHERE nspname=$2)`,
				triggerName, schemaName).Scan(&remaining); scanErr != nil {
				cleanupErr = fmt.Errorf("verify absence: %w", scanErr)
				return
			}
			if remaining != 0 {
				cleanupErr = fmt.Errorf("fault objects remain: %d", remaining)
				return
			}
			if commitErr := cleanupTx.Commit(ctx); commitErr != nil {
				cleanupErr = fmt.Errorf("commit: %w", commitErr)
			}
		})
		return cleanupErr
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("release fault cleanup: %v", err)
		}
	})

	var schemaUsage, schemaCreate bool
	var sequenceUsage, sequenceSelect, sequenceUpdate bool
	var tableSelect, tableInsert, tableUpdate, tableDelete, tableTruncate bool
	var auditTrigger, functionExecute, securityDefiner, functionLeakproof bool
	var functionOwner, functionVolatility string
	var functionConfig []string
	functionSignature := schemaName + "." + functionName + "()"
	if err := admin.QueryRow(ctx, `SELECT
		has_schema_privilege('aegis_app',$1,'USAGE'),
		has_schema_privilege('aegis_app',$1,'CREATE'),
		has_sequence_privilege('aegis_app',$2,'USAGE'),
		has_sequence_privilege('aegis_app',$2,'SELECT'),
		has_sequence_privilege('aegis_app',$2,'UPDATE'),
		has_table_privilege('aegis_app',$3,'SELECT'),
		has_table_privilege('aegis_app',$3,'INSERT'),
		has_table_privilege('aegis_app',$3,'UPDATE'),
		has_table_privilege('aegis_app',$3,'DELETE'),
		has_table_privilege('aegis_app',$3,'TRUNCATE'),
		has_table_privilege('aegis_app','public.audit_events','TRIGGER'),
		has_function_privilege('aegis_app',$4,'EXECUTE'),
		p.prosecdef,p.proleakproof,p.provolatile::text,r.rolname,
		coalesce(p.proconfig,ARRAY[]::text[])
		FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		JOIN pg_roles r ON r.oid=p.proowner
		WHERE n.nspname=$1 AND p.proname=$5 AND p.pronargs=0`,
		schemaName, schemaName+"."+sequenceName, schemaName+"."+tableName,
		functionSignature, functionName).Scan(
		&schemaUsage, &schemaCreate, &sequenceUsage, &sequenceSelect, &sequenceUpdate,
		&tableSelect, &tableInsert, &tableUpdate, &tableDelete, &tableTruncate,
		&auditTrigger, &functionExecute, &securityDefiner, &functionLeakproof,
		&functionVolatility, &functionOwner, &functionConfig); err != nil {
		t.Fatalf("inspect release fault privileges: %v", err)
	}
	if schemaUsage || schemaCreate || sequenceUsage || sequenceSelect || sequenceUpdate ||
		tableSelect || tableInsert || tableUpdate || tableDelete || tableTruncate ||
		auditTrigger || functionExecute || !securityDefiner || functionLeakproof ||
		functionVolatility != "v" || functionOwner == "aegis_app" ||
		len(functionConfig) != 1 || functionConfig[0] != "search_path=pg_catalog" {
		t.Fatalf("unsafe release fault privileges schema=%t/%t sequence=%t/%t/%t table=%t/%t/%t/%t/%t trigger=%t function=%t definer=%t leakproof=%t volatility=%s owner=%s config=%v",
			schemaUsage, schemaCreate, sequenceUsage, sequenceSelect, sequenceUpdate,
			tableSelect, tableInsert, tableUpdate, tableDelete, tableTruncate,
			auditTrigger, functionExecute, securityDefiner, functionLeakproof,
			functionVolatility, functionOwner, functionConfig)
	}
	var triggerRows int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t
		JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace n ON n.oid=p.pronamespace
		WHERE t.tgname=$1 AND t.tgrelid='public.audit_events'::regclass
		AND NOT t.tgisinternal AND t.tgdeferrable AND t.tginitdeferred AND t.tgenabled='O'
		AND n.nspname=$2 AND p.proname=$3
		AND pg_get_triggerdef(t.oid) LIKE '%AFTER INSERT%DEFERRABLE INITIALLY DEFERRED%'`,
		triggerName, schemaName, functionName).Scan(&triggerRows); err != nil {
		t.Fatalf("inspect release fault trigger: %v", err)
	}
	if triggerRows != 1 {
		t.Fatalf("release fault trigger rows=%d want=1", triggerRows)
	}
	var sequenceOID uint32
	var sequenceLast int64
	var sequenceCalled bool
	if err := admin.QueryRow(ctx, "SELECT $1::regclass::oid,last_value,is_called FROM "+qualifiedSequence,
		schemaName+"."+sequenceName).Scan(&sequenceOID, &sequenceLast, &sequenceCalled); err != nil {
		t.Fatalf("inspect release fault sequence: %v", err)
	}
	if sequenceLast != 1 || sequenceCalled {
		t.Fatalf("release fault sequence initial=%d/%t want=1/false", sequenceLast, sequenceCalled)
	}
	permissionErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
		var value int64
		return tx.QueryRow(ctx, `SELECT pg_catalog.nextval($1::oid::pg_catalog.regclass)`, sequenceOID).
			Scan(&value)
	})
	var permissionPGErr *pgconn.PgError
	if !errors.As(permissionErr, &permissionPGErr) || permissionPGErr.Code != "42501" {
		t.Fatalf("aegis_app direct fault sequence access err=%v pg=%#v want SQLSTATE 42501",
			permissionErr, permissionPGErr)
	}
	if err := admin.QueryRow(ctx, "SELECT last_value,is_called FROM "+qualifiedSequence).
		Scan(&sequenceLast, &sequenceCalled); err != nil {
		t.Fatalf("reinspect release fault sequence: %v", err)
	}
	if sequenceLast != 1 || sequenceCalled {
		t.Fatalf("denied app probe consumed release fault sequence=%d/%t", sequenceLast, sequenceCalled)
	}
	return cleanup
}

func orderReleasePG18BusinessSnapshot(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, orderID string) string {
	t.Helper()
	parts := make([]string, 0, 19)
	add := func(name, query string, args ...any) {
		t.Helper()
		var value string
		if err := admin.QueryRow(ctx, query, args...).Scan(&value); err != nil {
			t.Fatalf("snapshot %s: %v", name, err)
		}
		parts = append(parts, name+"="+value)
	}
	rowSet := func(table, predicate, order string) string {
		return fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY %s),'[]'::jsonb)::text
			FROM %s x WHERE %s`, order, table, predicate)
	}
	add("orders", rowSet("orders", "x.tenant_id=$1 AND x.id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("items", rowSet("order_items", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("intents", rowSet("payment_intents", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("reservations", rowSet("order_reservations", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("stock", rowSet("order_stock_reservations", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("purchase", rowSet("order_purchase_limit_reservations", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("redemptions", rowSet("coupon_redemptions", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("holds", rowSet("balance_holds", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("plans", rowSet("plans", "x.tenant_id=$1 AND x.id=$2::uuid", "x.id"), fx.tenant, fx.plan)
	add("purchase_counters", rowSet("plan_purchase_counters", "x.tenant_id=$1 AND x.plan_id=$2::uuid AND x.user_id=$3::uuid", "x.plan_id,x.user_id"), fx.tenant, fx.plan, fx.buyer)
	add("coupons", rowSet("coupons", "x.tenant_id=$1 AND x.id=$2::uuid", "x.id"), fx.tenant, fx.coupon)
	add("accounts", `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb)::text
		FROM ledger_accounts x WHERE x.tenant_id=$1 AND x.id IN (
			SELECT available_account_id FROM balance_holds WHERE tenant_id=$1 AND order_id=$2::uuid
			UNION SELECT hold_account_id FROM balance_holds WHERE tenant_id=$1 AND order_id=$2::uuid)`,
		fx.tenant, orderID)
	add("ledger_transactions", rowSet("ledger_transactions", "x.tenant_id=$1 AND x.source_type='order' AND x.source_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("ledger_entries", `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb)::text
		FROM ledger_entries x WHERE x.tenant_id=$1 AND x.transaction_id IN (
			SELECT id FROM ledger_transactions WHERE tenant_id=$1 AND source_type='order' AND source_id=$2::uuid)`,
		fx.tenant, orderID)
	add("events", rowSet("order_reservation_events", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("audit", rowSet("audit_events", "x.tenant_id=$1 AND x.resource_type='order' AND x.resource_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("idempotency", `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb)::text
		FROM idempotency_keys x WHERE x.tenant_id=$1 AND x.id=(
			SELECT idempotency_key_id FROM orders WHERE tenant_id=$1 AND id=$2::uuid)`, fx.tenant, orderID)
	add("payments", rowSet("payments", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("refunds", rowSet("refunds", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("late_cases", rowSet("late_payment_cases", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("schema40", `SELECT to_jsonb(x)::text FROM app.order_release_00040_meta x WHERE singleton`)
	return strings.Join(parts, "\n")
}

func orderReleasePG18AssertFaultRetryEvidence(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, orderID string) {
	t.Helper()
	requestID := httpx.RequestIDFrom(ctx)
	var intents, cancelledIntents, events, reserveEvents, exactCancelEvents int
	var audits, createdAudits, exactCancelAudits, payments, cases, refunds int
	err := admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM payment_intents WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM payment_intents WHERE tenant_id=$1 AND order_id=$2::uuid AND status='cancelled'),
		(SELECT count(*) FROM order_reservation_events WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM order_reservation_events WHERE tenant_id=$1 AND order_id=$2::uuid AND event_kind='reserve'),
		(SELECT count(*) FROM order_reservation_events e WHERE e.tenant_id=$1 AND e.order_id=$2::uuid
		 AND e.event_kind='cancel' AND e.from_state='held' AND e.to_state='released'
		 AND e.business_request_id=(SELECT business_request_id FROM orders WHERE tenant_id=$1 AND id=$2::uuid)
		 AND e.actor_kind='user' AND e.actor_id=$3::uuid AND e.reason='user_cancelled'),
		(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_type='order' AND resource_id=$2::uuid),
		(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_type='order' AND resource_id=$2::uuid AND action='order.created'),
		(SELECT count(*) FROM audit_events a WHERE a.tenant_id=$1 AND a.resource_type='order' AND a.resource_id=$2::uuid
		 AND a.action='order.cancelled' AND a.actor_kind='user' AND a.actor_id=$3::uuid
		 AND a.request_id=$4 AND a.api_domain='public' AND a.outcome='success'),
		(SELECT count(*) FROM payments WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM late_payment_cases WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM refunds WHERE tenant_id=$1 AND order_id=$2::uuid)`,
		fx.tenant, orderID, fx.buyer, requestID).Scan(
		&intents, &cancelledIntents, &events, &reserveEvents, &exactCancelEvents,
		&audits, &createdAudits, &exactCancelAudits, &payments, &cases, &refunds)
	if err != nil {
		t.Fatalf("read fault retry evidence: %v", err)
	}
	if intents != 1 || cancelledIntents != 1 || events != 2 || reserveEvents != 1 ||
		exactCancelEvents != 1 || audits != 2 || createdAudits != 1 || exactCancelAudits != 1 ||
		payments != 0 || cases != 0 || refunds != 0 {
		t.Fatalf("fault retry evidence intents=%d/%d events=%d/%d/%d audits=%d/%d/%d payments=%d cases=%d refunds=%d request=%s",
			intents, cancelledIntents, events, reserveEvents, exactCancelEvents,
			audits, createdAudits, exactCancelAudits, payments, cases, refunds, requestID)
	}
}

func orderReleasePG18FundBalance(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, userID string, amount int64) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		available, err := EnsureAccount(ctx, tx, tenantID,
			AccountUserBalance, "CNY", &userID, "")
		if err != nil {
			return err
		}
		source, err := EnsureAccount(ctx, tx, tenantID,
			AccountSuspense, "CNY", nil, "order-release-pg18-funding")
		if err != nil {
			return err
		}
		_, err = Post(ctx, tx, tenantID, Posting{
			Kind: "balance_topup", Currency: "CNY", SourceType: "test_fixture",
			Memo: "order release PG18 user balance", ActorKind: "system",
			Entries: []Entry{
				{AccountID: source, Direction: Debit, Amount: amount},
				{AccountID: available, Direction: Credit, Amount: amount},
			},
		})
		return err
	})
}

func orderReleasePG18AssertHeldResources(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, fx orderReleasePG18Fixture, userID, orderID string,
	fundedBalance int64) {
	t.Helper()
	// 必须带上下单用户：这条查询要 JOIN idempotency_keys，而那张表的 RLS
	// 策略按 actor 过滤，只给租户的话它一行都看不见。
	orderReleasePG18InTxAs(t, ctx, pool, fx.tenant, userID, func(tx pgx.Tx) error {
		var orderStatus, reservationState, redemptionState, holdState string
		var stockReserved, stockSold, purchaseReserved, purchased int
		var couponReserved, couponRedeemed int
		var availableBalance, holdBalance int64
		var itemRows, stockRows, purchaseRows, redemptionRows, holdRows int
		var holdEntries, holdTxns, idempotencyRows int
		var holdDebit, holdCredit bool
		var keyStatus, resourceType, resourceID string
		err := tx.QueryRow(ctx, `SELECT o.status,r.state,p.stock_reserved,p.stock_sold,
			pc.reserved,pc.purchased,cr.status,c.reserved_count,c.redeemed_count,bh.status,
			aa.balance_signed * CASE aa.normal_balance WHEN 'credit' THEN -1 ELSE 1 END,
			ha.balance_signed * CASE ha.normal_balance WHEN 'credit' THEN -1 ELSE 1 END,
			(SELECT count(*) FROM order_items x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM order_stock_reservations x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM order_purchase_limit_reservations x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM coupon_redemptions x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM balance_holds x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM ledger_entries le WHERE le.transaction_id=bh.hold_txn_id),
			(SELECT count(*) FROM ledger_transactions lt WHERE lt.tenant_id=o.tenant_id
			 AND lt.kind='balance_hold' AND lt.source_type='order' AND lt.source_id=o.id),
			EXISTS(SELECT 1 FROM ledger_entries le WHERE le.transaction_id=bh.hold_txn_id
			 AND le.account_id=bh.available_account_id AND le.direction='debit' AND le.amount=200),
			EXISTS(SELECT 1 FROM ledger_entries le WHERE le.transaction_id=bh.hold_txn_id
			 AND le.account_id=bh.hold_account_id AND le.direction='credit' AND le.amount=200),
			k.status,k.resource_type,k.resource_id::text,
			(SELECT count(*) FROM idempotency_keys x WHERE x.tenant_id=o.tenant_id
			 AND x.id=o.idempotency_key_id AND x.status='succeeded'
			 AND x.resource_type='order' AND x.resource_id=o.id)
			FROM orders o
			JOIN idempotency_keys k ON k.tenant_id=o.tenant_id AND k.id=o.idempotency_key_id
			JOIN order_reservations r ON r.tenant_id=o.tenant_id AND r.order_id=o.id
			JOIN order_items oi ON oi.tenant_id=o.tenant_id AND oi.order_id=o.id
			JOIN plans p ON p.tenant_id=oi.tenant_id AND p.id=oi.plan_id
			JOIN plan_purchase_counters pc ON pc.tenant_id=o.tenant_id
			 AND pc.plan_id=oi.plan_id AND pc.user_id=o.user_id
			JOIN coupon_redemptions cr ON cr.tenant_id=o.tenant_id AND cr.order_id=o.id
			JOIN coupons c ON c.tenant_id=cr.tenant_id AND c.id=cr.coupon_id
			JOIN balance_holds bh ON bh.tenant_id=o.tenant_id AND bh.order_id=o.id
			JOIN ledger_accounts aa ON aa.tenant_id=bh.tenant_id AND aa.id=bh.available_account_id
			JOIN ledger_accounts ha ON ha.tenant_id=bh.tenant_id AND ha.id=bh.hold_account_id
			WHERE o.tenant_id=$1 AND o.id=$2::uuid`, fx.tenant, orderID).Scan(
			&orderStatus, &reservationState, &stockReserved, &stockSold,
			&purchaseReserved, &purchased, &redemptionState, &couponReserved,
			&couponRedeemed, &holdState, &availableBalance, &holdBalance,
			&itemRows, &stockRows, &purchaseRows, &redemptionRows, &holdRows,
			&holdEntries, &holdTxns, &holdDebit, &holdCredit,
			&keyStatus, &resourceType, &resourceID, &idempotencyRows)
		if err != nil {
			return err
		}
		if orderStatus != "pending_payment" || reservationState != "held" ||
			stockReserved != 1 || stockSold != 0 || purchaseReserved != 1 || purchased != 0 ||
			redemptionState != "held" || couponReserved != 1 || couponRedeemed != 0 ||
			holdState != "held" || availableBalance != fundedBalance-200 || holdBalance != 200 ||
			itemRows != 1 || stockRows != 1 || purchaseRows != 1 || redemptionRows != 1 || holdRows != 1 ||
			holdEntries != 2 || holdTxns != 1 || !holdDebit || !holdCredit ||
			keyStatus != "succeeded" || resourceType != "order" || resourceID != orderID || idempotencyRows != 1 {
			t.Fatalf("held resources order=%s reservation=%s stock=%d/%d purchase=%d/%d coupon=%s/%d/%d hold=%s balances=%d/%d rows=%d/%d/%d/%d/%d ledger=%d/%d/%t/%t key=%s/%s/%s/%d",
				orderStatus, reservationState, stockReserved, stockSold,
				purchaseReserved, purchased, redemptionState, couponReserved,
				couponRedeemed, holdState, availableBalance, holdBalance,
				itemRows, stockRows, purchaseRows, redemptionRows, holdRows,
				holdEntries, holdTxns, holdDebit, holdCredit,
				keyStatus, resourceType, resourceID, idempotencyRows)
		}
		return nil
	})
}

func orderReleasePG18AssertReleasedResources(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, fx orderReleasePG18Fixture, userID, orderID, wantStatus string,
	fundedBalance int64) {
	t.Helper()
	// 和 AssertHeldResources 同理：要 JOIN idempotency_keys，而那张表的 RLS
	// 按 actor 过滤，只给租户的话它一行都看不见。
	orderReleasePG18InTxAs(t, ctx, pool, fx.tenant, userID, func(tx pgx.Tx) error {
		var orderStatus, reservationState, redemptionState, redemptionReason, holdState string
		var stockReserved, stockSold, purchaseReserved, purchased int
		var couponReserved, couponRedeemed int
		var availableBalance, holdBalance int64
		var releaseTxnID string
		var itemRows, stockRows, purchaseRows, redemptionRows, holdRows int
		var holdEntries, holdTxns, releaseEntries, releaseTxns, idempotencyRows int
		var redemptionReleased, redemptionReverted, redemptionUncaptured bool
		var holdReleased, holdUncaptured bool
		var holdDebit, holdCredit, releaseDebit, releaseCredit bool
		var keyStatus, resourceType, resourceID string
		err := tx.QueryRow(ctx, `SELECT o.status,r.state,p.stock_reserved,p.stock_sold,
			pc.reserved,pc.purchased,cr.status,cr.revert_reason,
			cr.released_at IS NOT NULL,cr.reverted_at IS NOT NULL,cr.captured_at IS NULL,
			c.reserved_count,c.redeemed_count,bh.status,
			bh.released_at IS NOT NULL,bh.captured_at IS NULL,
			aa.balance_signed * CASE aa.normal_balance WHEN 'credit' THEN -1 ELSE 1 END,
			ha.balance_signed * CASE ha.normal_balance WHEN 'credit' THEN -1 ELSE 1 END,
			bh.release_txn_id::text,
			(SELECT count(*) FROM order_items x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM order_stock_reservations x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM order_purchase_limit_reservations x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM coupon_redemptions x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM balance_holds x WHERE x.tenant_id=o.tenant_id AND x.order_id=o.id),
			(SELECT count(*) FROM ledger_entries le WHERE le.transaction_id=bh.hold_txn_id),
			(SELECT count(*) FROM ledger_transactions lt WHERE lt.tenant_id=o.tenant_id
			 AND lt.kind='balance_hold' AND lt.source_type='order' AND lt.source_id=o.id),
			(SELECT count(*) FROM ledger_entries le WHERE le.transaction_id=bh.release_txn_id),
			(SELECT count(*) FROM ledger_transactions lt
			  WHERE lt.tenant_id=o.tenant_id
			    AND lt.kind='balance_release' AND lt.source_type='order' AND lt.source_id=o.id),
			EXISTS(SELECT 1 FROM ledger_entries le WHERE le.transaction_id=bh.hold_txn_id
			 AND le.account_id=bh.available_account_id AND le.direction='debit' AND le.amount=200),
			EXISTS(SELECT 1 FROM ledger_entries le WHERE le.transaction_id=bh.hold_txn_id
			 AND le.account_id=bh.hold_account_id AND le.direction='credit' AND le.amount=200),
			EXISTS(SELECT 1 FROM ledger_entries le WHERE le.transaction_id=bh.release_txn_id
			 AND le.account_id=bh.hold_account_id AND le.direction='debit' AND le.amount=200),
			EXISTS(SELECT 1 FROM ledger_entries le WHERE le.transaction_id=bh.release_txn_id
			 AND le.account_id=bh.available_account_id AND le.direction='credit' AND le.amount=200),
			k.status,k.resource_type,k.resource_id::text,
			(SELECT count(*) FROM idempotency_keys x WHERE x.tenant_id=o.tenant_id
			 AND x.id=o.idempotency_key_id AND x.status='succeeded'
			 AND x.resource_type='order' AND x.resource_id=o.id)
			FROM orders o
			JOIN idempotency_keys k ON k.tenant_id=o.tenant_id AND k.id=o.idempotency_key_id
			JOIN order_reservations r ON r.tenant_id=o.tenant_id AND r.order_id=o.id
			JOIN order_items oi ON oi.tenant_id=o.tenant_id AND oi.order_id=o.id
			JOIN plans p ON p.tenant_id=oi.tenant_id AND p.id=oi.plan_id
			JOIN plan_purchase_counters pc ON pc.tenant_id=o.tenant_id
			 AND pc.plan_id=oi.plan_id AND pc.user_id=o.user_id
			JOIN coupon_redemptions cr ON cr.tenant_id=o.tenant_id AND cr.order_id=o.id
			JOIN coupons c ON c.tenant_id=cr.tenant_id AND c.id=cr.coupon_id
			JOIN balance_holds bh ON bh.tenant_id=o.tenant_id AND bh.order_id=o.id
			JOIN ledger_accounts aa ON aa.tenant_id=bh.tenant_id AND aa.id=bh.available_account_id
			JOIN ledger_accounts ha ON ha.tenant_id=bh.tenant_id AND ha.id=bh.hold_account_id
			WHERE o.tenant_id=$1 AND o.id=$2::uuid`, fx.tenant, orderID).Scan(
			&orderStatus, &reservationState, &stockReserved, &stockSold,
			&purchaseReserved, &purchased, &redemptionState, &redemptionReason,
			&redemptionReleased, &redemptionReverted, &redemptionUncaptured,
			&couponReserved, &couponRedeemed, &holdState, &holdReleased,
			&holdUncaptured, &availableBalance, &holdBalance,
			&releaseTxnID, &itemRows, &stockRows, &purchaseRows, &redemptionRows, &holdRows,
			&holdEntries, &holdTxns, &releaseEntries, &releaseTxns,
			&holdDebit, &holdCredit, &releaseDebit, &releaseCredit,
			&keyStatus, &resourceType, &resourceID, &idempotencyRows)
		if err != nil {
			return err
		}
		wantReason := "user_cancelled"
		if wantStatus == "expired" {
			wantReason = "reservation_expired"
		}
		if orderStatus != wantStatus || reservationState != "released" ||
			stockReserved != 0 || stockSold != 0 || purchaseReserved != 0 || purchased != 0 ||
			redemptionState != "released" || couponReserved != 0 || couponRedeemed != 0 ||
			redemptionReason != wantReason || !redemptionReleased || !redemptionReverted ||
			!redemptionUncaptured || holdState != "released" || !holdReleased || !holdUncaptured ||
			availableBalance != fundedBalance || holdBalance != 0 ||
			releaseTxnID == "" || itemRows != 1 || stockRows != 1 || purchaseRows != 1 ||
			redemptionRows != 1 || holdRows != 1 || holdEntries != 2 || holdTxns != 1 ||
			releaseEntries != 2 || releaseTxns != 1 || !holdDebit || !holdCredit ||
			!releaseDebit || !releaseCredit || keyStatus != "succeeded" || resourceType != "order" ||
			resourceID != orderID || idempotencyRows != 1 {
			t.Fatalf("released resources order=%s reservation=%s stock=%d/%d purchase=%d/%d coupon=%s/%s/%t/%t/%t/%d/%d hold=%s/%t/%t balances=%d/%d release=%s rows=%d/%d/%d/%d/%d ledger=%d/%d/%d/%d/%t/%t/%t/%t key=%s/%s/%s/%d",
				orderStatus, reservationState, stockReserved, stockSold,
				purchaseReserved, purchased, redemptionState, redemptionReason,
				redemptionReleased, redemptionReverted, redemptionUncaptured,
				couponReserved, couponRedeemed, holdState, holdReleased, holdUncaptured,
				availableBalance, holdBalance,
				releaseTxnID, itemRows, stockRows, purchaseRows, redemptionRows, holdRows,
				holdEntries, holdTxns, releaseEntries, releaseTxns,
				holdDebit, holdCredit, releaseDebit, releaseCredit,
				keyStatus, resourceType, resourceID, idempotencyRows)
		}
		return nil
	})
}

func orderReleasePG18InTx(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID string, fn func(pgx.Tx) error) {
	t.Helper()
	orderReleasePG18InTxAs(t, ctx, pool, tenantID, "", fn)
}

// orderReleasePG18InTxAs 在指定租户 + 指定操作者的作用域里跑一个事务。
//
// 光有租户不够：idempotency_keys 上挂着 idempotency_keys_actor_select 策略，
// 按 actor 再过滤一层。只设 TenantID 的话，那张表在这个事务里看起来是空的——
// AssertHeldResources 那条 11 表 JOIN 因此永远返回 no rows，而报错只说
// "no rows in result set"，看不出是哪一张表被 RLS 挡住了。
func orderReleasePG18InTxAs(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID, actorID string, fn func(pgx.Tx) error) {
	t.Helper()
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: actorID}, fn); err != nil {
		t.Fatalf("aegis_app tenant transaction: %v", err)
	}
}

func orderReleasePG18Fingerprint(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID, orderID string) string {
	t.Helper()
	var status, reservation string
	var events, audits, payments, cases, txns int
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT o.status,r.state,
			(SELECT count(*) FROM order_reservation_events e WHERE e.tenant_id=o.tenant_id AND e.order_id=o.id),
			(SELECT count(*) FROM audit_events a WHERE a.tenant_id=o.tenant_id AND a.resource_id=o.id),
			(SELECT count(*) FROM payments p WHERE p.tenant_id=o.tenant_id AND p.order_id=o.id),
			(SELECT count(*) FROM late_payment_cases c WHERE c.tenant_id=o.tenant_id AND c.order_id=o.id),
			(SELECT count(*) FROM ledger_transactions l WHERE l.tenant_id=o.tenant_id AND l.source_type='order' AND l.source_id=o.id)
			FROM orders o JOIN order_reservations r ON r.tenant_id=o.tenant_id AND r.order_id=o.id
			WHERE o.tenant_id=$1 AND o.id=$2::uuid`, tenantID, orderID).
			Scan(&status, &reservation, &events, &audits, &payments, &cases, &txns)
	})
	return fmt.Sprintf("%s/%s/events=%d/audits=%d/payments=%d/cases=%d/order_txns=%d",
		status, reservation, events, audits, payments, cases, txns)
}

func orderReleasePG18AssertReleased(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID, orderID, status, eventKind string, eventCount int) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		var gotStatus, reservation string
		var events, terminalEvents, audits, payments, cases int
		err := tx.QueryRow(ctx, `SELECT o.status,r.state,
			(SELECT count(*) FROM order_reservation_events e WHERE e.tenant_id=o.tenant_id AND e.order_id=o.id),
			(SELECT count(*) FROM order_reservation_events e WHERE e.tenant_id=o.tenant_id AND e.order_id=o.id AND e.event_kind=$3),
			(SELECT count(*) FROM audit_events a WHERE a.tenant_id=o.tenant_id
			 AND a.resource_type='order' AND a.resource_id=o.id AND a.action=$4),
			(SELECT count(*) FROM payments p WHERE p.tenant_id=o.tenant_id AND p.order_id=o.id),
			(SELECT count(*) FROM late_payment_cases c WHERE c.tenant_id=o.tenant_id AND c.order_id=o.id)
			FROM orders o JOIN order_reservations r ON r.tenant_id=o.tenant_id AND r.order_id=o.id
			WHERE o.tenant_id=$1 AND o.id=$2::uuid`, tenantID, orderID, eventKind, "order."+status).
			Scan(&gotStatus, &reservation, &events, &terminalEvents, &audits, &payments, &cases)
		if err != nil {
			return err
		}
		if gotStatus != status || reservation != "released" || events != eventCount ||
			terminalEvents != 1 || audits != 1 || payments != 0 || cases != 0 {
			t.Fatalf("released graph status=%s reservation=%s events=%d terminal=%d audits=%d payments=%d cases=%d",
				gotStatus, reservation, events, terminalEvents, audits, payments, cases)
		}
		return nil
	})
}

func orderReleasePG18AssertOrderStatus(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, orderID, want string) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		var got string
		if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, orderID).Scan(&got); err != nil {
			return err
		}
		if got != want {
			t.Fatalf("order status=%s want=%s", got, want)
		}
		return nil
	})
}

func orderReleasePG18AssertQuarantine(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, orderID, providerPaymentID, providerCode, caseKind string,
	amount, fee int64, processedEvents, ignoredEvents int) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		var paymentID, txnID, gotKind, currency, eventStatus string
		var gotAmount, gotFee int64
		err := tx.QueryRow(ctx, `SELECT p.id::text,c.suspense_txn_id::text,c.case_kind,
			c.currency::text,p.amount,p.fee_amount,e.processing_status
			FROM payments p JOIN late_payment_cases c
			  ON c.tenant_id=p.tenant_id AND c.payment_id=p.id
			JOIN payment_events e ON e.tenant_id=c.tenant_id AND e.id=c.payment_event_id
			WHERE p.tenant_id=$1 AND p.order_id=$2::uuid AND p.provider_payment_id=$3`,
			tenantID, orderID, providerPaymentID).
			Scan(&paymentID, &txnID, &gotKind, &currency, &gotAmount, &gotFee, &eventStatus)
		if err != nil {
			return err
		}
		if gotKind != caseKind || currency != "CNY" || gotAmount != amount ||
			gotFee != fee || eventStatus != "processed" {
			t.Fatalf("quarantine case payment=%s txn=%s kind=%s currency=%s amount=%d fee=%d event=%s",
				paymentID, txnID, gotKind, currency, gotAmount, gotFee, eventStatus)
		}
		var cases, payments, audits, processed, ignored, txns int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM late_payment_cases WHERE tenant_id=$1 AND payment_id=$2::uuid),
			(SELECT count(*) FROM payments WHERE tenant_id=$1 AND provider_payment_id=$3),
			(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='payment.quarantined' AND resource_id=$2::uuid),
			(SELECT count(*) FROM payment_events WHERE tenant_id=$1 AND provider_payment_id=$3 AND processing_status='processed'),
			(SELECT count(*) FROM payment_events WHERE tenant_id=$1 AND provider_payment_id=$3 AND processing_status='ignored'),
			(SELECT count(*) FROM ledger_transactions WHERE tenant_id=$1 AND id=$4::uuid AND kind='late_payment_suspense' AND source_type='payment' AND source_id=$2::uuid)`,
			tenantID, paymentID, providerPaymentID, txnID).
			Scan(&cases, &payments, &audits, &processed, &ignored, &txns); err != nil {
			return err
		}
		if cases != 1 || payments != 1 || audits != 1 || processed != processedEvents ||
			ignored != ignoredEvents || txns != 1 {
			t.Fatalf("quarantine cardinality cases=%d payments=%d audits=%d processed=%d ignored=%d txns=%d",
				cases, payments, audits, processed, ignored, txns)
		}
		rows, err := tx.Query(ctx, `SELECT a.account_type,a.owner_ref,e.direction,e.amount
			FROM ledger_entries e JOIN ledger_accounts a
			  ON a.tenant_id=e.tenant_id AND a.id=e.account_id
			WHERE e.tenant_id=$1 AND e.transaction_id=$2::uuid`, tenantID, txnID)
		if err != nil {
			return err
		}
		defer rows.Close()
		got := map[string]int64{}
		for rows.Next() {
			var account, direction string
			var owner *string
			var entryAmount int64
			if err := rows.Scan(&account, &owner, &direction, &entryAmount); err != nil {
				return err
			}
			ownerText := ""
			if owner != nil {
				ownerText = *owner
			}
			got[account+"/"+ownerText+"/"+direction] = entryAmount
		}
		if err := rows.Err(); err != nil {
			return err
		}
		want := map[string]int64{
			"channel_cash/" + providerCode + "/debit": amount - fee,
			"late_payment_suspense/main/credit":       amount,
		}
		// 与 00040 守卫的分录形状一致：没有手续费就没有手续费那一条
		if fee > 0 {
			want["platform_fee_expense/"+providerCode+"/debit"] = fee
		}
		if len(got) != len(want) {
			t.Fatalf("quarantine ledger entry identities=%v", got)
		}
		for key, value := range want {
			if got[key] != value {
				t.Fatalf("quarantine ledger %s=%d want=%d; all=%v", key, got[key], value, got)
			}
		}
		return nil
	})
}

func orderReleasePG18AssertPaymentShape(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, orderID string, payments, cases int) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		var gotPayments, gotCases int
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM payments WHERE tenant_id=$1 AND order_id=$2::uuid),
			(SELECT count(*) FROM late_payment_cases WHERE tenant_id=$1 AND order_id=$2::uuid)`,
			tenantID, orderID).Scan(&gotPayments, &gotCases); err != nil {
			return err
		}
		if gotPayments != payments || gotCases != cases {
			t.Fatalf("payment shape payments=%d cases=%d want=%d/%d",
				gotPayments, gotCases, payments, cases)
		}
		return nil
	})
}

func orderReleasePG18AssertCommissionCount(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, orderID string, want int) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		var got int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commission_entries
			WHERE tenant_id=$1 AND order_id=$2::uuid`, tenantID, orderID).Scan(&got); err != nil {
			return err
		}
		if got != want {
			t.Fatalf("commission count=%d want=%d", got, want)
		}
		return nil
	})
}

func orderReleasePG18AssertCommissionState(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, id, wantStatus, wantAccrual, wantSettle string) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		var status string
		var accrual, settle *string
		if err := tx.QueryRow(ctx, `SELECT status,accrual_txn_id::text,settle_txn_id::text
			FROM commission_entries WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, id).
			Scan(&status, &accrual, &settle); err != nil {
			return err
		}
		actualAccrual, actualSettle := "", ""
		if accrual != nil {
			actualAccrual = *accrual
		}
		if settle != nil {
			actualSettle = *settle
		}
		if status != wantStatus || (wantAccrual != "" && actualAccrual != wantAccrual) ||
			actualSettle != wantSettle {
			t.Fatalf("commission state status=%s accrual=%s settle=%s", status, actualAccrual, actualSettle)
		}
		return nil
	})
}

// orderReleasePG18RejectedByPrivilegeOrGuard 判断一次越权写是否被挡住了，
// 不在乎挡它的是权限层还是守卫触发器。
//
// 提现金额、身份和存在性都不该被运行时角色改动。原先这层保护来自列级
// GRANT，被拒时是 42501；列级白名单放弃之后（见 deploy/configure-app-role.sql
// 末尾），改由 00040 的 guard_withdrawal 触发器拦下，错误码变成 23514。
//
// 被拒这件事没变，变的只是谁来拒。断言盯着「必须被拒」，而不是盯着某一个
// 错误码——否则每换一次拦截层，一堆测试就要跟着改一遍数字。
func orderReleasePG18RejectedByPrivilegeOrGuard(err error) bool {
	switch orderReleasePG18SQLState(err) {
	case "42501", "23514":
		return true
	default:
		return false
	}
}

func orderReleasePG18SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
