// [INPUT]: 依赖 orders / order_items 表与 platform/db、platform/httpx
// [OUTPUT]: 对外提供 ListMyOrders、MyOrderDetail 及其行类型
// [POS]: billing 的门户订单读模型；订单名取订单项套餐名，流量包订单没有套餐名时取商品名（流量包名）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 用户自己的订单列表与详情。
//
// 对应 Xboard 的 user/order/fetch 与 user/order/detail。此前用户端只有
// 下单、支付、取消三个动作，没有任何地方能看到自己买过什么 ——
// 连"我上周那单到底付没付成"都查不了，只能开工单问。

// MyOrderRow 是列表里的一行。
//
// 刻意不带 business_request_id、idempotency_key_id、coupon_id 这些内部标识：
// 用户不需要，暴露出去只会多一条可以拿来试探的信息。
type MyOrderRow struct {
	ID             string     `json:"id"`
	OrderNo        string     `json:"order_no"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	Currency       string     `json:"currency"`
	TotalAmount    int64      `json:"total_amount"`
	DiscountAmount int64      `json:"discount_amount"`
	BalanceApplied int64      `json:"balance_applied"`
	PayableAmount  int64      `json:"payable_amount"`
	PaidAmount     int64      `json:"paid_amount"`
	RefundedAmount int64      `json:"refunded_amount"`
	PlanName       string     `json:"plan_name,omitempty"`
	Cancellable    bool       `json:"cancellable"`
	CreatedAt      time.Time  `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at,omitempty"`
	CancelledAt    *time.Time `json:"cancelled_at,omitempty"`
	CancelReason   *string    `json:"cancel_reason,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// cancellableOrderStatuses 与 releaseOrderReservation 里允许释放的状态保持一致。
// 两处一旦不同步，界面上就会出现"按钮点了报冲突"的情况。
var cancellableOrderStatuses = map[string]bool{
	"draft": true, "pending_payment": true, "processing": true,
}

type ListMyOrdersInput struct {
	Status string
	Limit  int
	Offset int
}

func (s *Service) ListMyOrders(ctx context.Context, tenantID, userID string,
	in ListMyOrdersInput) ([]MyOrderRow, int64, error) {

	if tenantID == "" || userID == "" {
		return nil, 0, httpx.New(httpx.CodeBadRequest, "tenant and user are required")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, 0, httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 20
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	// 状态用白名单而不是 LIKE：这个参数直接来自查询串，
	// 放任 % 进去会让用户拼出跨状态的模糊匹配。
	status := strings.TrimSpace(in.Status)
	if status != "" && !isKnownOrderStatus(status) {
		return nil, 0, httpx.New(httpx.CodeBadRequest, "不支持的订单状态")
	}

	out := []MyOrderRow{}
	var total int64
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM orders
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			   AND ($3 = '' OR status::text = $3)`,
			tenantID, userID, status).Scan(&total); err != nil {
			return err
		}

		// 套餐名取订单项里的快照，不 join 当前的 plans ——
		// 套餐改名或下架之后，历史订单仍应显示当时买的那个名字。
		rows, err := tx.Query(ctx, `
			SELECT o.id::text, o.order_no, o.kind, o.status, o.currency::text,
			       o.total_amount, o.discount_amount, o.balance_applied,
			       o.payable_amount, o.paid_amount, o.refunded_amount,
			       COALESCE((SELECT coalesce(oi.snapshot_plan_name, oi.snapshot_product_name) FROM order_items oi
			                  WHERE oi.tenant_id = o.tenant_id AND oi.order_id = o.id
			                  ORDER BY oi.id LIMIT 1), ''),
			       o.created_at, o.paid_at, o.cancelled_at, o.cancel_reason,
			       o.expires_at
			  FROM orders o
			 WHERE o.tenant_id = $1 AND o.user_id = $2::uuid
			   AND ($3 = '' OR o.status::text = $3)
			 ORDER BY o.created_at DESC
			 LIMIT $4 OFFSET $5`,
			tenantID, userID, status, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var r MyOrderRow
			if err := rows.Scan(&r.ID, &r.OrderNo, &r.Kind, &r.Status, &r.Currency,
				&r.TotalAmount, &r.DiscountAmount, &r.BalanceApplied,
				&r.PayableAmount, &r.PaidAmount, &r.RefundedAmount, &r.PlanName,
				&r.CreatedAt, &r.PaidAt, &r.CancelledAt, &r.CancelReason,
				&r.ExpiresAt); err != nil {
				return err
			}
			r.Cancellable = cancellableOrderStatuses[r.Status]
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// MyOrderDetail 在列表行之上补齐明细与支付记录。
type MyOrderDetail struct {
	MyOrderRow
	Items    []MyOrderItem    `json:"items"`
	Payments []MyOrderPayment `json:"payments"`
}

type MyOrderItem struct {
	Name       string `json:"name"`
	Quantity   int    `json:"quantity"`
	UnitAmount int64  `json:"unit_amount"`
	LineAmount int64  `json:"line_amount"`
}

// MyOrderPayment 只回渠道与金额，不回 provider_payment_id ——
// 那是对账用的外部单号，用户拿不到用处，泄漏了反而多一个可猜测面。
type MyOrderPayment struct {
	Status    string    `json:"status"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) MyOrderDetail(ctx context.Context, tenantID, userID,
	orderID string) (*MyOrderDetail, error) {

	if tenantID == "" || userID == "" || orderID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant, user and order are required")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	// 订单号非 UUID 时直接当"不存在"处理，而不是报参数错误：
	// 两者回不同的错，就等于告诉试探者哪些 id 格式是对的。
	if _, err := uuid.Parse(orderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}

	var out MyOrderDetail
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT o.id::text, o.order_no, o.kind, o.status, o.currency::text,
			       o.total_amount, o.discount_amount, o.balance_applied,
			       o.payable_amount, o.paid_amount, o.refunded_amount,
			       COALESCE((SELECT coalesce(oi.snapshot_plan_name, oi.snapshot_product_name) FROM order_items oi
			                  WHERE oi.tenant_id = o.tenant_id AND oi.order_id = o.id
			                  ORDER BY oi.id LIMIT 1), ''),
			       o.created_at, o.paid_at, o.cancelled_at, o.cancel_reason,
			       o.expires_at
			  FROM orders o
			 WHERE o.tenant_id = $1 AND o.id = $2::uuid AND o.user_id = $3::uuid`,
			tenantID, orderID, userID).Scan(
			&out.ID, &out.OrderNo, &out.Kind, &out.Status, &out.Currency,
			&out.TotalAmount, &out.DiscountAmount, &out.BalanceApplied,
			&out.PayableAmount, &out.PaidAmount, &out.RefundedAmount, &out.PlanName,
			&out.CreatedAt, &out.PaidAt, &out.CancelledAt, &out.CancelReason,
			&out.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		out.Cancellable = cancellableOrderStatuses[out.Status]

		itemRows, err := tx.Query(ctx, `
			SELECT coalesce(snapshot_plan_name, snapshot_product_name), quantity, unit_amount, line_amount
			  FROM order_items
			 WHERE tenant_id = $1 AND order_id = $2::uuid
			 ORDER BY id`, tenantID, orderID)
		if err != nil {
			return err
		}
		defer itemRows.Close()
		out.Items = []MyOrderItem{}
		for itemRows.Next() {
			var it MyOrderItem
			if err := itemRows.Scan(&it.Name, &it.Quantity, &it.UnitAmount,
				&it.LineAmount); err != nil {
				return err
			}
			out.Items = append(out.Items, it)
		}
		if err := itemRows.Err(); err != nil {
			return err
		}

		payRows, err := tx.Query(ctx, `
			SELECT status, amount, currency::text, created_at
			  FROM payments
			 WHERE tenant_id = $1 AND order_id = $2::uuid
			 ORDER BY created_at`, tenantID, orderID)
		if err != nil {
			return err
		}
		defer payRows.Close()
		out.Payments = []MyOrderPayment{}
		for payRows.Next() {
			var p MyOrderPayment
			if err := payRows.Scan(&p.Status, &p.Amount, &p.Currency,
				&p.CreatedAt); err != nil {
				return err
			}
			out.Payments = append(out.Payments, p)
		}
		return payRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func isKnownOrderStatus(s string) bool {
	switch s {
	case "draft", "pending_payment", "processing", "paid", "fulfilled",
		"cancelled", "expired", "partially_refunded", "refunded":
		return true
	}
	return false
}
