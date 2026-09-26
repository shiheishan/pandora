// [INPUT]: 依赖 order_release_pg18_fixture_test.go 的 orderReleasePG18Fixture 与事务工具
// [OUTPUT]: 包内提供释放、挂账与佣金的共用断言：orderReleasePG18AssertHeldResources / ReleasedResources / Released / OrderStatus / Quarantine / PaymentShape / CommissionCount / CommissionState
// [POS]: 本包 PG18 测试的共用断言库，TestOrderReleasePG18、TestSettlementPG18 与各复用夹具的 PG18 测试共用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

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
