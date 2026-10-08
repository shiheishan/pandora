package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/domain/plugin"
)

type zeroPayCapture struct {
	TenantID          string
	UserID            string
	OrderID           string
	ReservationID     string
	BusinessRequestID string
	// Kind 是 new 或 addon：新购要结转库存并开订阅，流量包没有库存、履约是发余额
	Kind             string
	PlanID           string
	Currency         string
	TotalAmount      int64
	BalanceApplied   int64
	HoldAccountID    string
	HasPurchaseLimit bool
	Coupon           *couponMatch
}

// captureZeroPayOrder converts every held resource to captured and fulfils a
// zero-payable order before the surrounding create-order transaction commits.
func (s *Service) captureZeroPayOrder(ctx context.Context, tx pgx.Tx, in zeroPayCapture) error {
	var captureTxnID string
	if in.BalanceApplied > 0 {
		if in.BalanceApplied != in.TotalAmount || in.HoldAccountID == "" {
			return errors.New("zero-pay order has an invalid balance hold")
		}
		revenueAccountID, err := EnsureAccount(ctx, tx, in.TenantID,
			AccountPlatformRevenue, in.Currency, nil, "main")
		if err != nil {
			return err
		}
		captureTxnID, err = Post(ctx, tx, in.TenantID, Posting{
			Kind: "order_paid", Currency: in.Currency,
			SourceType: "order", SourceID: &in.OrderID,
			Memo: "zero-pay order balance capture", ActorKind: "user", ActorID: &in.UserID,
			Entries: []Entry{
				{AccountID: in.HoldAccountID, Direction: Debit, Amount: in.BalanceApplied,
					Description: "capture order balance hold"},
				{AccountID: revenueAccountID, Direction: Credit, Amount: in.TotalAmount,
					Description: "order revenue"},
			},
		})
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE balance_holds
			   SET status = 'captured', capture_txn_id = $4::uuid, captured_at = now()
			 WHERE tenant_id = $1 AND order_id = $2::uuid
			   AND reservation_id = $3::uuid AND status = 'held'`,
			in.TenantID, in.OrderID, in.ReservationID, captureTxnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order balance hold capture lost")
		}
	}

	if in.Kind != "new" && in.Kind != "addon" {
		return fmt.Errorf("zero-pay capture does not support order kind %q", in.Kind)
	}
	var tag pgconn.CommandTag
	var err error
	if in.Kind == "new" {
		tag, err = tx.Exec(ctx, `
			UPDATE plans
			   SET stock_reserved = stock_reserved - 1, stock_sold = stock_sold + 1
			 WHERE tenant_id = $1 AND id = $2::uuid AND stock_reserved > 0`,
			in.TenantID, in.PlanID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order stock capture lost")
		}
	}

	if in.HasPurchaseLimit {
		tag, err = tx.Exec(ctx, `
			UPDATE plan_purchase_counters
			   SET reserved = reserved - 1, purchased = purchased + 1, updated_at = now()
			 WHERE tenant_id = $1 AND plan_id = $2::uuid AND user_id = $3::uuid
			   AND reserved > 0`, in.TenantID, in.PlanID, in.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order purchase-limit capture lost")
		}
	}

	if in.Coupon != nil {
		tag, err = tx.Exec(ctx, `
			UPDATE coupons
			   SET reserved_count = reserved_count - 1,
			       redeemed_count = redeemed_count + 1, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid AND reserved_count > 0`,
			in.TenantID, in.Coupon.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order coupon counter capture lost")
		}
		tag, err = tx.Exec(ctx, `
			UPDATE coupon_redemptions
			   SET status = 'captured', captured_at = now()
			 WHERE tenant_id = $1 AND order_id = $2::uuid
			   AND reservation_id = $3::uuid AND status = 'held'`,
			in.TenantID, in.OrderID, in.ReservationID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order coupon capture lost")
		}
	}

	tag, err = tx.Exec(ctx, `
		UPDATE order_reservations
		   SET state = 'captured', captured_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND order_id = $3::uuid
		   AND state = 'held'`, in.TenantID, in.ReservationID, in.OrderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay order parent reservation capture lost")
	}
	tag, err = tx.Exec(ctx, `
		INSERT INTO order_reservation_events
			(tenant_id, reservation_id, order_id, from_state, to_state,
			 event_kind, business_request_id, actor_kind, actor_id)
		VALUES ($1, $2::uuid, $3::uuid, 'held', 'captured',
		        'capture', $4::uuid, 'user', $5::uuid)`,
		in.TenantID, in.ReservationID, in.OrderID, in.BusinessRequestID, in.UserID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay terminal capture event was not inserted")
	}

	tag, err = tx.Exec(ctx, `
		UPDATE orders
		   SET status = 'paid', paid_amount = total_amount, paid_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'pending_payment'`,
		in.TenantID, in.OrderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay order paid transition lost")
	}
	subID := ""
	if in.Kind == "addon" {
		if _, err := fulfillTrafficPackOrder(ctx, tx, in.TenantID, in.OrderID, in.UserID); err != nil {
			return err
		}
	} else if subID, err = s.fulfillOrder(ctx, tx, in.TenantID, in.OrderID, in.UserID); err != nil {
		return err
	}
	// 零元单也算一次支付完成：插件那边不该因为金额是 0 就漏掉这笔。
	return plugin.EmitOrderPaid(ctx, tx, in.TenantID, in.OrderID, in.UserID,
		in.Kind, "", 0, subID)
}
