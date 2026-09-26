// [INPUT]: 依赖 settlement.go 的 HandlePaymentWebhook 结算主链、checkout.go 的 CreateOrder、topup.go 的 CreateTopup、renewal.go 的 CreateRenewal、release.go 的 CancelOrder，依赖 settlement_pg18_fixture_test.go 的常量、认领与并发回调工具、settlement_pg18_assert_test.go 的指纹与账本断言、order_release_pg18_assert_test.go 的 orderReleasePG18AssertQuarantine，依赖迁移 00040
// [OUTPUT]: 对外提供 TestSettlementPG18（run-pg18-gates.sh 的 billing 域）
// [POS]: 支付结算的 PG18 集成门禁：运行角色 ACL 与强制 RLS、外部全额结算与重复身份、混合扣款、充值只记父级、佣金共享锁与唯一冲突回滚、故障矩阵回滚后同身份恰好一次、金额币种错误整体回滚、旧续费与 actor 绑定续费、损坏预留图 fail closed。deploy/test-settlement-runner_static_test.sh 按文件名 grep 本文件，门禁用到的字面量要留在这里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
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
