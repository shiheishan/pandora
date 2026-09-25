// [INPUT]: 依赖 reservations.go 的 lockOrderReservationGraph、ledger.go 的记账、platform/audit、platform/httpx
// [OUTPUT]: 对外提供 AdminCancelOrder、CancelOrder、ReleaseOrderOutput；包内提供释放共用的 releaseOrderReservation、lockReleaseReservationGraph 与把释放错误翻成中文接口错误的 releaseHTTPError
// [POS]: billing 的订单释放（取消 / 过期）：把 held 预留图整体转成 released 并退回余额冻结；new / topup / addon / upgrade（变更套餐）走同一套预留图锁，renewal 走续费专用分支；金额恒等式经 reservations.go 的 orderTotal
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var (
	errOrderReleaseNotFound        = errors.New("order release target not found")
	errOrderReleaseConflict        = errors.New("order cannot enter the requested release state")
	errOrderReleasePaymentEvidence = errors.New("order has successful payment evidence")
)

// releaseConflict 是给人看的冲突原因：errors.Is 仍认作 errOrderReleaseConflict，
// 过期任务照旧把它当正常的空转，取消接口把 message 原样回给前端（R95）
type releaseConflict struct{ message string }

func (e releaseConflict) Error() string        { return e.message }
func (e releaseConflict) Is(target error) bool { return target == errOrderReleaseConflict }

// releaseHTTPError 把释放事务的错误翻成接口错误，后台与门户取消共用
func releaseHTTPError(err error) error {
	var rc releaseConflict
	var he *httpx.Error
	switch {
	case errors.Is(err, errOrderReleaseNotFound):
		return httpx.NotFoundOrForbidden()
	case errors.As(err, &rc):
		return httpx.New(httpx.CodeConflict, rc.message)
	case errors.Is(err, errOrderReleasePaymentEvidence):
		return httpx.New(httpx.CodeConflict, "这张订单已有入账，不能取消")
	case errors.As(err, &he):
		return he
	default:
		return httpx.Internal(err)
	}
}

// ReleaseOrderOutput is the monotonic terminal result of a cancellation or
// expiry. AlreadyTerminal is true only when the order was already in the same
// requested terminal state and no evidence or ledger row was written.
type ReleaseOrderOutput struct {
	OrderID         string     `json:"order_id"`
	Status          string     `json:"status"`
	StateVersion    int64      `json:"state_version"`
	CancelledAt     *time.Time `json:"cancelled_at,omitempty"`
	CancelReason    *string    `json:"cancel_reason,omitempty"`
	AlreadyTerminal bool       `json:"already_terminal"`
}

type AdminCancelOrderInput struct {
	OrderID              string
	ActorID              string
	ExpectedStateVersion int64
	Reason               string
}

type releaseOrderRequest struct {
	OrderID              string
	UserID               string
	Target               string
	EventKind            string
	Reason               string
	ActorKind            string
	ActorID              *string
	APIDomain            string
	RequireDue           bool
	SkipLocked           bool
	ExpectedStateVersion int64
}

type releaseOrderResult struct {
	Output  ReleaseOrderOutput
	Skipped bool
}

type releaseOrderShape struct {
	ID                string
	UserID            string
	Status            string
	Kind              string
	Currency          string
	BusinessRequestID string
	SubtotalAmount    int64
	DiscountAmount    int64
	TaxAmount         int64
	TotalAmount       int64
	BalanceAmount     int64
	PayableAmount     int64
	CouponID          *string
	ProrationCredit   int64
	StateVersion      int64
	CancelledAt       *time.Time
	CancelReason      *string
}

// AdminCancelOrder exposes the existing reservation-release transaction to the
// admin domain with an explicit state-version compare-and-swap. Authorization,
// recent reauthentication and HTTP idempotency remain router responsibilities.
func (s *Service) AdminCancelOrder(ctx context.Context, tenantID string,
	in AdminCancelOrderInput) (*ReleaseOrderOutput, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if tenantID == "" || in.OrderID == "" || in.ActorID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant, actor and order are required")
	}
	if _, err := uuid.Parse(in.ActorID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "actor identifier is invalid")
	}
	if _, err := uuid.Parse(in.OrderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if in.ExpectedStateVersion <= 0 {
		return nil, httpx.New(httpx.CodeBadRequest, "expected_state_version 必须是正整数")
	}
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return nil, httpx.New(httpx.CodeBadRequest, "取消原因需要 5 到 500 个字")
	}

	actorID := in.ActorID
	var result releaseOrderResult
	err := s.pool.InTx(ctx, dbScope(tenantID, actorID), func(tx pgx.Tx) error {
		var err error
		result, err = releaseOrderReservation(ctx, tx, tenantID, releaseOrderRequest{
			OrderID: in.OrderID, Target: "cancelled", EventKind: "cancel", Reason: in.Reason,
			ActorKind: "admin", ActorID: &actorID, APIDomain: "admin",
			ExpectedStateVersion: in.ExpectedStateVersion,
		})
		return err
	})
	if err != nil {
		return nil, releaseHTTPError(err)
	}
	return &result.Output, nil
}

func releasedStatusMessage(status string) string {
	if status == "expired" {
		return "订单已过期"
	}
	return "订单已取消"
}

type releaseIntentLock struct {
	ID     string
	Status string
}

// CancelOrder releases an order owned by userID. Repeating cancellation of an
// already-cancelled order is idempotent; every other terminal state is a
// conflict and is never rewritten.
func (s *Service) CancelOrder(ctx context.Context, tenantID, userID,
	orderID string) (*ReleaseOrderOutput, error) {

	if tenantID == "" || userID == "" || orderID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant, user and order are required")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	if _, err := uuid.Parse(orderID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "order identifier is invalid")
	}

	actorID := userID
	var result releaseOrderResult
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		var err error
		result, err = releaseOrderReservation(ctx, tx, tenantID, releaseOrderRequest{
			OrderID: orderID, UserID: userID, Target: "cancelled",
			EventKind: "cancel", Reason: "user_cancelled", ActorKind: "user",
			ActorID: &actorID, APIDomain: "public",
		})
		return err
	})
	if err != nil {
		return nil, releaseHTTPError(err)
	}
	return &result.Output, nil
}

// releaseOrderReservation owns the complete release transaction after its
// caller has established tenant scope. Its first row lock is always the order;
// the remainder is intents, reservation graph, counters, coupons, balance
// hold, UUID-sorted ledger accounts, ledger writes, resource transitions,
// terminal reservation event, order transition, audit and deferred gates.
func releaseOrderReservation(ctx context.Context, tx pgx.Tx, tenantID string,
	req releaseOrderRequest) (releaseOrderResult, error) {

	var result releaseOrderResult
	if req.Target != "cancelled" && req.Target != "expired" {
		return result, fmt.Errorf("unsupported release target %q", req.Target)
	}
	if (req.Target == "cancelled" && req.EventKind != "cancel") ||
		(req.Target == "expired" && req.EventKind != "expire") {
		return result, errors.New("release target and event kind do not match")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return result, errors.New("release reason is required")
	}

	shape, skipped, err := lockReleaseOrder(ctx, tx, tenantID, req)
	if err != nil {
		return result, err
	}
	if skipped {
		result.Skipped = true
		return result, nil
	}
	result.Output = ReleaseOrderOutput{OrderID: shape.ID, Status: req.Target,
		StateVersion: shape.StateVersion, CancelledAt: shape.CancelledAt, CancelReason: shape.CancelReason}

	switch shape.Status {
	case req.Target:
		intents, err := lockActiveReleaseIntents(ctx, tx, tenantID, shape.ID)
		if err != nil {
			return result, err
		}
		if len(intents) != 0 {
			return result, errors.New("terminal released order still has an active payment intent")
		}
		if err := validateIdempotentRelease(ctx, tx, tenantID, shape, req); err != nil {
			return result, err
		}
		result.Output.AlreadyTerminal = true
		return result, nil
	case "cancelled", "expired":
		return result, releaseConflict{releasedStatusMessage(shape.Status)}
	case "paid", "fulfilled", "partially_refunded", "refunded":
		return result, releaseConflict{"订单已支付，不能取消"}
	case "draft", "pending_payment", "processing":
		// Continue below.
	default:
		return result, releaseConflict{"订单当前状态（" + shape.Status + "）不能取消"}
	}
	if req.ExpectedStateVersion > 0 && shape.StateVersion != req.ExpectedStateVersion {
		return result, httpx.New(httpx.CodeConflict, "订单状态已变化，请刷新后再操作")
	}

	intents, err := lockActiveReleaseIntents(ctx, tx, tenantID, shape.ID)
	if err != nil {
		return result, err
	}
	if len(intents) > 1 {
		return result, errors.New("order has more than one active payment intent")
	}
	if err := lockAndRejectSettledPaymentEvidence(ctx, tx, tenantID, shape.ID); err != nil {
		return result, err
	}

	locked, err := lockReleaseReservationGraph(ctx, tx, tenantID, shape, req.RequireDue)
	if err != nil {
		return result, err
	}

	var releaseTxnID string
	if locked.Balance != nil {
		if _, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, nil,
			locked.Balance.AvailableAccountID, locked.Balance.HoldAccountID); err != nil {
			return result, err
		}
		holdBalance, err := Balance(ctx, tx, locked.Balance.HoldAccountID)
		if err != nil {
			return result, err
		}
		if holdBalance < locked.Balance.Amount {
			return result, errors.New("balance hold account cannot fund its release")
		}
		releaseTxnID, err = Post(ctx, tx, tenantID, Posting{
			Kind: "balance_release", Currency: shape.Currency,
			SourceType: "order", SourceID: &shape.ID,
			Memo: req.Reason, ActorKind: req.ActorKind, ActorID: req.ActorID,
			Entries: []Entry{
				{AccountID: locked.Balance.HoldAccountID, Direction: Debit,
					Amount: locked.Balance.Amount, Description: "release held balance"},
				{AccountID: locked.Balance.AvailableAccountID, Direction: Credit,
					Amount: locked.Balance.Amount, Description: "restore available balance"},
			},
		})
		if err != nil {
			return result, err
		}
	}

	intentTarget := "cancelled"
	if req.Target == "expired" {
		intentTarget = "expired"
	}
	for _, intent := range intents {
		tag, err := tx.Exec(ctx, `
			UPDATE payment_intents SET status=$4
			 WHERE tenant_id=$1 AND order_id=$2::uuid AND id=$3::uuid
			   AND status=$5`, tenantID, shape.ID, intent.ID, intentTarget, intent.Status)
		if err != nil {
			return result, err
		}
		if tag.RowsAffected() != 1 {
			return result, errors.New("active payment intent release transition lost")
		}
	}

	if err := releaseLockedReservation(ctx, tx, tenantID, shape, locked,
		req, releaseTxnID); err != nil {
		return result, err
	}

	if req.Target == "cancelled" {
		var cancelledAt time.Time
		var cancelReason string
		err = tx.QueryRow(ctx, `
			UPDATE orders
			   SET status='cancelled',cancelled_at=now(),cancel_reason=$3
			 WHERE tenant_id=$1 AND id=$2::uuid
			   AND status IN ('draft','pending_payment','processing')
			   AND ($4::bigint=0 OR state_version=$4)
			 RETURNING state_version,cancelled_at,cancel_reason`,
			tenantID, shape.ID, req.Reason, req.ExpectedStateVersion).Scan(
			&result.Output.StateVersion, &cancelledAt, &cancelReason)
		result.Output.CancelledAt = &cancelledAt
		result.Output.CancelReason = &cancelReason
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE orders
			   SET status='expired',expired_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid
			   AND status IN ('draft','pending_payment','processing')
			 RETURNING state_version`, tenantID, shape.ID).Scan(&result.Output.StateVersion)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, errors.New("order release transition lost")
		}
		return result, err
	}

	if err := audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: req.ActorKind, ActorID: req.ActorID,
		Action: "order." + req.Target, ResourceType: "order", ResourceID: &shape.ID,
		BeforeDigest: map[string]any{"status": shape.Status},
		AfterDigest: map[string]any{
			"status": req.Target, "reason": req.Reason,
			"reservation_id": locked.ID, "balance_release_txn": releaseTxnID,
		},
		APIDomain: req.APIDomain, RequestID: httpx.RequestIDFrom(ctx),
	}); err != nil {
		return result, err
	}
	if err := plugin.EmitOrderCancelled(ctx, tx, tenantID, shape.ID,
		req.Target, req.Reason); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return result, err
	}
	return result, nil
}

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

	payments, err := tx.Query(ctx, `
		SELECT id::text FROM payments
		 WHERE tenant_id=$1 AND order_id=$2::uuid
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

func releaseLockedReservation(ctx context.Context, tx pgx.Tx, tenantID string,
	shape releaseOrderShape, locked *lockedReservation, req releaseOrderRequest,
	releaseTxnID string) error {

	if locked.Stock != nil {
		tag, err := tx.Exec(ctx, `UPDATE plans SET stock_reserved=stock_reserved-$3
			WHERE tenant_id=$1 AND id=$2::uuid AND stock_reserved >= $3`,
			tenantID, locked.Stock.PlanID, locked.Stock.Quantity)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("stock reservation release lost")
		}
	}
	if locked.Purchase != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE plan_purchase_counters
			   SET reserved=reserved-$4,updated_at=now()
			 WHERE tenant_id=$1 AND plan_id=$2::uuid AND user_id=$3::uuid
			   AND reserved >= $4`, tenantID, locked.Purchase.PlanID,
			locked.Purchase.UserID, locked.Purchase.Quantity)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("purchase-limit reservation release lost")
		}
	}
	if locked.Coupon != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE coupons SET reserved_count=reserved_count-1,updated_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND reserved_count > 0`,
			tenantID, locked.Coupon.CouponID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("coupon counter release lost")
		}
		tag, err = tx.Exec(ctx, `
			UPDATE coupon_redemptions
			   SET status='released',released_at=now(),reverted_at=now(),revert_reason=$4
			 WHERE tenant_id=$1 AND order_id=$2::uuid AND reservation_id=$3::uuid
			   AND status='held'`, tenantID, shape.ID, locked.ID, req.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("coupon reservation release lost")
		}
	}
	if locked.Balance != nil {
		if releaseTxnID == "" {
			return errors.New("balance hold release requires its ledger transaction")
		}
		tag, err := tx.Exec(ctx, `
			UPDATE balance_holds
			   SET status='released',release_txn_id=$4::uuid,released_at=now()
			 WHERE tenant_id=$1 AND order_id=$2::uuid AND reservation_id=$3::uuid
			   AND status='held'`, tenantID, shape.ID, locked.ID, releaseTxnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("balance hold release lost")
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE order_reservations
		   SET state='released',released_at=now(),release_reason=$4
		 WHERE tenant_id=$1 AND id=$2::uuid AND order_id=$3::uuid AND state='held'`,
		tenantID, locked.ID, shape.ID, req.Reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("parent reservation release lost")
	}

	tag, err = tx.Exec(ctx, `
		INSERT INTO order_reservation_events
			(tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
			 business_request_id,actor_kind,actor_id,reason)
		VALUES ($1,$2::uuid,$3::uuid,'held','released',$4,$5::uuid,$6,$7::uuid,$8)`,
		tenantID, locked.ID, shape.ID, req.EventKind, shape.BusinessRequestID,
		req.ActorKind, req.ActorID, req.Reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("terminal reservation release event was not inserted")
	}
	return nil
}

func validateIdempotentRelease(ctx context.Context, tx pgx.Tx, tenantID string,
	shape releaseOrderShape, req releaseOrderRequest) error {

	var reservationID, state string
	if err := tx.QueryRow(ctx, `SELECT id::text,state FROM order_reservations
		WHERE tenant_id=$1 AND order_id=$2::uuid FOR UPDATE`, tenantID, shape.ID).
		Scan(&reservationID, &state); err != nil {
		return err
	}
	if state != "released" {
		return errors.New("terminal order does not have a released reservation parent")
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM order_reservation_events
		 WHERE tenant_id=$1 AND reservation_id=$2::uuid AND order_id=$3::uuid
		   AND from_state='held' AND to_state='released'
		   AND (event_kind=$4 OR (event_kind='backfill' AND legacy_backfill))
		   AND business_request_id=$5::uuid`, tenantID, reservationID, shape.ID,
		req.EventKind, shape.BusinessRequestID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("terminal order lacks its exact reservation release event")
	}
	return nil
}
