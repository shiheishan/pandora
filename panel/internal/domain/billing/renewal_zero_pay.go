package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// zeroPaySubscriptionCapture 是挂在已有订阅上的零元单（续费 renewal、变更套餐
// upgrade）当场捕获所需的全部输入。
type zeroPaySubscriptionCapture struct {
	Kind              string
	TenantID          string
	UserID            string
	OrderID           string
	SubscriptionID    string
	ReservationID     string
	BusinessRequestID string
	Currency          string
	SubtotalAmount    int64
	DiscountAmount    int64
	TotalAmount       int64
	BalanceApplied    int64
	CouponID          *string
	HoldAccountID     string
	RevenueAccountID  string
	ProrationCredit   int64
	// ManualGrant：后台人工开单赠送的续费（全额减免，见 reservationLockRequest.ManualGrant）
	ManualGrant bool
	// SmallDueWaived：应付低于支付最低额、余额又不够，免掉并进折扣的那点钱（见 reservationLockRequest）
	SmallDueWaived int64
}

// captureZeroPaySubscriptionOrder captures the complete held renewal or plan
// change graph and fulfils the already locked subscription before the create
// transaction commits.
func (s *Service) captureZeroPaySubscriptionOrder(ctx context.Context, tx pgx.Tx,
	in zeroPaySubscriptionCapture) error {
	if !subscriptionBoundOrderKind(in.Kind) {
		return fmt.Errorf("zero-pay subscription capture does not support order kind %q", in.Kind)
	}
	locked, err := lockOrderReservationGraph(ctx, tx, reservationLockRequest{
		TenantID: in.TenantID, OrderID: in.OrderID, UserID: in.UserID,
		Kind: in.Kind, Currency: in.Currency, CouponID: in.CouponID,
		SubtotalAmount: in.SubtotalAmount, DiscountAmount: in.DiscountAmount,
		TotalAmount: in.TotalAmount, PayableAmount: 0,
		BalanceAmount: in.BalanceApplied, ProrationCredit: in.ProrationCredit,
		ManualGrant: in.ManualGrant, SmallDueWaived: in.SmallDueWaived,
	})
	if err != nil {
		return err
	}

	var captureTxnID string
	if in.BalanceApplied > 0 {
		if in.BalanceApplied != in.TotalAmount || in.HoldAccountID == "" ||
			in.RevenueAccountID == "" {
			return errors.New("zero-pay " + in.Kind + " has an invalid balance hold")
		}
		captureTxnID, err = Post(ctx, tx, in.TenantID, Posting{
			Kind: "order_paid", Currency: in.Currency,
			SourceType: "order", SourceID: &in.OrderID,
			Memo:      "zero-pay " + in.Kind + " balance capture",
			ActorKind: "user", ActorID: &in.UserID,
			Entries: []Entry{
				{AccountID: in.HoldAccountID, Direction: Debit,
					Amount: in.BalanceApplied, Description: "capture " + in.Kind + " balance hold"},
				{AccountID: in.RevenueAccountID, Direction: Credit,
					Amount: in.TotalAmount, Description: in.Kind + " revenue"},
			},
		})
		if err != nil {
			return err
		}
	}
	if err := captureLockedReservation(ctx, tx, in.TenantID, in.OrderID,
		in.UserID, in.BusinessRequestID, locked, captureTxnID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders
		   SET status='paid', paid_amount=total_amount, paid_at=now()
		 WHERE tenant_id=$1 AND id=$2::uuid AND status='pending_payment'`,
		in.TenantID, in.OrderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay " + in.Kind + " paid transition lost")
	}
	if in.Kind == "upgrade" {
		_, err = s.fulfillPlanChangeLocked(ctx, tx, in.TenantID, in.OrderID,
			in.UserID, in.SubscriptionID)
		return err
	}
	_, err = s.fulfillRenewalLocked(ctx, tx, in.TenantID, in.OrderID,
		in.UserID, in.SubscriptionID)
	return err
}
