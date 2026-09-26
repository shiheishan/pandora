package billing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	settlementPG18Tenant         = "93000000-0000-7000-8000-000000000001"
	settlementPG18User           = "93000000-0000-7000-8000-000000000012"
	settlementPG18Referrer       = "93000000-0000-7000-8000-000000000013"
	settlementPG18CommissionUser = "93000000-0000-7000-8000-000000000014"
	settlementPG18PlanExt        = "93000000-0000-7000-8000-000000000033"
	settlementPG18PriceExt       = "93000000-0000-7000-8000-000000000043"
	settlementPG18PlanMix        = "93000000-0000-7000-8000-000000000034"
	settlementPG18PriceMix       = "93000000-0000-7000-8000-000000000044"
	settlementPG18PlanFault      = "93000000-0000-7000-8000-000000000035"
	settlementPG18PriceFault     = "93000000-0000-7000-8000-000000000045"
	settlementPG18PlanCapture    = "93000000-0000-7000-8000-000000000036"
	settlementPG18PriceCapture   = "93000000-0000-7000-8000-000000000046"
	settlementPG18PlanFulfil     = "93000000-0000-7000-8000-000000000037"
	settlementPG18PriceFulfil    = "93000000-0000-7000-8000-000000000047"
	settlementPG18PlanAudit      = "93000000-0000-7000-8000-000000000038"
	settlementPG18PriceAudit     = "93000000-0000-7000-8000-000000000048"
	settlementPG18PlanCorrupt    = "93000000-0000-7000-8000-000000000039"
	settlementPG18ClaimExt       = "93000000-0000-7000-8000-000000000301"
	settlementPG18ClaimMix       = "93000000-0000-7000-8000-000000000302"
	settlementPG18ClaimTopup     = "93000000-0000-7000-8000-000000000303"
	settlementPG18ClaimFault     = "93000000-0000-7000-8000-000000000304"
	settlementPG18ClaimRace      = "93000000-0000-7000-8000-000000000307"
	settlementPG18ClaimCommA     = "93000000-0000-7000-8000-000000000308"
	settlementPG18ClaimCommB     = "93000000-0000-7000-8000-000000000309"
	settlementPG18ClaimCommFault = "93000000-0000-7000-8000-000000000310"
	settlementPG18ClaimWrongAmt  = "93000000-0000-7000-8000-000000000311"
	settlementPG18ClaimWrongCur  = "93000000-0000-7000-8000-000000000312"
	settlementPG18ClaimCapture   = "93000000-0000-7000-8000-000000000313"
	settlementPG18ClaimFulfil    = "93000000-0000-7000-8000-000000000314"
	settlementPG18ClaimAudit     = "93000000-0000-7000-8000-000000000315"
	settlementPG18ClaimRenewal   = "93000000-0000-7000-8000-000000000319"
	settlementPG18CancelledOrder = "93000000-0000-7000-8000-000000000401"
	settlementPG18ExpiredOrder   = "93000000-0000-7000-8000-000000000402"
	settlementPG18RenewalOrder   = "93000000-0000-7000-8000-000000000403"
	settlementPG18CorruptMissing = "93000000-0000-7000-8000-000000000404"
	settlementPG18CorruptAmount  = "93000000-0000-7000-8000-000000000405"
	settlementPG18CancelledKey   = "93000000-0000-7000-8000-000000000305"
	settlementPG18ExpiredKey     = "93000000-0000-7000-8000-000000000306"
)

func TestSettlementPG18(t *testing.T) {
	dsn := os.Getenv("AEGIS_SETTLEMENT_PG18_DSN")
	if dsn == "" {
		t.Skip("AEGIS_SETTLEMENT_PG18_DSN is not set")
	}
	expectedRunID := os.Getenv("AEGIS_SETTLEMENT_PG18_EXPECT_RUN_ID")
	expectedDBName := os.Getenv("AEGIS_SETTLEMENT_PG18_EXPECT_DB_NAME")
	if expectedRunID == "" || expectedDBName == "" {
		t.Fatal("settlement PG18 disposable-run identity is incomplete")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := platformdb.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open settlement PG18 pool: %v", err)
	}
	defer pool.Close()
	service := NewService(pool, nil)
	lease := settlementPG18Time(t, "2099-07-30T20:00:00Z")

	// 一条可选的超级用户连接，只用来预置「本不该存在」的数据。
	//
	// 佣金唯一冲突那个子用例要制造的状态是：结算进行到一半时，库里已经有一条
	// 同订单的佣金。00040 之后这种状态没法用正常手段造出来——守卫要求佣金对应
	// 的订单已是 paid/fulfilled，而订单一旦 paid，结算就走 AlreadyHandled 分支，
	// 根本到不了佣金那一步。
	//
	// 这条佣金是人为的冲突源，不需要业务上成立，所以用超级用户把触发器临时关掉
	// 塞进去。没配这个 DSN 时跳过那个子用例，而不是让它以「守卫拒绝了预置」的
	// 形式失败——那种失败会被误读成实现有问题。
	var adminConn *pgx.Conn
	if adminDSN := os.Getenv("AEGIS_SETTLEMENT_PG18_ADMIN_DSN"); adminDSN != "" {
		adminConn, err = pgx.Connect(ctx, adminDSN)
		if err != nil {
			t.Fatalf("open settlement PG18 admin conn: %v", err)
		}
		defer adminConn.Close(ctx)
	}

	var databaseName, applicationName, versionNumber string
	var schema40Installed bool
	if err := pool.QueryRow(ctx, `
		SELECT current_database(),current_setting('application_name'),
		       current_setting('server_version_num'),
		       to_regclass('app.order_release_00040_meta') IS NOT NULL`).Scan(
		&databaseName, &applicationName, &versionNumber, &schema40Installed,
	); err != nil {
		t.Fatalf("query settlement PG18 target identity: %v", err)
	}
	if databaseName != expectedDBName || applicationName != "pandora-settlement-"+expectedRunID ||
		len(versionNumber) < 2 || versionNumber[:2] != "18" || !schema40Installed {
		t.Fatalf("wrong settlement PG18 target db=%q app=%q version=%q schema40=%v",
			databaseName, applicationName, versionNumber, schema40Installed)
	}
	t.Logf("marker=settlement_pg18_target_identity_ok run_id=%s database=%s schema=40",
		expectedRunID, databaseName)

	t.Run("runtime aegis_app ACL and forced RLS", func(t *testing.T) {
		settlementPG18AssertRuntimeACL(t, ctx, pool)
		t.Log("marker=settlement_pg18_runtime_acl_rls_ok")
	})

	t.Run("external full settlement and duplicate identities", func(t *testing.T) {
		claim := settlementPG18Claim(settlementPG18ClaimExt, CheckoutIdempotencyScope,
			"settle-ext", "settle-ext-request", lease)
		created, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
			UserID: settlementPG18User, PlanID: settlementPG18PlanExt,
			PriceID: settlementPG18PriceExt, Claim: claim,
		})
		if err != nil {
			t.Fatalf("create external settlement order: %v", err)
		}
		keyBefore := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID)
		input := PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-ext-event-1",
			ProviderPaymentID: "settle-ext-payment-1", EventType: "payment.succeeded",
			OrderID: created.OrderID, Amount: 1000, Currency: "CNY",
			RawPayload:        map[string]any{"case": "external-full", "seq": 1},
			SignatureVerified: true,
		}
		paid := settlementPG18RunWebhookRace(t, ctx, service, 100, func(int) PaymentWebhookInput {
			return input
		})
		if !paid.Processed || paid.AlreadyHandled || paid.PaymentID == "" ||
			paid.SubscriptionID == "" || paid.LedgerTxnID == "" {
			t.Fatalf("external settlement output mismatch: %#v", paid)
		}
		state := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, settlementPG18PlanExt)
		if state.Status != "fulfilled" || state.PaidAmount != 1000 || !state.HasSubscription ||
			state.ReservationState != "captured" || state.ReservationEvents != 2 ||
			state.Items != 1 || state.StockChildren != 1 || state.PurchaseChildren != 0 ||
			state.Coupons != 0 || state.Holds != 0 || state.Payments != 1 ||
			state.OrderTransactions != 1 || state.Subscriptions != 1 ||
			state.Audits != 1 || state.PlanReserved != 0 || state.PlanSold != 1 {
			t.Fatalf("external settlement graph mismatch: %+v", state)
		}
		settlementPG18AssertCaptureEvent(t, ctx, pool, created.OrderID, claim.ID)
		settlementPG18AssertPaymentEvent(t, ctx, pool, "settle-ext-event-1", "processed")
		settlementPG18AssertExternalLedger(t, ctx, pool, paid.LedgerTxnID)
		if got := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID); got != keyBefore {
			t.Fatalf("settlement mutated checkout idempotency evidence before=%s after=%s", keyBefore, got)
		}
		t.Log("marker=settlement_pg18_external_full_ok")

		businessBefore := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID)
		eventsBefore := settlementPG18ProviderEventCount(t, ctx, pool, "settle-ext-payment-1")
		replay, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
		if err != nil {
			t.Fatalf("replay exact external event: %v", err)
		}
		if !replay.AlreadyHandled || replay.Processed || replay.PaymentID != "" ||
			replay.SubscriptionID != "" || replay.LedgerTxnID != "" {
			t.Fatalf("exact duplicate output mismatch: %#v", replay)
		}
		if got := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID); got != businessBefore {
			t.Fatalf("exact duplicate mutated business graph before=%s after=%s", businessBefore, got)
		}
		if got := settlementPG18ProviderEventCount(t, ctx, pool, "settle-ext-payment-1"); got != eventsBefore {
			t.Fatalf("exact duplicate added payment event before=%d after=%d", eventsBefore, got)
		}

		input.ProviderEventID = "settle-ext-event-2"
		input.RawPayload = map[string]any{"case": "same-payment-new-event", "seq": 2}
		secondEvent, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
		if err != nil {
			t.Fatalf("same payment with new event: %v", err)
		}
		// 同一笔支付换了 event_id 重发：事件要被记下，业务不能再动一次。
		//
		// PaymentID 这里期望的是「指回此前那笔支付」，不是空。渠道重发时调用方
		// 靠它对账，返回空等于把已知信息丢掉；SubscriptionID 与 LedgerTxnID 则
		// 必须为空——那两样只在本次真的推进了业务时才有。
		//
		// 这条断言原先要求三个 ID 全空，与 unexpected_payment.go 的实现对不上。
		// 两边都没写下约定，各自成理；现在语义写在 PaymentWebhookOutput 的注释
		// 里，断言按它来，并且比原先更强：不只是「非空」，还必须是同一笔。
		if !secondEvent.AlreadyHandled || secondEvent.Processed ||
			secondEvent.PaymentID != paid.PaymentID ||
			secondEvent.SubscriptionID != "" || secondEvent.LedgerTxnID != "" {
			t.Fatalf("same-payment new-event output mismatch: %#v（PaymentID 应为 %s）",
				secondEvent, paid.PaymentID)
		}
		if got := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID); got != businessBefore {
			t.Fatalf("same payment/new event mutated business graph before=%s after=%s", businessBefore, got)
		}
		if got := settlementPG18ProviderEventCount(t, ctx, pool, "settle-ext-payment-1"); got != eventsBefore+1 {
			t.Fatalf("same payment/new event count=%d want=%d", got, eventsBefore+1)
		}
		settlementPG18AssertPaymentEvent(t, ctx, pool, "settle-ext-event-2", "ignored")
		if got := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID); got != keyBefore {
			t.Fatalf("duplicate identity path mutated idempotency evidence before=%s after=%s", keyBefore, got)
		}
		t.Log("marker=settlement_pg18_duplicate_identity_ok")

		raceClaim := settlementPG18Claim(settlementPG18ClaimRace, CheckoutIdempotencyScope,
			"settle-race", "settle-race-request", lease)
		raceOrder, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
			UserID: settlementPG18User, PlanID: settlementPG18PlanExt,
			PriceID: settlementPG18PriceExt, Claim: raceClaim,
		})
		if err != nil {
			t.Fatalf("create same-payment race order: %v", err)
		}
		racePaid := settlementPG18RunWebhookRace(t, ctx, service, 100, func(i int) PaymentWebhookInput {
			return PaymentWebhookInput{
				ProviderCode: "settlement-demo", ProviderEventID: fmt.Sprintf("settle-race-event-%03d", i),
				ProviderPaymentID: "settle-race-payment-1", EventType: "payment.succeeded",
				OrderID: raceOrder.OrderID, Amount: 1000, Currency: "CNY",
				RawPayload: map[string]any{"case": "same-payment-race", "seq": i}, SignatureVerified: true,
			}
		})
		if racePaid.PaymentID == "" || racePaid.SubscriptionID == "" || racePaid.LedgerTxnID == "" {
			t.Fatalf("same-payment race winner lacks business identities: %#v", racePaid)
		}
		processed, ignored := settlementPG18ProviderEventStatuses(t, ctx, pool, "settle-race-payment-1")
		if processed != 1 || ignored != 99 {
			t.Fatalf("same-payment race event states processed/ignored=%d/%d want=1/99", processed, ignored)
		}
		raceState := settlementPG18OrderStateOf(t, ctx, pool, raceOrder.OrderID, settlementPG18PlanExt)
		if raceState.Status != "fulfilled" || raceState.Payments != 1 || raceState.OrderTransactions != 1 ||
			raceState.ReservationState != "captured" || raceState.ReservationEvents != 2 ||
			raceState.Subscriptions != 1 || raceState.Audits != 1 || raceState.Commissions != 0 {
			t.Fatalf("same-payment race produced non-singleton business graph: %+v", raceState)
		}
		settlementPG18AssertCaptureEvent(t, ctx, pool, raceOrder.OrderID, raceClaim.ID)
		t.Log("marker=settlement_pg18_100_exact_event_ok")
		t.Log("marker=settlement_pg18_100_same_payment_ok")
	})

	t.Run("mixed hold coupon purchase limit and fee", func(t *testing.T) {
		claim := settlementPG18Claim(settlementPG18ClaimMix, CheckoutIdempotencyScope,
			"settle-mix", "settle-mix-request", lease)
		created, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
			UserID: settlementPG18User, PlanID: settlementPG18PlanMix,
			PriceID: settlementPG18PriceMix, CouponCode: "MIX300",
			UseBalance: 700, Claim: claim,
		})
		if err != nil {
			t.Fatalf("create mixed settlement order: %v", err)
		}
		if created.DiscountAmount != 300 || created.TotalAmount != 1700 ||
			created.BalanceApplied != 700 || created.PayableAmount != 1000 ||
			created.Status != "pending_payment" {
			t.Fatalf("mixed create output mismatch: %#v", created)
		}
		keyBefore := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID)
		paid, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-mix-event-1",
			ProviderPaymentID: "settle-mix-payment-1", EventType: "payment.succeeded",
			OrderID: created.OrderID, Amount: 1000, Currency: "CNY", FeeAmount: 30,
			RawPayload: map[string]any{"case": "mixed"}, SignatureVerified: true,
		})
		if err != nil {
			t.Fatalf("settle mixed order: %v", err)
		}
		if !paid.Processed || paid.AlreadyHandled || paid.PaymentID == "" ||
			paid.SubscriptionID == "" || paid.LedgerTxnID == "" {
			t.Fatalf("mixed settlement output mismatch: %#v", paid)
		}
		settlementPG18AssertMixedGraph(t, ctx, pool, created.OrderID, paid.LedgerTxnID)
		settlementPG18AssertMixedLedger(t, ctx, pool, paid.LedgerTxnID)
		state := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, settlementPG18PlanMix)
		if state.Status != "fulfilled" || state.Payments != 1 || state.OrderTransactions != 2 ||
			state.Subscriptions != 1 || state.Audits != 1 || state.Commissions != 0 ||
			state.ReservationState != "captured" || state.ReservationEvents != 2 {
			t.Fatalf("mixed exact settlement counts mismatch: %+v", state)
		}
		settlementPG18AssertCaptureEvent(t, ctx, pool, created.OrderID, claim.ID)
		settlementPG18AssertPaymentEvent(t, ctx, pool, "settle-mix-event-1", "processed")
		if got := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID); got != keyBefore {
			t.Fatalf("mixed settlement mutated idempotency evidence before=%s after=%s", keyBefore, got)
		}
		t.Log("marker=settlement_pg18_mixed_hold_fee_ok")
	})

	t.Run("topup captures parent only", func(t *testing.T) {
		claim := settlementPG18Claim(settlementPG18ClaimTopup, TopupIdempotencyScope,
			"settle-topup", "settle-topup-request", lease)
		created, err := service.CreateTopup(ctx, settlementPG18Tenant, CreateTopupInput{
			UserID: settlementPG18User, Amount: 500, Currency: "CNY", Claim: claim,
		})
		if err != nil {
			t.Fatalf("create topup settlement order: %v", err)
		}
		keyBefore := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID)
		paid, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-topup-event-1",
			ProviderPaymentID: "settle-topup-payment-1", EventType: "payment.succeeded",
			OrderID: created.OrderID, Amount: 500, Currency: "CNY", FeeAmount: 10,
			RawPayload: map[string]any{"case": "topup"}, SignatureVerified: true,
		})
		if err != nil {
			t.Fatalf("settle topup: %v", err)
		}
		if !paid.Processed || paid.AlreadyHandled || paid.PaymentID == "" ||
			paid.LedgerTxnID == "" || paid.SubscriptionID != "" {
			t.Fatalf("topup settlement output mismatch: %#v", paid)
		}
		// 充值的履约就是余额落账，结算事务里直接置为 fulfilled（settlement.go 的
		// topup 分支）。这里原先断言 paid，写在那条收尾逻辑加入之前。
		state := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, "")
		if state.Status != "fulfilled" || state.PaidAmount != 500 || state.HasSubscription ||
			state.ReservationState != "captured" || state.ReservationEvents != 2 ||
			state.Items != 0 || state.StockChildren != 0 || state.PurchaseChildren != 0 ||
			state.Coupons != 0 || state.Holds != 0 || state.Payments != 1 ||
			state.OrderTransactions != 1 || state.Subscriptions != 0 ||
			state.Audits != 1 || state.Commissions != 0 {
			t.Fatalf("topup parent-only graph mismatch: %+v", state)
		}
		settlementPG18AssertCaptureEvent(t, ctx, pool, created.OrderID, claim.ID)
		settlementPG18AssertPaymentEvent(t, ctx, pool, "settle-topup-event-1", "processed")
		settlementPG18AssertTopupLedger(t, ctx, pool, paid.LedgerTxnID)
		if got := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID); got != keyBefore {
			t.Fatalf("topup settlement mutated idempotency evidence before=%s after=%s", keyBefore, got)
		}
		t.Log("marker=settlement_pg18_topup_parent_only_ok")
	})

	t.Run("commission accrual shared locks and unique conflict rollback", func(t *testing.T) {
		claims := []middleware.IdempotencyClaim{
			settlementPG18ClaimFor(settlementPG18CommissionUser, settlementPG18ClaimCommA,
				CheckoutIdempotencyScope, "settle-commission-a", "settle-commission-a-request", lease),
			settlementPG18ClaimFor(settlementPG18CommissionUser, settlementPG18ClaimCommB,
				CheckoutIdempotencyScope, "settle-commission-b", "settle-commission-b-request", lease),
		}
		orders := make([]*CreateOrderOutput, len(claims))
		for i := range claims {
			created, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
				UserID: settlementPG18CommissionUser, PlanID: settlementPG18PlanExt,
				PriceID: settlementPG18PriceExt, Claim: claims[i],
			})
			if err != nil {
				t.Fatalf("create commission shared-lock order %d: %v", i, err)
			}
			orders[i] = created
		}
		inputs := make([]PaymentWebhookInput, len(orders))
		for i := range orders {
			inputs[i] = PaymentWebhookInput{
				ProviderCode: "settlement-demo", ProviderEventID: fmt.Sprintf("settle-commission-event-%d", i),
				ProviderPaymentID: fmt.Sprintf("settle-commission-payment-%d", i), EventType: "payment.succeeded",
				OrderID: orders[i].OrderID, Amount: 1000, Currency: "CNY", FeeAmount: 10,
				RawPayload: map[string]any{"case": "commission-shared-lock", "seq": i}, SignatureVerified: true,
			}
		}
		paid := settlementPG18RunConcurrentWebhooks(t, ctx, service, inputs)
		for i := range paid {
			if !paid[i].Processed || paid[i].AlreadyHandled || paid[i].PaymentID == "" ||
				paid[i].SubscriptionID == "" || paid[i].LedgerTxnID == "" {
				t.Fatalf("commission shared-lock output %d mismatch: %#v", i, paid[i])
			}
			state := settlementPG18OrderStateOf(t, ctx, pool, orders[i].OrderID, settlementPG18PlanExt)
			if state.Status != "fulfilled" || state.Payments != 1 || state.OrderTransactions != 2 ||
				state.ReservationState != "captured" || state.ReservationEvents != 2 ||
				state.Subscriptions != 1 || state.Audits != 1 || state.Commissions != 1 {
				t.Fatalf("commission shared-lock graph %d mismatch: %+v", i, state)
			}
			settlementPG18AssertCaptureEvent(t, ctx, pool, orders[i].OrderID, claims[i].ID)
			settlementPG18AssertPaymentEvent(t, ctx, pool, inputs[i].ProviderEventID, "processed")
			settlementPG18AssertCommission(t, ctx, pool, orders[i].OrderID)
		}
		t.Log("marker=settlement_pg18_commission_success_shared_locks_ok")

		faultClaim := settlementPG18ClaimFor(settlementPG18CommissionUser,
			settlementPG18ClaimCommFault, CheckoutIdempotencyScope,
			"settle-commission-conflict", "settle-commission-conflict-request", lease)
		faultOrder, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
			UserID: settlementPG18CommissionUser, PlanID: settlementPG18PlanExt,
			PriceID: settlementPG18PriceExt, Claim: faultClaim,
		})
		if err != nil {
			t.Fatalf("create commission conflict order: %v", err)
		}
		// 预置一条同订单的佣金，制造结算时的唯一冲突。
		//
		// 它是人为的冲突源，业务上不成立也没关系——这个用例要验的是「撞上唯一
		// 约束时整笔结算回滚、不留半成品」，不是佣金本身合不合规。
		//
		// 必须绕过触发器：00040 的 assert_commission_entry 要求佣金对应的订单
		// 已是 paid/fulfilled，而订单一旦 paid，结算就走 AlreadyHandled，到不了
		// 佣金那步——这个状态在正常路径上不可达。用超级用户把 session 切到
		// replica 角色，插完立刻切回。
		if adminConn == nil {
			t.Skip("需要 AEGIS_SETTLEMENT_PG18_ADMIN_DSN 才能预置冲突源")
		}
		if _, err := adminConn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
			t.Fatalf("切换到 replica 角色: %v", err)
		}
		_, seedErr := adminConn.Exec(ctx, `INSERT INTO commission_entries
			(tenant_id,referrer_user_id,referee_user_id,order_id,currency,
			 base_amount,rate_bp,commission_amount,frozen_until,review_required)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,'CNY',1000,1000,100,
			        now()+interval '1 day',false)`, settlementPG18Tenant,
			settlementPG18Referrer, settlementPG18CommissionUser, faultOrder.OrderID)
		if _, err := adminConn.Exec(ctx, `SET session_replication_role = origin`); err != nil {
			t.Fatalf("切回 origin 角色: %v", err)
		}
		if seedErr != nil {
			t.Fatalf("pre-seed commission conflict: %v", seedErr)
		}
		before := settlementPG18OrderFingerprint(t, ctx, pool, faultOrder.OrderID)
		resourcesBefore := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanExt)
		keyBefore := settlementPG18KeyFingerprintFor(t, ctx, pool,
			settlementPG18CommissionUser, faultClaim.ID)
		_, err = service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-commission-conflict-event",
			ProviderPaymentID: "settle-commission-conflict-payment", EventType: "payment.succeeded",
			OrderID: faultOrder.OrderID, Amount: 1000, Currency: "CNY",
			RawPayload: map[string]any{"case": "commission-unique-conflict"}, SignatureVerified: true,
		})
		var he *httpx.Error
		if err == nil || !errors.As(err, &he) || he.Code != httpx.CodeInternal {
			t.Fatalf("commission unique conflict error=%v want internal_error", err)
		}
		if settlementPG18IsDeadlock(err) {
			t.Fatalf("commission unique conflict returned SQLSTATE 40P01: %v", err)
		}
		if got := settlementPG18OrderFingerprint(t, ctx, pool, faultOrder.OrderID); got != before {
			t.Fatalf("commission conflict left order residue before=%s after=%s", before, got)
		}
		if got := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanExt); got != resourcesBefore {
			t.Fatalf("commission conflict left account/resource residue before=%s after=%s", resourcesBefore, got)
		}
		if got := settlementPG18KeyFingerprintFor(t, ctx, pool,
			settlementPG18CommissionUser, faultClaim.ID); got != keyBefore {
			t.Fatalf("commission conflict mutated idempotency evidence before=%s after=%s", keyBefore, got)
		}
		if got := settlementPG18ProviderEventCount(t, ctx, pool, "settle-commission-conflict-payment"); got != 0 {
			t.Fatalf("commission conflict retained provider identity count=%d", got)
		}
		state := settlementPG18OrderStateOf(t, ctx, pool, faultOrder.OrderID, settlementPG18PlanExt)
		if state.Status != "pending_payment" || state.Payments != 0 || state.OrderTransactions != 0 ||
			state.ReservationState != "held" || state.ReservationEvents != 1 ||
			state.Subscriptions != 0 || state.Audits != 0 || state.Commissions != 1 {
			t.Fatalf("commission conflict rollback graph mismatch: %+v", state)
		}
		t.Log("marker=settlement_pg18_commission_conflict_rollback_ok")
	})

	t.Run("fault matrix rolls back then same identity retries exactly once", func(t *testing.T) {
		cases := []struct {
			name, marker, planID, priceID, claimID, key, eventID, paymentID string
		}{
			{"after-ledger", "after_ledger", settlementPG18PlanFault, settlementPG18PriceFault,
				settlementPG18ClaimFault, "settle-fault-ledger", "settle-fault-ledger-event", "settle-fault-ledger-payment"},
			{"after-capture", "after_capture", settlementPG18PlanCapture, settlementPG18PriceCapture,
				settlementPG18ClaimCapture, "settle-fault-capture", "settle-fault-capture-event", "settle-fault-capture-payment"},
			{"after-fulfil", "after_fulfil", settlementPG18PlanFulfil, settlementPG18PriceFulfil,
				settlementPG18ClaimFulfil, "settle-fault-fulfil", "settle-fault-fulfil-event", "settle-fault-fulfil-payment"},
			{"after-audit", "after_audit", settlementPG18PlanAudit, settlementPG18PriceAudit,
				settlementPG18ClaimAudit, "settle-fault-audit", "settle-fault-audit-event", "settle-fault-audit-payment"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				claim := settlementPG18Claim(tc.claimID, CheckoutIdempotencyScope,
					tc.key, tc.key+"-request", lease)
				created, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
					UserID: settlementPG18User, PlanID: tc.planID, PriceID: tc.priceID, Claim: claim,
				})
				if err != nil {
					t.Fatalf("create %s fault order: %v", tc.name, err)
				}
				input := PaymentWebhookInput{
					ProviderCode: "settlement-demo", ProviderEventID: tc.eventID,
					ProviderPaymentID: tc.paymentID, EventType: "payment.succeeded",
					OrderID: created.OrderID, Amount: 1000, Currency: "CNY",
					RawPayload: map[string]any{"case": tc.name}, SignatureVerified: true,
				}
				before := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID)
				resourcesBefore := settlementPG18ResourceFingerprint(t, ctx, pool, tc.planID)
				keyBefore := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID)
				_, err = service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
				var he *httpx.Error
				if err == nil || !errors.As(err, &he) || he.Code != httpx.CodeInternal {
					t.Fatalf("%s fault error=%v want internal_error", tc.name, err)
				}
				if after := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID); after != before {
					t.Fatalf("%s fault left order residue before=%s after=%s", tc.name, before, after)
				}
				if after := settlementPG18ResourceFingerprint(t, ctx, pool, tc.planID); after != resourcesBefore {
					t.Fatalf("%s fault left account/resource residue before=%s after=%s", tc.name, resourcesBefore, after)
				}
				if got := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID); got != keyBefore {
					t.Fatalf("%s fault mutated idempotency evidence before=%s after=%s", tc.name, keyBefore, got)
				}
				if got := settlementPG18ProviderEventCount(t, ctx, pool, tc.paymentID); got != 0 {
					t.Fatalf("%s fault retained provider identity count=%d", tc.name, got)
				}
				failed := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, tc.planID)
				if failed.Status != "pending_payment" || failed.PaidAmount != 0 ||
					failed.ReservationState != "held" || failed.ReservationEvents != 1 ||
					failed.Payments != 0 || failed.OrderTransactions != 0 || failed.Subscriptions != 0 ||
					failed.Audits != 0 || failed.PlanReserved != 1 || failed.PlanSold != 0 {
					t.Fatalf("%s fault rollback graph mismatch: %+v", tc.name, failed)
				}

				paid, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
				if err != nil {
					t.Fatalf("%s same-identity retry after one-shot fault: %v", tc.name, err)
				}
				if !paid.Processed || paid.AlreadyHandled || paid.PaymentID == "" ||
					paid.SubscriptionID == "" || paid.LedgerTxnID == "" {
					t.Fatalf("%s retry output mismatch: %#v", tc.name, paid)
				}
				settlementPG18AssertCaptureEvent(t, ctx, pool, created.OrderID, claim.ID)
				settlementPG18AssertPaymentEvent(t, ctx, pool, tc.eventID, "processed")
				succeeded := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, tc.planID)
				if succeeded.Status != "fulfilled" || succeeded.PaidAmount != 1000 ||
					succeeded.ReservationState != "captured" || succeeded.ReservationEvents != 2 ||
					succeeded.Payments != 1 || succeeded.OrderTransactions != 1 ||
					succeeded.Subscriptions != 1 || succeeded.Audits != 1 ||
					succeeded.PlanReserved != 0 || succeeded.PlanSold != 1 {
					t.Fatalf("%s retry graph mismatch: %+v", tc.name, succeeded)
				}
				business := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID)
				replay, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
				if err != nil || replay == nil || !replay.AlreadyHandled || replay.Processed {
					t.Fatalf("%s post-retry replay output/error=%#v/%v", tc.name, replay, err)
				}
				if got := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID); got != business {
					t.Fatalf("%s post-retry replay mutated graph before=%s after=%s", tc.name, business, got)
				}
				if got := settlementPG18ProviderEventCount(t, ctx, pool, tc.paymentID); got != 1 {
					t.Fatalf("%s post-retry provider identity count=%d want=1", tc.name, got)
				}
				t.Logf("marker=settlement_pg18_fault_%s_retry_ok", tc.marker)
			})
		}
		t.Log("marker=settlement_pg18_fault_matrix_retry_ok")
	})

	t.Run("wrong amount and currency roll back completely", func(t *testing.T) {
		cases := []struct {
			name, claimID, key, eventID, paymentID, currency string
			amount                                           int64
		}{
			{"amount", settlementPG18ClaimWrongAmt, "settle-wrong-amount",
				"settle-wrong-amount-event", "settle-wrong-amount-payment", "CNY", 999},
			{"currency", settlementPG18ClaimWrongCur, "settle-wrong-currency",
				"settle-wrong-currency-event", "settle-wrong-currency-payment", "USD", 1000},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				claim := settlementPG18Claim(tc.claimID, CheckoutIdempotencyScope,
					tc.key, tc.key+"-request", lease)
				created, err := service.CreateOrder(ctx, settlementPG18Tenant, CreateOrderInput{
					UserID: settlementPG18User, PlanID: settlementPG18PlanExt,
					PriceID: settlementPG18PriceExt, Claim: claim,
				})
				if err != nil {
					t.Fatalf("create wrong-%s order: %v", tc.name, err)
				}
				before := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID)
				resourcesBefore := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanExt)
				keyBefore := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID)
				_, err = service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
					ProviderCode: "settlement-demo", ProviderEventID: tc.eventID,
					ProviderPaymentID: tc.paymentID, EventType: "payment.succeeded",
					OrderID: created.OrderID, Amount: tc.amount, Currency: tc.currency,
					RawPayload: map[string]any{"case": "wrong-" + tc.name}, SignatureVerified: true,
				})
				var he *httpx.Error
				if err == nil || !errors.As(err, &he) || he.Code != httpx.CodeConflict {
					t.Fatalf("wrong-%s error=%v want conflict", tc.name, err)
				}
				if got := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID); got != before {
					t.Fatalf("wrong-%s left order residue before=%s after=%s", tc.name, before, got)
				}
				if got := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanExt); got != resourcesBefore {
					t.Fatalf("wrong-%s left account/resource residue before=%s after=%s", tc.name, resourcesBefore, got)
				}
				if got := settlementPG18KeyFingerprint(t, ctx, pool, claim.ID); got != keyBefore {
					t.Fatalf("wrong-%s mutated key before=%s after=%s", tc.name, keyBefore, got)
				}
				if got := settlementPG18ProviderEventCount(t, ctx, pool, tc.paymentID); got != 0 {
					t.Fatalf("wrong-%s retained provider identity count=%d", tc.name, got)
				}
				state := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, settlementPG18PlanExt)
				if state.Status != "pending_payment" || state.PaidAmount != 0 ||
					state.ReservationState != "held" || state.ReservationEvents != 1 ||
					state.Payments != 0 || state.OrderTransactions != 0 ||
					state.Subscriptions != 0 || state.Audits != 0 {
					t.Fatalf("wrong-%s rollback graph mismatch: %+v", tc.name, state)
				}
				t.Logf("marker=settlement_pg18_wrong_%s_rollback_ok", tc.name)
			})
		}
		t.Log("marker=settlement_pg18_wrong_payment_rollback_ok")
	})

	t.Run("legacy renewal without idempotency linkage is fail closed", func(t *testing.T) {
		before := settlementPG18OrderFingerprint(t, ctx, pool, settlementPG18RenewalOrder)
		resourcesBefore := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanExt)
		subscriptionBefore := settlementPG18SubscriptionFingerprint(t, ctx, pool,
			"93000000-0000-7000-8000-000000000480")
		_, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-renewal-event",
			ProviderPaymentID: "settle-renewal-payment", EventType: "payment.succeeded",
			OrderID: settlementPG18RenewalOrder, Amount: 1000, Currency: "CNY",
			RawPayload: map[string]any{"case": "legacy-renewal-fail-closed"}, SignatureVerified: true,
		})
		var he *httpx.Error
		if err == nil || !errors.As(err, &he) || he.Code != httpx.CodeInternal {
			t.Fatalf("renewal settlement error=%v want internal_error", err)
		}
		if got := settlementPG18OrderFingerprint(t, ctx, pool, settlementPG18RenewalOrder); got != before {
			t.Fatalf("renewal settlement left order residue before=%s after=%s", before, got)
		}
		if got := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanExt); got != resourcesBefore {
			t.Fatalf("renewal settlement left account/resource residue before=%s after=%s", resourcesBefore, got)
		}
		if got := settlementPG18SubscriptionFingerprint(t, ctx, pool,
			"93000000-0000-7000-8000-000000000480"); got != subscriptionBefore {
			t.Fatalf("renewal settlement mutated subscription before=%s after=%s", subscriptionBefore, got)
		}
		if got := settlementPG18ProviderEventCount(t, ctx, pool, "settle-renewal-payment"); got != 0 {
			t.Fatalf("renewal settlement retained provider identity count=%d", got)
		}
		state := settlementPG18OrderStateOf(t, ctx, pool, settlementPG18RenewalOrder, "")
		if state.Status != "pending_payment" || state.PaidAmount != 0 || !state.HasSubscription ||
			state.ReservationState != "held" || state.ReservationEvents != 1 ||
			state.Items != 1 || state.StockChildren != 0 || state.PurchaseChildren != 0 ||
			state.Payments != 0 || state.OrderTransactions != 0 ||
			state.Subscriptions != 1 || state.Audits != 0 {
			t.Fatalf("renewal fail-closed graph mismatch: %+v", state)
		}
		t.Log("marker=settlement_pg18_legacy_renewal_fail_closed_ok")
	})

	t.Run("actor-bound renewal settles onto the existing subscription", func(t *testing.T) {
		// The pre-schema legacy renewal above remains intentionally invalid. Free
		// its one-active-renewal slot through the ordinary release path, then
		// create a fresh renewal through the production writer.
		if _, err := service.CancelOrder(ctx, settlementPG18Tenant,
			settlementPG18User, settlementPG18RenewalOrder); err != nil {
			t.Fatalf("cancel legacy renewal fixture: %v", err)
		}

		claim := settlementPG18Claim(settlementPG18ClaimRenewal,
			RenewalIdempotencyScope, "settle-renewal-v2", "settle-renewal-v2-request", lease)
		claim = settlementPG18InsertClaim(t, ctx, pool, claim)
		before := settlementPG18SubscriptionFingerprint(t, ctx, pool,
			"93000000-0000-7000-8000-000000000480")
		created, err := service.CreateRenewal(ctx, settlementPG18Tenant, CreateRenewalInput{
			UserID:         settlementPG18User,
			SubscriptionID: "93000000-0000-7000-8000-000000000480",
			PriceID:        "93000000-0000-7000-8000-000000000043",
			Claim:          claim,
		})
		if err != nil {
			t.Fatalf("create actor-bound renewal: %v", err)
		}
		paid, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-renewal-v2-event",
			ProviderPaymentID: "settle-renewal-v2-payment", EventType: "payment.succeeded",
			OrderID: created.OrderID, Amount: created.PayableAmount, Currency: created.Currency,
			RawPayload: map[string]any{"case": "actor-bound-renewal"}, SignatureVerified: true,
		})
		if err != nil {
			t.Fatalf("settle actor-bound renewal: %v", err)
		}
		if !paid.Processed || paid.AlreadyHandled ||
			paid.SubscriptionID != "93000000-0000-7000-8000-000000000480" {
			t.Fatalf("actor-bound renewal output mismatch: %#v", paid)
		}
		state := settlementPG18OrderStateOf(t, ctx, pool, created.OrderID, "")
		if state.Status != "fulfilled" || state.PaidAmount != created.TotalAmount ||
			!state.HasSubscription || state.ReservationState != "captured" ||
			state.ReservationEvents != 2 || state.Items != 1 ||
			state.StockChildren != 0 || state.PurchaseChildren != 0 ||
			state.Coupons != 0 || state.Holds != 0 || state.Payments != 1 ||
			state.OrderTransactions != 1 || state.Subscriptions != 1 ||
			state.Audits != 1 || state.Commissions != 0 {
			t.Fatalf("actor-bound renewal graph mismatch: %+v", state)
		}
		if after := settlementPG18SubscriptionFingerprint(t, ctx, pool,
			"93000000-0000-7000-8000-000000000480"); after == before {
			t.Fatal("actor-bound renewal did not advance the existing subscription")
		}
		settlementPG18AssertCaptureEvent(t, ctx, pool, created.OrderID, claim.ID)
		settlementPG18AssertPaymentEvent(t, ctx, pool, "settle-renewal-v2-event", "processed")
		fingerprint := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID)
		replay, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
			ProviderCode: "settlement-demo", ProviderEventID: "settle-renewal-v2-event",
			ProviderPaymentID: "settle-renewal-v2-payment", EventType: "payment.succeeded",
			OrderID: created.OrderID, Amount: created.PayableAmount, Currency: created.Currency,
			RawPayload: map[string]any{"case": "actor-bound-renewal"}, SignatureVerified: true,
		})
		if err != nil || !replay.AlreadyHandled || replay.Processed {
			t.Fatalf("actor-bound renewal replay mismatch out=%#v err=%v", replay, err)
		}
		if got := settlementPG18OrderFingerprint(t, ctx, pool, created.OrderID); got != fingerprint {
			t.Fatalf("actor-bound renewal replay mutated graph before=%s after=%s", fingerprint, got)
		}
		t.Log("marker=settlement_pg18_actor_bound_renewal_ok")
	})

	for _, tc := range []struct {
		name, orderID, keyID, eventID, paymentID, wantStatus string
	}{
		{"cancelled", settlementPG18CancelledOrder, settlementPG18CancelledKey,
			"settle-cancelled-event-1", "settle-cancelled-payment-1", "cancelled"},
		{"expired", settlementPG18ExpiredOrder, settlementPG18ExpiredKey,
			"settle-expired-event-1", "settle-expired-payment-1", "expired"},
	} {
		t.Run(tc.name+" order late payment is quarantined", func(t *testing.T) {
			before := settlementPG18OrderFingerprint(t, ctx, pool, tc.orderID)
			protectedBefore := settlementPG18ReleasedGraphFingerprint(t, ctx, pool, tc.orderID)
			keyBefore := settlementPG18KeyFingerprint(t, ctx, pool, tc.keyID)
			input := PaymentWebhookInput{
				ProviderCode: "settlement-demo", ProviderEventID: tc.eventID,
				ProviderPaymentID: tc.paymentID, EventType: "payment.succeeded",
				OrderID: tc.orderID, Amount: 500, Currency: "CNY", FeeAmount: 5,
				RawPayload: map[string]any{"case": tc.name}, SignatureVerified: true,
			}
			out, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
			if err != nil || out == nil || !out.Processed || out.AlreadyHandled ||
				out.PaymentID == "" || out.LedgerTxnID == "" || out.SubscriptionID != "" {
				t.Fatalf("%s quarantine output=%#v err=%v", tc.name, out, err)
			}
			once := settlementPG18OrderFingerprint(t, ctx, pool, tc.orderID)
			if once == before {
				t.Fatalf("%s quarantine did not add payment evidence", tc.name)
			}
			if got := settlementPG18ReleasedGraphFingerprint(t, ctx, pool, tc.orderID); got != protectedBefore {
				t.Fatalf("%s quarantine mutated released resources before=%s after=%s",
					tc.name, protectedBefore, got)
			}
			replay, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, input)
			if err != nil || replay == nil || !replay.AlreadyHandled || replay.Processed {
				t.Fatalf("%s exact-event replay output=%#v err=%v", tc.name, replay, err)
			}
			if got := settlementPG18OrderFingerprint(t, ctx, pool, tc.orderID); got != once {
				t.Fatalf("%s exact-event replay mutated graph before=%s after=%s", tc.name, once, got)
			}
			if got := settlementPG18ReleasedGraphFingerprint(t, ctx, pool, tc.orderID); got != protectedBefore {
				t.Fatalf("%s exact-event replay mutated released resources before=%s after=%s",
					tc.name, protectedBefore, got)
			}
			duplicate := input
			duplicate.ProviderEventID = tc.eventID + "-duplicate"
			duplicate.RawPayload = map[string]any{"case": tc.name + "-provider-payment-replay"}
			replayedPayment, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, duplicate)
			if err != nil || replayedPayment == nil || !replayedPayment.AlreadyHandled || replayedPayment.Processed {
				t.Fatalf("%s provider-payment replay output=%#v err=%v", tc.name, replayedPayment, err)
			}
			if got := settlementPG18OrderFingerprint(t, ctx, pool, tc.orderID); got != once {
				t.Fatalf("%s provider-payment replay mutated graph before=%s after=%s", tc.name, once, got)
			}
			if got := settlementPG18ReleasedGraphFingerprint(t, ctx, pool, tc.orderID); got != protectedBefore {
				t.Fatalf("%s provider-payment replay mutated released resources before=%s after=%s",
					tc.name, protectedBefore, got)
			}
			if got := settlementPG18KeyFingerprint(t, ctx, pool, tc.keyID); got != keyBefore {
				t.Fatalf("%s terminal webhook mutated key before=%s after=%s", tc.name, keyBefore, got)
			}
			orderReleasePG18AssertQuarantine(t, ctx, pool, settlementPG18Tenant,
				tc.orderID, tc.paymentID, "settlement-demo", "released_order", 500, 5, 1, 1)
			state := settlementPG18OrderStateOf(t, ctx, pool, tc.orderID, "")
			if state.Status != tc.wantStatus || state.PaidAmount != 0 ||
				state.ReservationState != "released" || state.ReservationEvents != 2 ||
				state.Payments != 1 || state.OrderTransactions != 0 ||
				state.Subscriptions != 0 || state.Audits != 0 {
				t.Fatalf("%s terminal quarantine state mismatch: %+v", tc.name, state)
			}
		})
	}
	t.Log("marker=settlement_pg18_terminal_quarantine_exactly_once_ok")

	t.Run("corrupt reservation graphs fail closed", func(t *testing.T) {
		cases := []struct {
			name, marker, orderID, eventID, paymentID string
			wantStock                                 int
		}{
			{"missing stock child", "missing_child", settlementPG18CorruptMissing,
				"settle-corrupt-missing-event", "settle-corrupt-missing-payment", 0},
			{"line amount mismatch", "amount_mismatch", settlementPG18CorruptAmount,
				"settle-corrupt-amount-event", "settle-corrupt-amount-payment", 1},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				before := settlementPG18OrderFingerprint(t, ctx, pool, tc.orderID)
				resourcesBefore := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanCorrupt)
				_, err := service.HandlePaymentWebhook(ctx, settlementPG18Tenant, PaymentWebhookInput{
					ProviderCode: "settlement-demo", ProviderEventID: tc.eventID,
					ProviderPaymentID: tc.paymentID, EventType: "payment.succeeded",
					OrderID: tc.orderID, Amount: 1000, Currency: "CNY",
					RawPayload: map[string]any{"case": tc.marker}, SignatureVerified: true,
				})
				var he *httpx.Error
				if err == nil || !errors.As(err, &he) || he.Code != httpx.CodeInternal {
					t.Fatalf("corrupt %s error=%v want internal_error", tc.name, err)
				}
				if got := settlementPG18OrderFingerprint(t, ctx, pool, tc.orderID); got != before {
					t.Fatalf("corrupt %s left order residue before=%s after=%s", tc.name, before, got)
				}
				if got := settlementPG18ResourceFingerprint(t, ctx, pool, settlementPG18PlanCorrupt); got != resourcesBefore {
					t.Fatalf("corrupt %s left resource residue before=%s after=%s", tc.name, resourcesBefore, got)
				}
				if got := settlementPG18ProviderEventCount(t, ctx, pool, tc.paymentID); got != 0 {
					t.Fatalf("corrupt %s retained provider identity count=%d", tc.name, got)
				}
				state := settlementPG18OrderStateOf(t, ctx, pool, tc.orderID, "")
				if state.Status != "pending_payment" || state.ReservationState != "held" ||
					state.ReservationEvents != 1 || state.Items != 1 ||
					state.StockChildren != tc.wantStock || state.Payments != 0 ||
					state.OrderTransactions != 0 || state.Subscriptions != 0 || state.Audits != 0 {
					t.Fatalf("corrupt %s rollback graph mismatch: %+v", tc.name, state)
				}
				t.Logf("marker=settlement_pg18_corrupt_%s_fail_closed_ok", tc.marker)
			})
		}
		t.Log("marker=settlement_pg18_corrupt_graph_fail_closed_ok")
	})
}

func settlementPG18Claim(id, scope, key, hashSeed string, lease time.Time) middleware.IdempotencyClaim {
	return settlementPG18ClaimFor(settlementPG18User, id, scope, key, hashSeed, lease)
}

func settlementPG18ClaimFor(userID, id, scope, key, hashSeed string, lease time.Time) middleware.IdempotencyClaim {
	actorHash := sha256.Sum256([]byte(userID))
	return middleware.IdempotencyClaim{
		ID: id, TenantID: settlementPG18Tenant, ActorID: userID,
		Scope: scope, StorageScope: fmt.Sprintf("%s:actor:%x", scope, actorHash[:12]),
		Key: key, RequestHash: sha256.Sum256([]byte(hashSeed)),
		Generation: 1, LockedUntil: lease,
	}
}

// settlementPG18InsertClaim 预置一条 claim，并返回带真实 id 的那一份。
//
// 不写 id 这一列：产品代码（middleware/idempotency.go）也不写，它靠
// DEFAULT uuidv7() 生成，所以列级授权只给了它实际需要的那几列。测试原先
// 显式插 id，撞在这道最小权限上——正确的方向是让测试跟上权限，而不是为了
// 让测试跑通去放宽生产的授权面。
//
// id 由数据库回填后必须交还给调用方：claim 会被传进 CreateRenewal，服务层
// 要靠它关联这条记录。
func settlementPG18InsertClaim(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	claim middleware.IdempotencyClaim) middleware.IdempotencyClaim {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO idempotency_keys
				(tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
			VALUES ($1,$2,$3,$4,$5::uuid,$6)
			RETURNING id::text`,
			claim.TenantID, claim.StorageScope, claim.Key,
			claim.RequestHash[:], claim.ActorID, claim.LockedUntil).Scan(&claim.ID); err != nil {
			t.Fatalf("insert settlement idempotency claim: %v", err)
		}
	})
	return claim
}

type settlementPG18WebhookResult struct {
	index int
	out   *PaymentWebhookOutput
	err   error
}

func settlementPG18RunConcurrentWebhooks(t *testing.T, ctx context.Context, service *Service,
	inputs []PaymentWebhookInput) []*PaymentWebhookOutput {
	t.Helper()
	start := make(chan struct{})
	results := make(chan settlementPG18WebhookResult, len(inputs))
	var ready sync.WaitGroup
	ready.Add(len(inputs))
	for i := range inputs {
		index := i
		in := inputs[i]
		go func() {
			ready.Done()
			<-start
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out, err := service.HandlePaymentWebhook(callCtx, settlementPG18Tenant, in)
			results <- settlementPG18WebhookResult{index: index, out: out, err: err}
		}()
	}
	ready.Wait()
	close(start)

	outputs := make([]*PaymentWebhookOutput, len(inputs))
	for range inputs {
		result := <-results
		if result.err != nil {
			if settlementPG18IsDeadlock(result.err) {
				t.Fatalf("concurrent settlement returned SQLSTATE 40P01: %v", result.err)
			}
			t.Fatalf("concurrent settlement failed: %v", result.err)
		}
		if result.out == nil {
			t.Fatal("concurrent settlement returned nil output without error")
		}
		outputs[result.index] = result.out
	}
	return outputs
}

func settlementPG18RunWebhookRace(t *testing.T, ctx context.Context, service *Service, count int,
	input func(int) PaymentWebhookInput) *PaymentWebhookOutput {
	t.Helper()
	inputs := make([]PaymentWebhookInput, count)
	for i := range inputs {
		inputs[i] = input(i)
	}
	outputs := settlementPG18RunConcurrentWebhooks(t, ctx, service, inputs)
	processed, handled := 0, 0
	var winner *PaymentWebhookOutput
	for _, out := range outputs {
		switch {
		case out.Processed && !out.AlreadyHandled:
			processed++
			winner = out
		case out.AlreadyHandled && !out.Processed:
			handled++
		default:
			t.Fatalf("concurrent settlement returned invalid disposition: %#v", out)
		}
	}
	if processed != 1 || handled != count-1 {
		t.Fatalf("concurrent settlement dispositions processed/already=%d/%d want=1/%d",
			processed, handled, count-1)
	}
	return winner
}

func settlementPG18IsDeadlock(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "40P01"
}

func settlementPG18Time(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func settlementPG18InTx(t *testing.T, ctx context.Context, pool *platformdb.Pool, fn func(pgx.Tx)) {
	t.Helper()
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: settlementPG18Tenant, ActorID: settlementPG18User}, func(tx pgx.Tx) error {
		fn(tx)
		return nil
	}); err != nil {
		t.Fatalf("settlement PG18 query transaction: %v", err)
	}
}

func settlementPG18AssertRuntimeACL(t *testing.T, ctx context.Context, pool *platformdb.Pool) {
	t.Helper()
	var sessionUser, currentUser, rowSecurity string
	var canLogin, superuser, inherit, bypass bool
	if err := pool.QueryRow(ctx, `
		SELECT session_user,current_user,current_setting('row_security'),
		       rolcanlogin,rolsuper,rolinherit,rolbypassrls
		  FROM pg_roles WHERE rolname=current_user`).Scan(
		&sessionUser, &currentUser, &rowSecurity, &canLogin, &superuser, &inherit, &bypass,
	); err != nil {
		t.Fatal(err)
	}
	if sessionUser != "aegis_app" || currentUser != "aegis_app" || rowSecurity != "on" ||
		!canLogin || superuser || inherit || bypass {
		t.Fatalf("unsafe runtime role session=%s current=%s row_security=%s login=%v super=%v inherit=%v bypass=%v",
			sessionUser, currentUser, rowSecurity, canLogin, superuser, inherit, bypass)
	}
	var memberships int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_auth_members m
		JOIN pg_roles member_role ON member_role.oid=m.member
		JOIN pg_roles target_role ON target_role.oid=m.roleid
		WHERE member_role.rolname='aegis_app' OR target_role.rolname='aegis_app'`).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 {
		t.Fatalf("aegis_app role memberships=%d want=0", memberships)
	}
	if _, err := pool.Exec(ctx, `SET ROLE postgres`); !platformdb.IsInsufficientPrivilege(err) {
		t.Fatalf("SET ROLE postgres error=%v want SQLSTATE 42501", err)
	}

	tables := []string{
		"orders", "order_items", "payment_providers", "payment_intents", "payments",
		"payment_events", "order_reservations", "order_stock_reservations",
		"order_purchase_limit_reservations", "coupon_redemptions", "balance_holds",
		"late_payment_cases", "order_reservation_events", "ledger_accounts",
		"ledger_transactions", "ledger_entries", "subscriptions", "audit_events",
		"commission_entries", "idempotency_keys",
	}
	var protected int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relname=ANY($1::text[])
		  AND c.relrowsecurity AND c.relforcerowsecurity`, tables).Scan(&protected); err != nil {
		t.Fatal(err)
	}
	if protected != len(tables) {
		t.Fatalf("forced-RLS tables=%d want=%d", protected, len(tables))
	}

	// 权限模型已从列级白名单改回表级，这里改验现在真正要保证的东西。
	//
	// 原先这段断言的是「哪几列能写、哪几列不能」——payments.amount 不可改、
	// ledger_accounts.balance_signed 不可改、commission_entries 的身份列不可改。
	// 那套列级最小权限防的是「应用被攻破后的越权写」，设计没错，但维护成本
	// 在当前阶段压过了收益：同一份清单散在迁移与权限脚本两处，每加一列都要
	// 回头改两处，而漏改只在收窄过的库上才显形（开发机是超级用户，永远看不
	// 见）。orders 就这么漏过两列，让全新部署的库根本下不了单。详见
	// deploy/configure-app-role.sql 末尾那段说明。
	//
	// 放弃列级之后，仍然必须成立的是下面这些。它们防的是「已经写进去的钱账
	// 被改写或抹掉」，跟攻击面大小无关，属于账本自身的完整性。
	var writableEvidence []string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(array_agg(t ORDER BY t), ARRAY[]::text[])
		  FROM unnest(ARRAY['ledger_entries','ledger_transactions',
		                    'gift_card_redemptions','traffic_reset_logs']) AS t
		 WHERE has_table_privilege('aegis_app','public.'||t,'UPDATE')
		    OR has_table_privilege('aegis_app','public.'||t,'DELETE')`).
		Scan(&writableEvidence); err != nil {
		t.Fatal(err)
	}
	if len(writableEvidence) > 0 {
		t.Fatalf("这些表是只追加的证据流水，不该允许改写或删除: %v", writableEvidence)
	}

	// 幂等资源绑定那两个函数只给运行时角色，不能对 PUBLIC 开放——它们能直接
	// 改写幂等证据，是绕过整套重放保护的捷径。
	var binderExec, completerExec, publicBinder, publicCompleter bool
	if err := pool.QueryRow(ctx, `
		SELECT has_function_privilege('aegis_app','app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)','EXECUTE'),
		       has_function_privilege('aegis_app','app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)','EXECUTE'),
		       has_function_privilege('public','app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)','EXECUTE'),
		       has_function_privilege('public','app.complete_bound_idempotency_success(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid,integer,bytea,text,text,text,text,text)','EXECUTE')`).
		Scan(&binderExec, &completerExec, &publicBinder, &publicCompleter); err != nil {
		t.Fatal(err)
	}
	if !binderExec || !completerExec || publicBinder || publicCompleter {
		t.Fatalf("幂等绑定函数的授权不对 runtime=%v/%v public=%v/%v",
			binderExec, completerExec, publicBinder, publicCompleter)
	}

	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var shadow int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM payment_providers
			WHERE id='93000000-0000-7000-8000-000000000082'::uuid`).Scan(&shadow); err != nil {
			t.Fatal(err)
		}
		if shadow != 0 {
			t.Fatalf("cross-tenant provider visible through RLS count=%d", shadow)
		}
	})
	if err := pool.InTx(ctx, platformdb.Scope{
		TenantID: "93000000-0000-7000-8000-000000000002",
	}, func(tx pgx.Tx) error {
		var visible int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commission_entries
			WHERE id='93000000-0000-7000-8000-000000000491'::uuid`).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			return fmt.Errorf("cross-tenant commission visible through RLS count=%d", visible)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// payment_events.raw_payload 的写保护，随列级权限一起放弃了。
	//
	// 这里原本断言运行时角色改不动这一列——渠道原始回调是取证材料，改了就
	// 没法复盘。放弃列级白名单之后这项保护不复存在：payment_events 上只剩
	// 一个 no_delete 触发器，拦删不拦改。
	//
	// 明写在这里而不是默默删掉断言：这是那次取舍的实际代价，将来若要把它
	// 找回来，正确的位置是给 payment_events 加一个 BEFORE UPDATE 触发器，
	// 而不是重新收窄整套列级权限。
	var rawPayloadWritable bool
	if err := pool.QueryRow(ctx,
		`SELECT has_column_privilege('aegis_app','public.payment_events','raw_payload','UPDATE')`).
		Scan(&rawPayloadWritable); err != nil {
		t.Fatal(err)
	}
	if !rawPayloadWritable {
		t.Log("marker=settlement_pg18_raw_payload_still_protected（列级保护似乎回来了，可以收紧这条断言）")
	}

	// 佣金的身份与金额仍然改不动、删不掉——只是拦它的从权限层换成了
	// 00040 的守卫触发器，所以错误码从 42501 变成 check_violation。
	// 保护本身没有减弱，减弱的只是「谁来拦」。
	for name, statement := range map[string]string{
		"commission identity UPDATE": `UPDATE commission_entries SET referrer_user_id=referrer_user_id WHERE id IN (SELECT id FROM commission_entries LIMIT 1)`,
		"commission amount UPDATE":   `UPDATE commission_entries SET commission_amount=commission_amount+1 WHERE id IN (SELECT id FROM commission_entries LIMIT 1)`,
		"commission DELETE":          `DELETE FROM commission_entries WHERE id IN (SELECT id FROM commission_entries LIMIT 1)`,
	} {
		err := pool.InTx(ctx, platformdb.Scope{TenantID: settlementPG18Tenant, ActorID: settlementPG18User}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, statement)
			return err
		})
		if err == nil {
			t.Fatalf("%s 竟然成功了：佣金的身份、金额与存在性都不该被运行时角色改动", name)
		}
		if !platformdb.IsInsufficientPrivilege(err) && !platformdb.IsCheckViolation(err) {
			t.Fatalf("%s 被拒绝的方式不对 error=%v（期望权限或守卫拦截）", name, err)
		}
	}
	// 佣金守卫必须留在 RLS 之下。
	//
	// 它是 BEFORE INSERT 触发器，而 BEFORE 触发器必然先于 RLS 的 WITH CHECK
	// 执行——跨租户插入先撞上的是它，不是 RLS。这本身没问题，前提是它自己也
	// 看不见外租户的数据：SECURITY INVOKER 的守卫查 referrals 时同样受 RLS
	// 约束，于是无论那条关系是否真实存在，都报同一句 missing。
	//
	// 一旦有人给它加上 SECURITY DEFINER，它就会绕过租户隔离读到外租户的
	// referral，错误信息随即开始区分「关系不存在」和「关系存在但字段不符」——
	// 那是一个可以用来枚举其他租户推荐关系的侧信道。
	var commissionGuardIsDefiner bool
	if err := pool.QueryRow(ctx, `
		SELECT p.prosecdef FROM pg_proc p
		  JOIN pg_namespace n ON n.oid=p.pronamespace
		 WHERE n.nspname='app' AND p.proname='guard_commission_entry'`).Scan(&commissionGuardIsDefiner); err != nil {
		t.Fatalf("查 app.guard_commission_entry 的安全属性: %v", err)
	}
	if commissionGuardIsDefiner {
		t.Fatal("app.guard_commission_entry 是 SECURITY DEFINER：它会绕过 RLS 读到外租户的 referral，错误信息将泄露存在性")
	}

	// 跨租户插入必须被拒。42501 是 RLS 拒的，23514 是守卫拒的，两者都算——
	// 上面那条断言已经保证了后者不泄露信息。
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: settlementPG18Tenant, ActorID: settlementPG18User}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO commission_entries
			(tenant_id,referrer_user_id,referee_user_id,order_id,currency,
			 base_amount,rate_bp,commission_amount,frozen_until,review_required)
			VALUES ('93000000-0000-7000-8000-000000000002'::uuid,$1::uuid,$2::uuid,
			        $3::uuid,'CNY',500,1000,50,now()+interval '1 day',false)`,
			settlementPG18Referrer, settlementPG18CommissionUser, settlementPG18ExpiredOrder)
		return err
	}); !platformdb.IsInsufficientPrivilege(err) && !platformdb.IsCheckViolation(err) {
		t.Fatalf("cross-tenant commission INSERT error=%v want SQLSTATE 42501 或 23514", err)
	}
}

type settlementPG18OrderState struct {
	Status, ReservationState                                  string
	PaidAmount                                                int64
	HasSubscription                                           bool
	ReservationEvents, Items, StockChildren, PurchaseChildren int
	Coupons, Holds, Payments, OrderTransactions               int
	Subscriptions, Audits, Commissions                        int
	PlanReserved, PlanSold                                    int
}

func settlementPG18OrderStateOf(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	orderID, planID string) settlementPG18OrderState {
	t.Helper()
	var out settlementPG18OrderState
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			SELECT o.status,o.paid_amount,o.subscription_id IS NOT NULL,r.state,
			 (SELECT count(*) FROM order_reservation_events e WHERE e.order_id=o.id),
			 (SELECT count(*) FROM order_items i WHERE i.order_id=o.id),
			 (SELECT count(*) FROM order_stock_reservations s WHERE s.order_id=o.id),
			 (SELECT count(*) FROM order_purchase_limit_reservations p WHERE p.order_id=o.id),
			 (SELECT count(*) FROM coupon_redemptions c WHERE c.order_id=o.id),
			 (SELECT count(*) FROM balance_holds h WHERE h.order_id=o.id),
			 (SELECT count(*) FROM payments p WHERE p.order_id=o.id),
			 (SELECT count(*) FROM ledger_transactions lt WHERE lt.source_type='order' AND lt.source_id=o.id),
			 (SELECT count(*) FROM subscriptions s WHERE s.id=o.subscription_id),
			 (SELECT count(*) FROM audit_events a WHERE a.resource_type='order' AND a.resource_id=o.id AND a.action='payment.succeeded'),
			 (SELECT count(*) FROM commission_entries c WHERE c.order_id=o.id),
			 coalesce((SELECT stock_reserved FROM plans WHERE id=nullif($2,'')::uuid),0),
			 coalesce((SELECT stock_sold FROM plans WHERE id=nullif($2,'')::uuid),0)
			FROM orders o JOIN order_reservations r ON r.order_id=o.id
			WHERE o.id=$1::uuid`, orderID, planID).Scan(
			&out.Status, &out.PaidAmount, &out.HasSubscription, &out.ReservationState,
			&out.ReservationEvents, &out.Items, &out.StockChildren, &out.PurchaseChildren,
			&out.Coupons, &out.Holds, &out.Payments, &out.OrderTransactions,
			&out.Subscriptions, &out.Audits, &out.Commissions, &out.PlanReserved, &out.PlanSold,
		); err != nil {
			t.Fatalf("query settlement order state: %v", err)
		}
	})
	return out
}

func settlementPG18KeyFingerprint(t *testing.T, ctx context.Context, pool *platformdb.Pool, keyID string) string {
	return settlementPG18KeyFingerprintFor(t, ctx, pool, settlementPG18User, keyID)
}

func settlementPG18KeyFingerprintFor(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, actorID, keyID string) string {
	t.Helper()
	var digest string
	if err := pool.InTx(ctx, platformdb.Scope{
		TenantID: settlementPG18Tenant, ActorID: actorID,
	}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT encode(digest(row_to_json(k)::text,'sha256'),'hex')
			FROM idempotency_keys k WHERE k.id=$1::uuid`, keyID).Scan(&digest); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("fingerprint idempotency key %s: %v", keyID, err)
	}
	return digest
}

func settlementPG18SubscriptionFingerprint(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	subscriptionID string) string {
	t.Helper()
	var digest string
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT encode(digest(jsonb_build_object(
			'subscription',(SELECT to_jsonb(s) FROM subscriptions s WHERE s.id=$1::uuid),
			'events',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY e.id),'[]')
			           FROM subscription_events e WHERE e.subscription_id=$1::uuid)
		)::text,'sha256'),'hex')`, subscriptionID).Scan(&digest); err != nil {
			t.Fatalf("fingerprint subscription %s: %v", subscriptionID, err)
		}
	})
	return digest
}

func settlementPG18OrderFingerprint(t *testing.T, ctx context.Context, pool *platformdb.Pool, orderID string) string {
	t.Helper()
	var digest string
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			SELECT encode(digest(jsonb_build_object(
			 'order',(SELECT to_jsonb(o) FROM orders o WHERE o.id=$1::uuid),
			 'reservations',(SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]') FROM order_reservations r WHERE r.order_id=$1::uuid),
			 'reservation_events',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY e.id),'[]') FROM order_reservation_events e WHERE e.order_id=$1::uuid),
			 'items',(SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY i.id),'[]') FROM order_items i WHERE i.order_id=$1::uuid),
			 'stock',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]') FROM order_stock_reservations s WHERE s.order_id=$1::uuid),
			 'purchase',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id),'[]') FROM order_purchase_limit_reservations p WHERE p.order_id=$1::uuid),
			 'coupons',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]') FROM coupon_redemptions c WHERE c.order_id=$1::uuid),
			 'holds',(SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY h.id),'[]') FROM balance_holds h WHERE h.order_id=$1::uuid),
			 'payments',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id),'[]') FROM payments p WHERE p.order_id=$1::uuid),
			 'transactions',(SELECT coalesce(jsonb_agg(to_jsonb(lt) ORDER BY lt.id),'[]') FROM ledger_transactions lt WHERE lt.source_type='order' AND lt.source_id=$1::uuid),
			 'entries',(SELECT coalesce(jsonb_agg(to_jsonb(le) ORDER BY le.id),'[]') FROM ledger_entries le JOIN ledger_transactions lt ON lt.id=le.transaction_id WHERE lt.source_type='order' AND lt.source_id=$1::uuid),
			 'subscriptions',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]') FROM subscriptions s JOIN orders o ON o.subscription_id=s.id WHERE o.id=$1::uuid),
			 'subscription_events',(SELECT coalesce(jsonb_agg(to_jsonb(se) ORDER BY se.id),'[]') FROM subscription_events se WHERE se.order_id=$1::uuid),
			 'audits',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]') FROM audit_events a WHERE a.resource_type='order' AND a.resource_id=$1::uuid),
			 'commissions',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]') FROM commission_entries c WHERE c.order_id=$1::uuid)
			)::text,'sha256'),'hex')`, orderID).Scan(&digest); err != nil {
			t.Fatalf("fingerprint settlement order %s: %v", orderID, err)
		}
	})
	return digest
}

// settlementPG18ReleasedGraphFingerprint excludes the payment, quarantine
// ledger and quarantine audit evidence that a late callback is expected to
// add. Every order/reservation/resource lifecycle row must remain byte-stable
// across the first quarantine and both replay forms.
func settlementPG18ReleasedGraphFingerprint(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, orderID string) string {
	t.Helper()
	var digest string
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			SELECT encode(digest(jsonb_build_object(
			 'order',(SELECT to_jsonb(o) FROM orders o WHERE o.id=$1::uuid),
			 'reservations',(SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]') FROM order_reservations r WHERE r.order_id=$1::uuid),
			 'reservation_events',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY e.id),'[]') FROM order_reservation_events e WHERE e.order_id=$1::uuid),
			 'items',(SELECT coalesce(jsonb_agg(to_jsonb(i) ORDER BY i.id),'[]') FROM order_items i WHERE i.order_id=$1::uuid),
			 'stock',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]') FROM order_stock_reservations s WHERE s.order_id=$1::uuid),
			 'purchase',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id),'[]') FROM order_purchase_limit_reservations p WHERE p.order_id=$1::uuid),
			 'coupons',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]') FROM coupon_redemptions c WHERE c.order_id=$1::uuid),
			 'holds',(SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY h.id),'[]') FROM balance_holds h WHERE h.order_id=$1::uuid),
			 'payment_intents',(SELECT coalesce(jsonb_agg(to_jsonb(pi) ORDER BY pi.id),'[]') FROM payment_intents pi WHERE pi.order_id=$1::uuid),
			 'subscriptions',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]') FROM subscriptions s JOIN orders o ON o.subscription_id=s.id WHERE o.id=$1::uuid),
			 'plans',(SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id),'[]') FROM plans p WHERE p.id IN (SELECT i.plan_id FROM order_items i WHERE i.order_id=$1::uuid)),
			 'purchase_counters',(SELECT coalesce(jsonb_agg(to_jsonb(pc) ORDER BY pc.plan_id,pc.user_id),'[]') FROM plan_purchase_counters pc WHERE pc.plan_id IN (SELECT i.plan_id FROM order_items i WHERE i.order_id=$1::uuid)),
			 'coupon_counters',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]') FROM coupons c WHERE c.id IN (SELECT cr.coupon_id FROM coupon_redemptions cr WHERE cr.order_id=$1::uuid))
			)::text,'sha256'),'hex')`, orderID).Scan(&digest); err != nil {
			t.Fatalf("fingerprint released settlement graph %s: %v", orderID, err)
		}
	})
	return digest
}

func settlementPG18ResourceFingerprint(t *testing.T, ctx context.Context, pool *platformdb.Pool, planID string) string {
	t.Helper()
	var digest string
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			SELECT encode(digest(jsonb_build_object(
			 'accounts',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]')
			               FROM ledger_accounts a WHERE a.tenant_id=$1::uuid),
			 'plan',(SELECT to_jsonb(p) FROM plans p WHERE p.id=nullif($2,'')::uuid),
			 'purchase',(SELECT coalesce(jsonb_agg(to_jsonb(pc) ORDER BY pc.plan_id,pc.user_id),'[]')
			                 FROM plan_purchase_counters pc
			                WHERE pc.tenant_id=$1::uuid AND pc.user_id=$3::uuid),
			 'coupons',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY c.id),'[]')
			               FROM coupons c WHERE c.tenant_id=$1::uuid AND c.id='93000000-0000-7000-8000-000000000062'::uuid)
			)::text,'sha256'),'hex')`, settlementPG18Tenant, planID, settlementPG18User).Scan(&digest); err != nil {
			t.Fatalf("fingerprint settlement resources: %v", err)
		}
	})
	return digest
}

func settlementPG18ProviderEventCount(t *testing.T, ctx context.Context, pool *platformdb.Pool, paymentID string) int {
	t.Helper()
	var count int
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM payment_events
			WHERE provider_payment_id=$1`, paymentID).Scan(&count); err != nil {
			t.Fatal(err)
		}
	})
	return count
}

func settlementPG18ProviderEventStatuses(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	paymentID string) (processed, ignored int) {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE processing_status='processed'),
			count(*) FILTER (WHERE processing_status='ignored')
			FROM payment_events WHERE provider_payment_id=$1`, paymentID).Scan(
			&processed, &ignored); err != nil {
			t.Fatal(err)
		}
	})
	return processed, ignored
}

func settlementPG18AssertPaymentEvent(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	eventID, wantStatus string) {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var status string
		var signature bool
		var processed bool
		if err := tx.QueryRow(ctx, `SELECT processing_status,signature_verified,processed_at IS NOT NULL
			FROM payment_events WHERE provider_event_id=$1`, eventID).Scan(&status, &signature, &processed); err != nil {
			t.Fatal(err)
		}
		if status != wantStatus || !signature || !processed {
			t.Fatalf("payment event %s status/signature/processed=%s/%v/%v want=%s/true/true",
				eventID, status, signature, processed, wantStatus)
		}
	})
}

func settlementPG18AssertCaptureEvent(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	orderID, businessRequestID string) {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM order_reservation_events
			WHERE order_id=$1::uuid AND from_state='held' AND to_state='captured'
			  AND event_kind='capture' AND business_request_id=$2::uuid`,
			orderID, businessRequestID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("capture event count=%d want=1", count)
		}
	})
}

func settlementPG18AssertExternalLedger(t *testing.T, ctx context.Context, pool *platformdb.Pool, txnID string) {
	t.Helper()
	settlementPG18AssertLedger(t, ctx, pool, txnID, "order_paid", 2,
		map[string]int64{"channel_cash/debit": 1000, "platform_revenue/credit": 1000})
}

func settlementPG18AssertCommission(t *testing.T, ctx context.Context, pool *platformdb.Pool, orderID string) {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var referrer, referee, currency, status, accrualTxnID string
		var baseAmount, rateBP, amount int64
		var paymentLinked, frozen bool
		if err := tx.QueryRow(ctx, `SELECT referrer_user_id::text,referee_user_id::text,
			currency::text,base_amount,rate_bp,commission_amount,status,
			payment_id IS NOT NULL,frozen_until IS NOT NULL,accrual_txn_id::text
			FROM commission_entries WHERE order_id=$1::uuid`, orderID).Scan(
			&referrer, &referee, &currency, &baseAmount, &rateBP, &amount, &status,
			&paymentLinked, &frozen, &accrualTxnID); err != nil {
			t.Fatal(err)
		}
		if referrer != settlementPG18Referrer || referee != settlementPG18CommissionUser ||
			currency != "CNY" || baseAmount != 1000 || rateBP != 1000 || amount != 100 ||
			status != "pending" || paymentLinked || !frozen || accrualTxnID == "" {
			t.Fatalf("commission evidence mismatch referrer/referee=%s/%s currency=%s base/rate/amount=%d/%d/%d status=%s payment-linked/frozen=%v/%v accrual=%s",
				referrer, referee, currency, baseAmount, rateBP, amount, status,
				paymentLinked, frozen, accrualTxnID)
		}
		var sourceMatches int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions
			WHERE id=$1::uuid AND kind='commission_accrued' AND source_type='order'
			  AND source_id=$2::uuid AND currency='CNY'`, accrualTxnID, orderID).Scan(&sourceMatches); err != nil {
			t.Fatal(err)
		}
		if sourceMatches != 1 {
			t.Fatalf("commission accrual transaction source count=%d want=1", sourceMatches)
		}
	})
	settlementPG18AssertLedger(t, ctx, pool, settlementPG18CommissionTxnID(t, ctx, pool, orderID),
		"commission_accrued", 2,
		map[string]int64{"platform_revenue/debit": 100, "user_commission_pending/credit": 100})
}

func settlementPG18CommissionTxnID(t *testing.T, ctx context.Context, pool *platformdb.Pool, orderID string) string {
	t.Helper()
	var txnID string
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT accrual_txn_id::text FROM commission_entries
			WHERE order_id=$1::uuid`, orderID).Scan(&txnID); err != nil {
			t.Fatal(err)
		}
	})
	return txnID
}

func settlementPG18AssertTopupLedger(t *testing.T, ctx context.Context, pool *platformdb.Pool, txnID string) {
	t.Helper()
	settlementPG18AssertLedger(t, ctx, pool, txnID, "balance_topup", 3,
		map[string]int64{"channel_cash/debit": 490, "platform_fee_expense/debit": 10, "user_balance/credit": 500})
}

func settlementPG18AssertMixedLedger(t *testing.T, ctx context.Context, pool *platformdb.Pool, txnID string) {
	t.Helper()
	settlementPG18AssertLedger(t, ctx, pool, txnID, "order_paid", 4,
		map[string]int64{"channel_cash/debit": 970, "platform_fee_expense/debit": 30,
			"user_balance_hold/debit": 700, "platform_revenue/credit": 1700})
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var wrong int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ledger_entries e
			JOIN ledger_accounts a ON a.id=e.account_id
			WHERE e.transaction_id=$1::uuid AND a.account_type='user_balance'
			  AND e.direction='debit' AND e.amount=700`, txnID).Scan(&wrong); err != nil {
			t.Fatal(err)
		}
		if wrong != 0 {
			t.Fatal("mixed settlement double-debited available balance instead of capturing its hold")
		}
	})
}

func settlementPG18AssertLedger(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	txnID, wantKind string, wantEntries int, wants map[string]int64) {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var kind string
		var entries int
		if err := tx.QueryRow(ctx, `SELECT kind,(SELECT count(*) FROM ledger_entries WHERE transaction_id=t.id)
			FROM ledger_transactions t WHERE id=$1::uuid`, txnID).Scan(&kind, &entries); err != nil {
			t.Fatal(err)
		}
		if kind != wantKind || entries != wantEntries {
			t.Fatalf("ledger transaction kind/entries=%s/%d want=%s/%d", kind, entries, wantKind, wantEntries)
		}
		for key, amount := range wants {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM ledger_entries e
				JOIN ledger_accounts a ON a.id=e.account_id
				WHERE e.transaction_id=$1::uuid AND a.account_type||'/'||e.direction=$2
				  AND e.amount=$3`, txnID, key, amount).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("ledger entry %s amount=%d count=%d want=1", key, amount, count)
			}
		}
	})
}

func settlementPG18AssertMixedGraph(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	orderID, ledgerTxnID string) {
	t.Helper()
	settlementPG18InTx(t, ctx, pool, func(tx pgx.Tx) {
		var orderStatus, reservationState, couponStatus, holdStatus string
		var paidAmount int64
		var captureMatches bool
		var stockReserved, stockSold, purchased, purchaseReserved int
		var redeemed, couponReserved int
		var available, held int64
		if err := tx.QueryRow(ctx, `
			SELECT o.status,o.paid_amount,r.state,cr.status,bh.status,
			       bh.capture_txn_id=$2::uuid,p.stock_reserved,p.stock_sold,
			       pc.purchased,pc.reserved,c.redeemed_count,c.reserved_count,
			       -aa.balance_signed,-ha.balance_signed
			FROM orders o
			JOIN order_reservations r ON r.order_id=o.id
			JOIN coupon_redemptions cr ON cr.order_id=o.id
			JOIN coupons c ON c.id=cr.coupon_id
			JOIN balance_holds bh ON bh.order_id=o.id
			JOIN ledger_accounts aa ON aa.id=bh.available_account_id
			JOIN ledger_accounts ha ON ha.id=bh.hold_account_id
			JOIN order_stock_reservations sr ON sr.order_id=o.id
			JOIN plans p ON p.id=sr.plan_id
			JOIN order_purchase_limit_reservations pr ON pr.order_id=o.id
			JOIN plan_purchase_counters pc ON pc.plan_id=pr.plan_id AND pc.user_id=pr.user_id
			WHERE o.id=$1::uuid`, orderID, ledgerTxnID).Scan(
			&orderStatus, &paidAmount, &reservationState, &couponStatus, &holdStatus,
			&captureMatches, &stockReserved, &stockSold, &purchased, &purchaseReserved,
			&redeemed, &couponReserved, &available, &held,
		); err != nil {
			t.Fatal(err)
		}
		if orderStatus != "fulfilled" || paidAmount != 1700 || reservationState != "captured" ||
			couponStatus != "captured" || holdStatus != "captured" || !captureMatches ||
			stockReserved != 0 || stockSold != 1 || purchased != 1 || purchaseReserved != 0 ||
			redeemed != 1 || couponReserved != 0 || available != 1300 || held != 0 {
			t.Fatalf("mixed graph order/paid/reservation/coupon/hold=%s/%d/%s/%s/%s capture=%v stock=%d/%d purchase=%d/%d coupon=%d/%d balances=%d/%d",
				orderStatus, paidAmount, reservationState, couponStatus, holdStatus,
				captureMatches, stockReserved, stockSold, purchased, purchaseReserved,
				redeemed, couponReserved, available, held)
		}
	})
}
