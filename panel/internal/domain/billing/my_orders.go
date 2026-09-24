// [INPUT]: 依赖 orders / order_items / coupons / subscriptions / payments / payment_providers 表与 platform/db、platform/httpx
// [OUTPUT]: 对外提供 ListMyOrders（含 MyOrderCounts 筛选段计数）、MyOrderDetail 及其行类型，ParseOrderStatuses（门户与后台订单列表共用的状态筛选口径）
// [POS]: billing 的门户订单读模型；myOrderSelectSQL 是列表与详情共用的唯一行形状，订单名取订单项套餐名，流量包订单没有套餐名时取商品名（流量包名）
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
	ID             string `json:"id"`
	OrderNo        string `json:"order_no"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	DiscountAmount int64  `json:"discount_amount"`
	BalanceApplied int64  `json:"balance_applied"`
	PayableAmount  int64  `json:"payable_amount"`
	PaidAmount     int64  `json:"paid_amount"`
	RefundedAmount int64  `json:"refunded_amount"`
	PlanName       string `json:"plan_name,omitempty"`
	// 首个订单项的周期与商品名快照：列表标题要写「专业版 · 季付」，
	// 流量包订单要写「流量包 · 200 GB」（商品名就是流量包名）。
	Interval      string     `json:"interval,omitempty"`
	IntervalCount int        `json:"interval_count,omitempty"`
	ItemName      string     `json:"item_name"`
	Cancellable   bool       `json:"cancellable"`
	CreatedAt     time.Time  `json:"created_at"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	CancelledAt   *time.Time `json:"cancelled_at,omitempty"`
	CancelReason  *string    `json:"cancel_reason,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// cancellableOrderStatuses 与 releaseOrderReservation 里允许释放的状态保持一致。
// 两处一旦不同步，界面上就会出现"按钮点了报冲突"的情况。
var cancellableOrderStatuses = map[string]bool{
	"draft": true, "pending_payment": true, "processing": true,
}

type ListMyOrdersInput struct {
	// Status 是逗号分隔的状态白名单（空 = 全部），见 ParseOrderStatuses。
	Status string
	Limit  int
	Offset int
}

// MyOrderCounts 是本人全部订单按门户筛选段归类的计数，不受当前筛选影响。
type MyOrderCounts struct {
	Open     int64 `json:"open"`     // draft + pending_payment + processing
	Paid     int64 `json:"paid"`     // paid + fulfilled
	Closed   int64 `json:"closed"`   // cancelled + expired
	Refunded int64 `json:"refunded"` // partially_refunded + refunded
}

// myOrderSelectSQL 是门户订单行的唯一查询形状，列表与详情共用，调用方只拼
// WHERE / ORDER / LIMIT。首项取订单项里的快照而不是当前的套餐：套餐改名或
// 下架之后，历史订单仍应显示当时买的那个名字。
const myOrderSelectSQL = `
			SELECT o.id::text, o.order_no, o.kind, o.status, o.currency::text,
			       o.total_amount, o.discount_amount, o.balance_applied,
			       o.payable_amount, o.paid_amount, o.refunded_amount,
			       COALESCE(it.name, ''), COALESCE(it.snapshot_interval, ''),
			       COALESCE(it.snapshot_interval_count, 0), COALESCE(it.snapshot_product_name, ''),
			       o.created_at, o.paid_at, o.cancelled_at, o.cancel_reason,
			       o.expires_at
			  FROM orders o
			  LEFT JOIN LATERAL (
			    SELECT coalesce(oi.snapshot_plan_name, oi.snapshot_product_name) AS name,
			           oi.snapshot_interval, oi.snapshot_interval_count, oi.snapshot_product_name
			      FROM order_items oi
			     WHERE oi.tenant_id = o.tenant_id AND oi.order_id = o.id
			     ORDER BY oi.id LIMIT 1
			  ) it ON true`

func scanMyOrderRow(row pgx.Row, r *MyOrderRow) error {
	if err := row.Scan(&r.ID, &r.OrderNo, &r.Kind, &r.Status, &r.Currency,
		&r.TotalAmount, &r.DiscountAmount, &r.BalanceApplied,
		&r.PayableAmount, &r.PaidAmount, &r.RefundedAmount,
		&r.PlanName, &r.Interval, &r.IntervalCount, &r.ItemName,
		&r.CreatedAt, &r.PaidAt, &r.CancelledAt, &r.CancelReason,
		&r.ExpiresAt); err != nil {
		return err
	}
	r.Cancellable = cancellableOrderStatuses[r.Status]
	return nil
}

func (s *Service) ListMyOrders(ctx context.Context, tenantID, userID string,
	in ListMyOrdersInput) ([]MyOrderRow, int64, MyOrderCounts, error) {

	var counts MyOrderCounts
	if tenantID == "" || userID == "" {
		return nil, 0, counts, httpx.New(httpx.CodeBadRequest, "tenant and user are required")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return nil, 0, counts, httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 20
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	statuses, err := ParseOrderStatuses(in.Status)
	if err != nil {
		return nil, 0, counts, err
	}

	out := []MyOrderRow{}
	var total int64
	err = s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE $3::text[] IS NULL OR status::text = ANY($3)),
			       count(*) FILTER (WHERE status IN ('draft','pending_payment','processing')),
			       count(*) FILTER (WHERE status IN ('paid','fulfilled')),
			       count(*) FILTER (WHERE status IN ('cancelled','expired')),
			       count(*) FILTER (WHERE status IN ('partially_refunded','refunded'))
			  FROM orders
			 WHERE tenant_id = $1 AND user_id = $2::uuid`,
			tenantID, userID, statuses).Scan(&total,
			&counts.Open, &counts.Paid, &counts.Closed, &counts.Refunded); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, myOrderSelectSQL+`
			 WHERE o.tenant_id = $1 AND o.user_id = $2::uuid
			   AND ($3::text[] IS NULL OR o.status::text = ANY($3))
			 ORDER BY o.created_at DESC
			 LIMIT $4 OFFSET $5`,
			tenantID, userID, statuses, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var r MyOrderRow
			if err := scanMyOrderRow(rows, &r); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, counts, err
	}
	return out, total, counts, nil
}

// MyOrderDetail 在列表行之上补齐明细与支付记录。
type MyOrderDetail struct {
	MyOrderRow
	// CouponCode 是下单时用的优惠码（展开区「优惠」行）。
	CouponCode *string `json:"coupon_code,omitempty"`
	// SubscriptionPeriodEnd 是已履约订单所属订阅当前的到期时间
	// （结果行「有效期至 …」）；未履约或不开订阅的单没有。
	SubscriptionPeriodEnd *time.Time       `json:"subscription_period_end,omitempty"`
	Items                 []MyOrderItem    `json:"items"`
	Payments              []MyOrderPayment `json:"payments"`
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
	Status       string    `json:"status"`
	Amount       int64     `json:"amount"`
	Currency     string    `json:"currency"`
	Method       *string   `json:"method,omitempty"`
	ProviderName string    `json:"provider_name"`
	CreatedAt    time.Time `json:"created_at"`
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
		err := scanMyOrderRow(tx.QueryRow(ctx, myOrderSelectSQL+`
			 WHERE o.tenant_id = $1 AND o.id = $2::uuid AND o.user_id = $3::uuid`,
			tenantID, orderID, userID), &out.MyOrderRow)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT c.code,
			       CASE WHEN o.status = 'fulfilled' THEN s.current_period_end END
			  FROM orders o
			  LEFT JOIN coupons c       ON c.tenant_id = o.tenant_id AND c.id = o.coupon_id
			  LEFT JOIN subscriptions s ON s.tenant_id = o.tenant_id AND s.id = o.subscription_id
			 WHERE o.tenant_id = $1 AND o.id = $2::uuid`,
			tenantID, orderID).Scan(&out.CouponCode, &out.SubscriptionPeriodEnd); err != nil {
			return err
		}

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
			SELECT p.status, p.amount, p.currency::text, p.method, pp.display_name, p.created_at
			  FROM payments p
			  JOIN payment_providers pp ON pp.tenant_id = p.tenant_id AND pp.id = p.provider_id
			 WHERE p.tenant_id = $1 AND p.order_id = $2::uuid
			 ORDER BY p.created_at`, tenantID, orderID)
		if err != nil {
			return err
		}
		defer payRows.Close()
		out.Payments = []MyOrderPayment{}
		for payRows.Next() {
			var p MyOrderPayment
			if err := payRows.Scan(&p.Status, &p.Amount, &p.Currency,
				&p.Method, &p.ProviderName, &p.CreatedAt); err != nil {
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

// ParseOrderStatuses 解析订单状态筛选：逗号分隔、按枚举白名单精确匹配、
// 去重；空串返回 nil（不筛选）。门户与后台的订单列表共用这一个口径——
// 以前后台把原值当 LIKE 模式，% 与 _ 都是通配符。
func ParseOrderStatuses(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		st := strings.TrimSpace(part)
		if st == "" {
			continue
		}
		if !isKnownOrderStatus(st) {
			return nil, httpx.New(httpx.CodeBadRequest, "不支持的订单状态")
		}
		if !seen[st] {
			seen[st] = true
			out = append(out, st)
		}
	}
	return out, nil
}

func isKnownOrderStatus(s string) bool {
	switch s {
	case "draft", "pending_payment", "processing", "paid", "fulfilled",
		"cancelled", "expired", "partially_refunded", "refunded":
		return true
	}
	return false
}
