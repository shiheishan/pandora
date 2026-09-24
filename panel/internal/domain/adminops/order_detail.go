// [INPUT]: 依赖 service.go 的 orderRowSelectSQL / scanOrderRow（订单行唯一形状），读 orders / order_items / users / payment_intents / payments / refunds，依赖 platform/db、platform/httpx
// [OUTPUT]: 对外提供 OrderDetail、OrderItemDetail、OrderPaymentHistory 及 GetOrder、GetOrderPaymentHistory
// [POS]: domain/adminops 的订单详情读模型：列表行 + 不可变快照 + 开单人；支付证据单独放在更高一级的 billing.payment.read 之下
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// OrderDetail is the immutable order and product snapshot. Payment evidence is
// intentionally served under the stronger billing.payment.read permission.
type OrderDetail struct {
	OrderRow
	UserID         string  `json:"user_id"`
	OrganizationID *string `json:"organization_id"`
	StateVersion   int64   `json:"state_version"`
	SubtotalAmount int64   `json:"subtotal_amount"`
	DiscountAmount int64   `json:"discount_amount"`
	TaxAmount      int64   `json:"tax_amount"`
	CouponID       *string `json:"coupon_id"`
	ManualReason   *string `json:"manual_reason"`
	// 人工单的开单人（抽屉「来源：人工开单 · 邮箱」）；门户下单为空。
	CreatedBy      *string           `json:"created_by"`
	CreatedByEmail *string           `json:"created_by_email"`
	SubscriptionID *string           `json:"subscription_id"`
	ExpiresAt      *time.Time        `json:"expires_at"`
	FulfilledAt    *time.Time        `json:"fulfilled_at"`
	CancelledAt    *time.Time        `json:"cancelled_at"`
	ExpiredAt      *time.Time        `json:"expired_at"`
	CancelReason   *string           `json:"cancel_reason"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Items          []OrderItemDetail `json:"items"`
}

type OrderItemDetail struct {
	ID                   string          `json:"id"`
	ProductID            *string         `json:"product_id"`
	PriceID              *string         `json:"price_id"`
	PlanID               *string         `json:"plan_id"`
	PlanVersionID        *string         `json:"plan_version_id"`
	ProductName          string          `json:"product_name"`
	PlanName             *string         `json:"plan_name"`
	PlanVersion          *int            `json:"plan_version"`
	Interval             *string         `json:"interval"`
	IntervalCount        *int16          `json:"interval_count"`
	SnapshotEntitlements json.RawMessage `json:"snapshot_entitlements"`
	SnapshotQuotas       json.RawMessage `json:"snapshot_quotas"`
	Quantity             int             `json:"quantity"`
	UnitAmount           int64           `json:"unit_amount"`
	LineAmount           int64           `json:"line_amount"`
	Currency             string          `json:"currency"`
	CreatedAt            time.Time       `json:"created_at"`
}

type PaymentIntentDetail struct {
	ID             string     `json:"id"`
	ProviderCode   string     `json:"provider_code"`
	ProviderName   string     `json:"provider_name"`
	Currency       string     `json:"currency"`
	Amount         int64      `json:"amount"`
	Status         string     `json:"status"`
	ProviderRef    *string    `json:"provider_ref"`
	FailureCode    *string    `json:"failure_code"`
	FailureMessage *string    `json:"failure_message"`
	ExpiresAt      *time.Time `json:"expires_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type PaymentDetail struct {
	ID                string    `json:"id"`
	PaymentIntentID   *string   `json:"payment_intent_id"`
	ProviderCode      string    `json:"provider_code"`
	ProviderName      string    `json:"provider_name"`
	ProviderPaymentID string    `json:"provider_payment_id"`
	Currency          string    `json:"currency"`
	Amount            int64     `json:"amount"`
	FeeAmount         int64     `json:"fee_amount"`
	RefundedAmount    int64     `json:"refunded_amount"`
	Status            string    `json:"status"`
	Method            *string   `json:"method"`
	PaidAt            time.Time `json:"paid_at"`
}

type RefundDetail struct {
	ID                 string     `json:"id"`
	PaymentID          *string    `json:"payment_id"`
	ProviderRefundID   *string    `json:"provider_refund_id"`
	Currency           string     `json:"currency"`
	Amount             int64      `json:"amount"`
	Reason             string     `json:"reason"`
	Status             string     `json:"status"`
	EntitlementRevoked bool       `json:"entitlement_revoked"`
	CommissionReversed bool       `json:"commission_reversed"`
	FailureMessage     *string    `json:"failure_message"`
	SucceededAt        *time.Time `json:"succeeded_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

// OrderPaymentHistory excludes provider credentials, action payloads, webhook
// bodies and ledger internals even for payment readers.
type OrderPaymentHistory struct {
	PaymentIntents []PaymentIntentDetail `json:"payment_intents"`
	Payments       []PaymentDetail       `json:"payments"`
	Refunds        []RefundDetail        `json:"refunds"`
}

func (s *Service) GetOrder(ctx context.Context, tenantID, orderID string) (*OrderDetail, error) {
	if _, err := uuid.Parse(orderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}

	out := &OrderDetail{Items: []OrderItemDetail{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 列表行部分与订单列表同一份查询：以前这里自己写一遍 SELECT，
		// 首项快照（plan_name / interval / item_count）恒为零值，与缺陷 9 同类
		row, err := scanOrderRow(tx.QueryRow(ctx, orderRowSelectSQL+`
			 WHERE o.tenant_id = $1 AND o.id = $2::uuid`, tenantID, orderID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		out.OrderRow = row
		if err := tx.QueryRow(ctx, `
			SELECT o.user_id,o.organization_id,o.state_version,
			       o.subtotal_amount,o.discount_amount,o.tax_amount,
			       o.coupon_id,o.manual_reason,o.created_by,cb.email,o.subscription_id,o.expires_at,
			       o.fulfilled_at,o.cancelled_at,o.expired_at,o.cancel_reason,o.updated_at
			  FROM orders o
			  LEFT JOIN users cb ON cb.tenant_id=o.tenant_id AND cb.id=o.created_by
			 WHERE o.tenant_id=$1 AND o.id=$2::uuid`, tenantID, orderID).Scan(
			&out.UserID, &out.OrganizationID, &out.StateVersion,
			&out.SubtotalAmount, &out.DiscountAmount, &out.TaxAmount,
			&out.CouponID, &out.ManualReason, &out.CreatedBy, &out.CreatedByEmail,
			&out.SubscriptionID, &out.ExpiresAt,
			&out.FulfilledAt, &out.CancelledAt, &out.ExpiredAt, &out.CancelReason,
			&out.UpdatedAt); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT id,product_id,price_id,plan_id,plan_version_id,
			       snapshot_product_name,snapshot_plan_name,snapshot_plan_version,
			       snapshot_interval,snapshot_interval_count,
			       snapshot_entitlements,snapshot_quotas,quantity,unit_amount,
			       line_amount,currency,created_at
			  FROM order_items WHERE tenant_id=$1 AND order_id=$2::uuid
			 ORDER BY created_at,id`, tenantID, orderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item OrderItemDetail
			if err := rows.Scan(&item.ID, &item.ProductID, &item.PriceID, &item.PlanID,
				&item.PlanVersionID, &item.ProductName, &item.PlanName, &item.PlanVersion,
				&item.Interval, &item.IntervalCount, &item.SnapshotEntitlements,
				&item.SnapshotQuotas, &item.Quantity, &item.UnitAmount, &item.LineAmount,
				&item.Currency, &item.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			out.Items = append(out.Items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		return nil
	})
	if err != nil {
		var httpErr *httpx.Error
		if errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return out, nil
}

func (s *Service) GetOrderPaymentHistory(ctx context.Context, tenantID, orderID string) (*OrderPaymentHistory, error) {
	if _, err := uuid.Parse(orderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	out := &OrderPaymentHistory{
		PaymentIntents: []PaymentIntentDetail{}, Payments: []PaymentDetail{}, Refunds: []RefundDetail{},
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var exists int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM orders WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, orderID).Scan(&exists); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT pi.id,pp.code,pp.display_name,pi.currency,pi.amount,pi.status,
			       pi.provider_ref,pi.failure_code,pi.failure_message,pi.expires_at,
			       pi.created_at,pi.updated_at
			  FROM payment_intents pi
			  JOIN payment_providers pp ON pp.tenant_id=pi.tenant_id AND pp.id=pi.provider_id
			 WHERE pi.tenant_id=$1 AND pi.order_id=$2::uuid
			 ORDER BY pi.created_at DESC,pi.id`, tenantID, orderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var intent PaymentIntentDetail
			if err := rows.Scan(&intent.ID, &intent.ProviderCode, &intent.ProviderName,
				&intent.Currency, &intent.Amount, &intent.Status, &intent.ProviderRef,
				&intent.FailureCode, &intent.FailureMessage, &intent.ExpiresAt,
				&intent.CreatedAt, &intent.UpdatedAt); err != nil {
				rows.Close()
				return err
			}
			out.PaymentIntents = append(out.PaymentIntents, intent)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = tx.Query(ctx, `
			SELECT p.id,p.payment_intent_id,pp.code,pp.display_name,p.provider_payment_id,
			       p.currency,p.amount,p.fee_amount,p.refunded_amount,p.status,p.method,p.paid_at
			  FROM payments p
			  JOIN payment_providers pp ON pp.tenant_id=p.tenant_id AND pp.id=p.provider_id
			 WHERE p.tenant_id=$1 AND p.order_id=$2::uuid
			 ORDER BY p.paid_at DESC,p.id`, tenantID, orderID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var payment PaymentDetail
			if err := rows.Scan(&payment.ID, &payment.PaymentIntentID, &payment.ProviderCode,
				&payment.ProviderName, &payment.ProviderPaymentID, &payment.Currency,
				&payment.Amount, &payment.FeeAmount, &payment.RefundedAmount, &payment.Status,
				&payment.Method, &payment.PaidAt); err != nil {
				rows.Close()
				return err
			}
			out.Payments = append(out.Payments, payment)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = tx.Query(ctx, `
			SELECT id,payment_id,provider_refund_id,currency,amount,reason,status,
			       entitlement_revoked,commission_reversed,failure_message,succeeded_at,created_at
			  FROM refunds WHERE tenant_id=$1 AND order_id=$2::uuid
			 ORDER BY created_at DESC,id`, tenantID, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var refund RefundDetail
			if err := rows.Scan(&refund.ID, &refund.PaymentID, &refund.ProviderRefundID,
				&refund.Currency, &refund.Amount, &refund.Reason, &refund.Status,
				&refund.EntitlementRevoked, &refund.CommissionReversed,
				&refund.FailureMessage, &refund.SucceededAt, &refund.CreatedAt); err != nil {
				return err
			}
			out.Refunds = append(out.Refunds, refund)
		}
		return rows.Err()
	})
	if err != nil {
		var httpErr *httpx.Error
		if errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return out, nil
}
