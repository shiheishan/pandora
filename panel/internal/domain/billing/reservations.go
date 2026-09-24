// [INPUT]: 依赖 ledger.go 的 normalBalance 与科目类型，读写 order_reservations 及其子资源表
// [OUTPUT]: 对包内提供 reservationLockRequest、lockOrderReservationGraph、captureLockedReservation、prepareAndLockLedgerAccounts 与锁类型
// [POS]: billing 结算与释放共用的预留图加锁与校验：按 kind 校验订单项形状（addon 的唯一一行指向流量包），统一 UUID 排序加锁避免死锁
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type reservationLockRequest struct {
	TenantID       string
	OrderID        string
	UserID         string
	Kind           string
	Currency       string
	CouponID       *string
	SubtotalAmount int64
	DiscountAmount int64
	TaxAmount      int64
	TotalAmount    int64
	PayableAmount  int64
	BalanceAmount  int64
}

type stockReservationLock struct {
	PlanID   string
	Quantity int
}

type purchaseReservationLock struct {
	PlanID   string
	UserID   string
	Quantity int
}

type couponReservationLock struct {
	CouponID string
}

type balanceReservationLock struct {
	Amount             int64
	AvailableAccountID string
	HoldAccountID      string
}

type lockedReservation struct {
	ID       string
	Stock    *stockReservationLock
	Purchase *purchaseReservationLock
	Coupon   *couponReservationLock
	Balance  *balanceReservationLock
}

// lockOrderReservationGraph locks and validates the complete reservation
// shape. The order and active payment intent are deliberately locked by the
// caller first. Every query below follows the shared settlement lock order:
// parent, immutable child shape, plan/counter, coupon/redemption, balance hold.
func lockOrderReservationGraph(ctx context.Context, tx pgx.Tx,
	in reservationLockRequest) (*lockedReservation, error) {

	if in.Kind != "new" && in.Kind != "renewal" && in.Kind != "topup" && in.Kind != "addon" {
		return nil, fmt.Errorf("unsupported order kind %q", in.Kind)
	}

	var out lockedReservation
	var parentUserID, parentState string
	if err := tx.QueryRow(ctx, `
		SELECT id::text, user_id::text, state
		  FROM order_reservations
		 WHERE tenant_id=$1 AND order_id=$2::uuid
		 FOR UPDATE`, in.TenantID, in.OrderID).
		Scan(&out.ID, &parentUserID, &parentState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("order reservation graph is missing its parent")
		}
		return nil, err
	}
	if parentUserID != in.UserID || parentState != "held" {
		return nil, errors.New("order reservation parent is not a held reservation for the order user")
	}

	type itemShape struct {
		planID      string
		trafficPack bool
		currency    string
		quantity    int
		unitAmount  int64
		lineAmount  int64
	}
	var items []itemShape
	rows, err := tx.Query(ctx, `
		SELECT coalesce(plan_id::text, ''), traffic_pack_id IS NOT NULL,
		       currency::text, quantity, unit_amount, line_amount
		  FROM order_items
		 WHERE tenant_id=$1 AND order_id=$2::uuid
		 ORDER BY id`, in.TenantID, in.OrderID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var item itemShape
		if err := rows.Scan(&item.planID, &item.trafficPack, &item.currency, &item.quantity,
			&item.unitAmount, &item.lineAmount); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var stocks []stockReservationLock
	rows, err = tx.Query(ctx, `
		SELECT plan_id::text, quantity
		  FROM order_stock_reservations
		 WHERE tenant_id=$1 AND order_id=$2::uuid AND reservation_id=$3::uuid
		 ORDER BY plan_id`, in.TenantID, in.OrderID, out.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var stock stockReservationLock
		if err := rows.Scan(&stock.PlanID, &stock.Quantity); err != nil {
			rows.Close()
			return nil, err
		}
		stocks = append(stocks, stock)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var purchases []purchaseReservationLock
	rows, err = tx.Query(ctx, `
		SELECT plan_id::text, user_id::text, quantity
		  FROM order_purchase_limit_reservations
		 WHERE tenant_id=$1 AND order_id=$2::uuid AND reservation_id=$3::uuid
		 ORDER BY plan_id, user_id`, in.TenantID, in.OrderID, out.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var purchase purchaseReservationLock
		if err := rows.Scan(&purchase.PlanID, &purchase.UserID, &purchase.Quantity); err != nil {
			rows.Close()
			return nil, err
		}
		purchases = append(purchases, purchase)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if in.Kind == "topup" {
		if len(items) != 0 || len(stocks) != 0 || len(purchases) != 0 ||
			in.CouponID != nil || in.BalanceAmount != 0 || in.DiscountAmount != 0 ||
			in.SubtotalAmount+in.TaxAmount != in.TotalAmount ||
			in.TotalAmount != in.PayableAmount {
			return nil, errors.New("topup reservation graph must contain only its parent")
		}
	} else {
		if in.SubtotalAmount-in.DiscountAmount+in.TaxAmount != in.TotalAmount ||
			in.TotalAmount-in.BalanceAmount != in.PayableAmount ||
			len(items) != 1 || items[0].quantity <= 0 ||
			// 流量包订单的那一行指向流量包、不指向套餐；其余订单反之
			(in.Kind == "addon") != (items[0].planID == "" && items[0].trafficPack) ||
			(in.Kind != "addon" && (items[0].planID == "" || items[0].trafficPack)) ||
			items[0].currency != in.Currency ||
			items[0].lineAmount != items[0].unitAmount*int64(items[0].quantity) ||
			items[0].lineAmount != in.SubtotalAmount {
			return nil, errors.New("order item reservation shape is incomplete or inconsistent")
		}
		if in.Kind == "new" {
			if len(stocks) != 1 || stocks[0].PlanID != items[0].planID ||
				stocks[0].Quantity != items[0].quantity {
				return nil, errors.New("new-order stock reservation shape is incomplete or inconsistent")
			}
			out.Stock = &stocks[0]
		} else if len(stocks) != 0 || len(purchases) != 0 {
			return nil, errors.New("renewal and addon reservation graphs cannot contain stock or purchase-limit reservations")
		}
	}

	var purchaseLimit *int
	if in.Kind == "new" {
		if err := tx.QueryRow(ctx, `
			SELECT purchase_limit_per_user
			  FROM plans
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR UPDATE`, in.TenantID, items[0].planID).Scan(&purchaseLimit); err != nil {
			return nil, err
		}
		if purchaseLimit == nil {
			if len(purchases) != 0 {
				return nil, errors.New("unlimited plan has a purchase-limit reservation")
			}
		} else {
			if len(purchases) != 1 || purchases[0].PlanID != items[0].planID ||
				purchases[0].UserID != in.UserID || purchases[0].Quantity != items[0].quantity {
				return nil, errors.New("limited plan is missing its exact purchase-limit reservation")
			}
			var purchased, reserved int
			if err := tx.QueryRow(ctx, `
				SELECT purchased, reserved
				  FROM plan_purchase_counters
				 WHERE tenant_id=$1 AND plan_id=$2::uuid AND user_id=$3::uuid
				 FOR UPDATE`, in.TenantID, purchases[0].PlanID, purchases[0].UserID).
				Scan(&purchased, &reserved); err != nil {
				return nil, err
			}
			if reserved < purchases[0].Quantity {
				return nil, errors.New("purchase-limit reservation is not represented by its counter")
			}
			out.Purchase = &purchases[0]
		}
	}

	var couponRows int
	if in.CouponID != nil {
		var reservedCount int
		if err := tx.QueryRow(ctx, `
			SELECT reserved_count FROM coupons
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR UPDATE`, in.TenantID, *in.CouponID).Scan(&reservedCount); err != nil {
			return nil, err
		}
		if reservedCount <= 0 {
			return nil, errors.New("coupon reservation is not represented by its counter")
		}
		var couponID, reservationID, userID, status, currency string
		var discount int64
		rows, err = tx.Query(ctx, `
			SELECT coupon_id::text, reservation_id::text, user_id::text,
			       status, currency::text, discount_amount
			  FROM coupon_redemptions
			 WHERE tenant_id=$1 AND order_id=$2::uuid
			 ORDER BY id
			 FOR UPDATE`, in.TenantID, in.OrderID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			couponRows++
			if err := rows.Scan(&couponID, &reservationID, &userID, &status,
				&currency, &discount); err != nil {
				rows.Close()
				return nil, err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if couponRows != 1 || couponID != *in.CouponID || reservationID != out.ID ||
			userID != in.UserID || status != "held" || currency != in.Currency || discount < 0 {
			return nil, errors.New("coupon reservation shape is incomplete or inconsistent")
		}
		if discount != in.DiscountAmount {
			return nil, errors.New("coupon reservation discount does not match the order")
		}
		out.Coupon = &couponReservationLock{CouponID: couponID}
	} else {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM coupon_redemptions
			 WHERE tenant_id=$1 AND order_id=$2::uuid`, in.TenantID, in.OrderID).
			Scan(&couponRows); err != nil {
			return nil, err
		}
		if couponRows != 0 {
			return nil, errors.New("coupon redemption exists without an order coupon")
		}
		if in.DiscountAmount != 0 {
			return nil, errors.New("order discount exists without a coupon reservation")
		}
	}

	var holdRows int
	rows, err = tx.Query(ctx, `
		SELECT h.amount, h.available_account_id::text, h.hold_account_id::text,
		       h.reservation_id::text, h.user_id::text, h.currency::text, h.status
		  FROM balance_holds h
		  JOIN ledger_accounts a ON a.tenant_id=h.tenant_id
		   AND a.id=h.available_account_id AND a.currency=h.currency
		   AND a.account_type='user_balance' AND a.owner_user_id=h.user_id
		  JOIN ledger_accounts ha ON ha.tenant_id=h.tenant_id
		   AND ha.id=h.hold_account_id AND ha.currency=h.currency
		   AND ha.account_type='user_balance_hold' AND ha.owner_user_id=h.user_id
		 WHERE h.tenant_id=$1 AND h.order_id=$2::uuid
		 ORDER BY h.id
		 FOR UPDATE OF h`, in.TenantID, in.OrderID)
	if err != nil {
		return nil, err
	}
	var balance balanceReservationLock
	var reservationID, holdUserID, holdCurrency, holdStatus string
	for rows.Next() {
		holdRows++
		if err := rows.Scan(&balance.Amount, &balance.AvailableAccountID,
			&balance.HoldAccountID, &reservationID, &holdUserID, &holdCurrency,
			&holdStatus); err != nil {
			rows.Close()
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if in.BalanceAmount > 0 {
		if holdRows != 1 || balance.Amount != in.BalanceAmount ||
			balance.AvailableAccountID == "" || balance.HoldAccountID == "" ||
			reservationID != out.ID || holdUserID != in.UserID ||
			holdCurrency != in.Currency || holdStatus != "held" {
			return nil, errors.New("balance hold shape is incomplete or inconsistent")
		}
		out.Balance = &balance
	} else if holdRows != 0 {
		return nil, errors.New("balance hold exists for an order with no balance applied")
	}

	return &out, nil
}

type ledgerAccountSpec struct {
	Key         string
	AccountType AccountType
	Currency    string
	UserID      *string
	OwnerRef    string
}

// prepareAndLockLedgerAccounts avoids EnsureAccount's conflict-update lock
// acquisition order. Missing rows are inserted in a deterministic business-key
// order, IDs are resolved without row locks, then every participating account
// is locked one-by-one in UUID order before any ledger entry is written.
func prepareAndLockLedgerAccounts(ctx context.Context, tx pgx.Tx, tenantID string,
	specs []ledgerAccountSpec, existingIDs ...string) (map[string]string, error) {

	sort.Slice(specs, func(i, j int) bool {
		return accountSpecIdentity(specs[i]) < accountSpecIdentity(specs[j])
	})
	for _, spec := range specs {
		normal, ok := normalBalance[spec.AccountType]
		if !ok {
			return nil, fmt.Errorf("unknown ledger account type %q", spec.AccountType)
		}
		var err error
		if spec.UserID != nil {
			_, err = tx.Exec(ctx, `
				INSERT INTO ledger_accounts
					(tenant_id,account_type,normal_balance,currency,owner_user_id)
				VALUES ($1,$2,$3,$4,$5::uuid)
				ON CONFLICT (tenant_id,account_type,owner_user_id,currency)
					WHERE owner_user_id IS NOT NULL DO NOTHING`,
				tenantID, spec.AccountType, normal, spec.Currency, *spec.UserID)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO ledger_accounts
					(tenant_id,account_type,normal_balance,currency,owner_ref)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (tenant_id,account_type,coalesce(owner_ref,''),currency)
					WHERE owner_user_id IS NULL DO NOTHING`,
				tenantID, spec.AccountType, normal, spec.Currency, spec.OwnerRef)
		}
		if err != nil {
			return nil, err
		}
	}

	resolved := make(map[string]string, len(specs))
	allIDs := append([]string(nil), existingIDs...)
	for _, spec := range specs {
		var id string
		var err error
		if spec.UserID != nil {
			err = tx.QueryRow(ctx, `
				SELECT id::text FROM ledger_accounts
				 WHERE tenant_id=$1 AND account_type=$2 AND currency=$3
				   AND owner_user_id=$4::uuid`, tenantID, spec.AccountType,
				spec.Currency, *spec.UserID).Scan(&id)
		} else {
			err = tx.QueryRow(ctx, `
				SELECT id::text FROM ledger_accounts
				 WHERE tenant_id=$1 AND account_type=$2 AND currency=$3
				   AND owner_user_id IS NULL AND coalesce(owner_ref,'')=$4`,
				tenantID, spec.AccountType, spec.Currency, spec.OwnerRef).Scan(&id)
		}
		if err != nil {
			return nil, err
		}
		resolved[spec.Key] = id
		allIDs = append(allIDs, id)
	}

	sort.Strings(allIDs)
	for i, id := range allIDs {
		if id == "" || (i > 0 && id == allIDs[i-1]) {
			continue
		}
		var lockedID string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM ledger_accounts
			 WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, id).
			Scan(&lockedID); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func accountSpecIdentity(spec ledgerAccountSpec) string {
	owner := spec.OwnerRef
	if spec.UserID != nil {
		owner = *spec.UserID
	}
	return spec.Currency + "\x00" + string(spec.AccountType) + "\x00" + owner
}

func captureLockedReservation(ctx context.Context, tx pgx.Tx, tenantID, orderID,
	userID, businessRequestID string, locked *lockedReservation,
	captureTxnID string) error {

	if locked.Stock != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE plans
			   SET stock_reserved=stock_reserved-$3, stock_sold=stock_sold+$3
			 WHERE tenant_id=$1 AND id=$2::uuid AND stock_reserved >= $3`,
			tenantID, locked.Stock.PlanID, locked.Stock.Quantity)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("stock reservation capture lost")
		}
	}
	if locked.Purchase != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE plan_purchase_counters
			   SET reserved=reserved-$4, purchased=purchased+$4, updated_at=now()
			 WHERE tenant_id=$1 AND plan_id=$2::uuid AND user_id=$3::uuid
			   AND reserved >= $4`, tenantID, locked.Purchase.PlanID,
			locked.Purchase.UserID, locked.Purchase.Quantity)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("purchase-limit reservation capture lost")
		}
	}
	if locked.Coupon != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE coupons
			   SET reserved_count=reserved_count-1,
			       redeemed_count=redeemed_count+1, updated_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND reserved_count > 0`,
			tenantID, locked.Coupon.CouponID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("coupon counter capture lost")
		}
		tag, err = tx.Exec(ctx, `
			UPDATE coupon_redemptions
			   SET status='captured', captured_at=now()
			 WHERE tenant_id=$1 AND order_id=$2::uuid
			   AND reservation_id=$3::uuid AND status='held'`,
			tenantID, orderID, locked.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("coupon reservation capture lost")
		}
	}
	if locked.Balance != nil {
		if captureTxnID == "" {
			return errors.New("balance hold capture requires its ledger transaction")
		}
		tag, err := tx.Exec(ctx, `
			UPDATE balance_holds
			   SET status='captured', capture_txn_id=$4::uuid, captured_at=now()
			 WHERE tenant_id=$1 AND order_id=$2::uuid
			   AND reservation_id=$3::uuid AND status='held'`,
			tenantID, orderID, locked.ID, captureTxnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("balance hold capture lost")
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE order_reservations
		   SET state='captured', captured_at=now()
		 WHERE tenant_id=$1 AND id=$2::uuid AND order_id=$3::uuid
		   AND state='held'`, tenantID, locked.ID, orderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("parent reservation capture lost")
	}
	tag, err = tx.Exec(ctx, `
		INSERT INTO order_reservation_events
			(tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
			 business_request_id,actor_kind,actor_id)
		VALUES ($1,$2::uuid,$3::uuid,'held','captured','capture',
		        $4::uuid,'system',$5::uuid)`,
		tenantID, locked.ID, orderID, businessRequestID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("terminal reservation capture event was not inserted")
	}
	return nil
}
