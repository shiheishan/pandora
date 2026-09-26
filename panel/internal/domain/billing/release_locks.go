// [INPUT]: 依赖 release.go 的 releaseOrderRequest / releaseOrderShape / releaseIntentLock 与释放错误哨兵，依赖 reservations.go 的 lockOrderReservationGraph，依赖 platform/db
// [OUTPUT]: 包内提供 lockReleaseOrder、lockActiveReleaseIntents、lockAndRejectSettledPaymentEvidence、lockReleaseReservationGraph、lockRenewalReservationForRelease、lockReleaseCoupon、lockReleaseBalance
// [POS]: billing 订单释放的加锁步骤：从 release.go 拆出，被 releaseOrderReservation 按 订单 → 活跃支付意图 → 已结算收款证据 → 预留图 的顺序调用；预留图对续费单走 lockRenewalReservationForRelease（由它再锁券与余额冻结），其余 kind 共用 lockOrderReservationGraph；过期扫描以 SKIP LOCKED 取订单，任何已成功的收款证据都拒绝释放
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func lockReleaseOrder(ctx context.Context, tx pgx.Tx, tenantID string,
	req releaseOrderRequest) (releaseOrderShape, bool, error) {

	query := `
		SELECT id::text,user_id::text,status,kind,currency::text,
		       business_request_id::text,subtotal_amount,discount_amount,tax_amount,
		       total_amount,balance_applied,payable_amount,coupon_id::text,
		       state_version,cancelled_at,cancel_reason,proration_credit_amount
		  FROM orders
		 WHERE tenant_id=$1 AND id=$2::uuid`
	args := []any{tenantID, req.OrderID}
	if req.UserID != "" {
		query += ` AND user_id=$3::uuid`
		args = append(args, req.UserID)
	}
	query += ` FOR UPDATE`
	if req.SkipLocked {
		query += ` SKIP LOCKED`
	}

	var shape releaseOrderShape
	err := tx.QueryRow(ctx, query, args...).Scan(
		&shape.ID, &shape.UserID, &shape.Status, &shape.Kind, &shape.Currency,
		&shape.BusinessRequestID, &shape.SubtotalAmount, &shape.DiscountAmount,
		&shape.TaxAmount, &shape.TotalAmount, &shape.BalanceAmount,
		&shape.PayableAmount, &shape.CouponID, &shape.StateVersion,
		&shape.CancelledAt, &shape.CancelReason, &shape.ProrationCredit)
	if errors.Is(err, pgx.ErrNoRows) {
		if req.SkipLocked {
			return shape, true, nil
		}
		return shape, false, errOrderReleaseNotFound
	}
	return shape, false, err
}

func lockActiveReleaseIntents(ctx context.Context, tx pgx.Tx, tenantID,
	orderID string) ([]releaseIntentLock, error) {

	rows, err := tx.Query(ctx, `
		SELECT id::text,status FROM payment_intents
		 WHERE tenant_id=$1 AND order_id=$2::uuid
		   AND status IN ('created','requires_action','processing')
		 ORDER BY id FOR UPDATE`, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []releaseIntentLock
	for rows.Next() {
		var item releaseIntentLock
		if err := rows.Scan(&item.ID, &item.Status); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func lockAndRejectSettledPaymentEvidence(ctx context.Context, tx pgx.Tx,
	tenantID, orderID string) error {

	succeededIntents, err := tx.Query(ctx, `
		SELECT id::text FROM payment_intents
		 WHERE tenant_id=$1 AND order_id=$2::uuid AND status='succeeded'
		 ORDER BY id FOR UPDATE`, tenantID, orderID)
	if err != nil {
		return err
	}
	hasSucceededIntent := false
	for succeededIntents.Next() {
		var intentID string
		if err := succeededIntents.Scan(&intentID); err != nil {
			succeededIntents.Close()
			return err
		}
		hasSucceededIntent = true
	}
	if err := succeededIntents.Err(); err != nil {
		succeededIntents.Close()
		return err
	}
	succeededIntents.Close()

	// 订阅已不收而隔离进挂账的收款（R117）不算入账：钱在挂账科目里另行处理，
	// 订单从未被捕获，照常释放才能退回余额冻结，否则冻结永远退不回来。
	payments, err := tx.Query(ctx, `
		SELECT id::text FROM payments
		 WHERE tenant_id=$1 AND order_id=$2::uuid
		   AND NOT EXISTS (
		         SELECT 1 FROM late_payment_cases c
		          WHERE c.tenant_id=payments.tenant_id AND c.payment_id=payments.id
		            AND c.case_kind='ineligible_subscription')
		 ORDER BY id`, tenantID, orderID)
	if err != nil {
		return err
	}
	hasPayment := false
	for payments.Next() {
		var paymentID string
		if err := payments.Scan(&paymentID); err != nil {
			payments.Close()
			return err
		}
		hasPayment = true
	}
	if err := payments.Err(); err != nil {
		payments.Close()
		return err
	}
	payments.Close()

	if hasSucceededIntent || hasPayment {
		return errOrderReleasePaymentEvidence
	}
	return nil
}

func lockReleaseReservationGraph(ctx context.Context, tx pgx.Tx, tenantID string,
	shape releaseOrderShape, requireDue bool) (*lockedReservation, error) {

	if orderTotal(shape.SubtotalAmount, shape.DiscountAmount, shape.ProrationCredit,
		shape.TaxAmount) != shape.TotalAmount ||
		shape.TotalAmount-shape.BalanceAmount != shape.PayableAmount {
		return nil, errors.New("order financial shape is inconsistent")
	}

	// 变更套餐单释放时不碰订阅（下单只冻结了券与余额），与新购走同一套预留图锁。
	if shape.Kind == "new" || shape.Kind == "topup" || shape.Kind == "addon" ||
		shape.Kind == "upgrade" {
		locked, err := lockOrderReservationGraph(ctx, tx, reservationLockRequest{
			TenantID: tenantID, OrderID: shape.ID, UserID: shape.UserID,
			Kind: shape.Kind, Currency: shape.Currency, CouponID: shape.CouponID,
			SubtotalAmount: shape.SubtotalAmount, DiscountAmount: shape.DiscountAmount,
			TaxAmount: shape.TaxAmount, TotalAmount: shape.TotalAmount,
			PayableAmount: shape.PayableAmount,
			BalanceAmount: shape.BalanceAmount, ProrationCredit: shape.ProrationCredit,
		})
		if err != nil {
			return nil, err
		}
		if requireDue {
			var due bool
			if err := tx.QueryRow(ctx, `
				SELECT expires_at<=now() FROM order_reservations
				 WHERE tenant_id=$1 AND id=$2::uuid AND state='held'`, tenantID, locked.ID).
				Scan(&due); err != nil {
				return nil, err
			}
			if !due {
				return nil, fmt.Errorf("%w: reservation is not due", errOrderReleaseConflict)
			}
		}
		return locked, nil
	}
	if shape.Kind == "renewal" {
		return lockRenewalReservationForRelease(ctx, tx, tenantID, shape, requireDue)
	}
	return nil, fmt.Errorf("unsupported order kind %q", shape.Kind)
}

func lockRenewalReservationForRelease(ctx context.Context, tx pgx.Tx,
	tenantID string, shape releaseOrderShape, requireDue bool) (*lockedReservation, error) {

	var out lockedReservation
	var parentUserID, parentState string
	var due bool
	if err := tx.QueryRow(ctx, `
		SELECT id::text,user_id::text,state,expires_at<=now()
		  FROM order_reservations
		 WHERE tenant_id=$1 AND order_id=$2::uuid FOR UPDATE`, tenantID, shape.ID).
		Scan(&out.ID, &parentUserID, &parentState, &due); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("renewal reservation graph is missing its parent")
		}
		return nil, err
	}
	if parentUserID != shape.UserID || parentState != "held" {
		return nil, errors.New("renewal reservation parent is not held for the order user")
	}
	if requireDue && !due {
		return nil, fmt.Errorf("%w: reservation is not due", errOrderReleaseConflict)
	}

	var itemCount, stockCount, purchaseCount int
	var itemCurrency string
	var quantity int
	var unitAmount, lineAmount int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*),coalesce(min(currency::text),''),coalesce(min(quantity),0),
		       coalesce(min(unit_amount),0),coalesce(min(line_amount),0)
		  FROM order_items WHERE tenant_id=$1 AND order_id=$2::uuid`, tenantID, shape.ID).
		Scan(&itemCount, &itemCurrency, &quantity, &unitAmount, &lineAmount); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM order_stock_reservations
		         WHERE tenant_id=$1 AND order_id=$2::uuid),
		       (SELECT count(*) FROM order_purchase_limit_reservations
		         WHERE tenant_id=$1 AND order_id=$2::uuid)`, tenantID, shape.ID).
		Scan(&stockCount, &purchaseCount); err != nil {
		return nil, err
	}
	if itemCount != 1 || itemCurrency != shape.Currency || quantity <= 0 ||
		lineAmount != unitAmount*int64(quantity) || lineAmount != shape.SubtotalAmount ||
		stockCount != 0 || purchaseCount != 0 {
		return nil, errors.New("renewal reservation item/resource shape is incomplete")
	}

	if err := lockReleaseCoupon(ctx, tx, tenantID, shape, &out); err != nil {
		return nil, err
	}
	if err := lockReleaseBalance(ctx, tx, tenantID, shape, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func lockReleaseCoupon(ctx context.Context, tx pgx.Tx, tenantID string,
	shape releaseOrderShape, out *lockedReservation) error {

	if shape.CouponID == nil {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM coupon_redemptions
			WHERE tenant_id=$1 AND order_id=$2::uuid`, tenantID, shape.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 || shape.DiscountAmount != 0 {
			return errors.New("renewal coupon shape exists without an order coupon")
		}
		return nil
	}

	var reserved int
	if err := tx.QueryRow(ctx, `SELECT reserved_count FROM coupons
		WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, *shape.CouponID).
		Scan(&reserved); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT coupon_id::text,reservation_id::text,user_id::text,status,
		       currency::text,discount_amount
		  FROM coupon_redemptions
		 WHERE tenant_id=$1 AND order_id=$2::uuid
		 ORDER BY id FOR UPDATE`, tenantID, shape.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int
	var couponID, reservationID, userID, status, currency string
	var discount int64
	for rows.Next() {
		count++
		if err := rows.Scan(&couponID, &reservationID, &userID, &status,
			&currency, &discount); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 || reserved <= 0 || couponID != *shape.CouponID || reservationID != out.ID ||
		userID != shape.UserID || status != "held" || currency != shape.Currency ||
		discount != shape.DiscountAmount {
		return errors.New("renewal coupon reservation shape is inconsistent")
	}
	out.Coupon = &couponReservationLock{CouponID: couponID}
	return nil
}

func lockReleaseBalance(ctx context.Context, tx pgx.Tx, tenantID string,
	shape releaseOrderShape, out *lockedReservation) error {

	rows, err := tx.Query(ctx, `
		SELECT h.amount,h.available_account_id::text,h.hold_account_id::text,
		       h.reservation_id::text,h.user_id::text,h.currency::text,h.status
		  FROM balance_holds h
		  JOIN ledger_accounts a ON a.tenant_id=h.tenant_id AND a.id=h.available_account_id
		   AND a.currency=h.currency AND a.account_type='user_balance'
		   AND a.owner_user_id=h.user_id
		  JOIN ledger_accounts ha ON ha.tenant_id=h.tenant_id AND ha.id=h.hold_account_id
		   AND ha.currency=h.currency AND ha.account_type='user_balance_hold'
		   AND ha.owner_user_id=h.user_id
		 WHERE h.tenant_id=$1 AND h.order_id=$2::uuid
		 ORDER BY h.id FOR UPDATE OF h`, tenantID, shape.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int
	var balance balanceReservationLock
	var reservationID, userID, currency, status string
	for rows.Next() {
		count++
		if err := rows.Scan(&balance.Amount, &balance.AvailableAccountID,
			&balance.HoldAccountID, &reservationID, &userID, &currency, &status); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if shape.BalanceAmount == 0 {
		if count != 0 {
			return errors.New("renewal balance hold exists without balance applied")
		}
		return nil
	}
	if count != 1 || balance.Amount != shape.BalanceAmount ||
		reservationID != out.ID || userID != shape.UserID || currency != shape.Currency ||
		status != "held" || balance.AvailableAccountID == "" || balance.HoldAccountID == "" {
		return errors.New("renewal balance hold shape is inconsistent")
	}
	out.Balance = &balance
	return nil
}
