// [INPUT]: 依赖 settlement_pg18_fixture_test.go 的常量与 settlementPG18InTx
// [OUTPUT]: 包内提供订单状态快照 settlementPG18OrderState / OrderStateOf、键 / 订阅 / 订单 / 释放图 / 资源指纹、支付事件计数与状态、捕获事件、外部 / 充值 / 混合账本与佣金断言
// [POS]: TestSettlementPG18 的断言库：用指纹证明重放与回滚没有改动任何业务行
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

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
