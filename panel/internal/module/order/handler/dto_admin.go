package handler

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/order/service"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 后台订单接口的 DTO；JSON 形状对照前端 admin/screens/billing/schemas.ts。

// parseDay 解析后台筛选的日期：先按 YYYY-MM-DD，再按 RFC3339；end 为真且是日期时取次日零点（左闭右开）。
func parseDay(field, raw string, end bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errs.Invalid(map[string]string{field: "日期格式不正确"})
	}
	return &t, nil
}

// splitStatuses 把逗号分隔的状态拆开（校验在 service）。
func splitStatuses(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// adminCancelRequest 是 POST /orders/:id/cancel（后台）。
type adminCancelRequest struct {
	ExpectedStateVersion *int64 `json:"expected_state_version"`
	Reason               string `json:"reason"`
}

// adminCancelResponse 是后台取消的结果。
type adminCancelResponse struct {
	AlreadyTerminal bool                  `json:"already_terminal"`
	Order           releasedOrderResponse `json:"order"`
}

// markPaidRequest 是 POST /orders/:id/mark-paid。
type markPaidRequest struct {
	Reason    string `json:"reason"`
	Reference string `json:"reference"`
}

// markPaidResponse 是标记已支付的结果（没有的 id 为空串）。
type markPaidResponse struct {
	Processed      bool   `json:"processed"`
	AlreadyHandled bool   `json:"already_handled"`
	PaymentID      string `json:"payment_id"`
	SubscriptionID string `json:"subscription_id"`
	LedgerTxnID    string `json:"ledger_txn_id"`
}

func markPaidFrom(r service.MarkPaidResult) markPaidResponse {
	return markPaidResponse{
		Processed:      r.Processed,
		AlreadyHandled: r.AlreadyHandled,
		PaymentID:      idOrEmpty(r.PaymentID),
		SubscriptionID: idOrEmpty(r.SubscriptionID),
		LedgerTxnID:    idOrEmpty(r.LedgerTxnID),
	}
}

// adminOrderRowResponse 是后台订单列表的一行。
type adminOrderRowResponse struct {
	ID             string     `json:"id"`
	OrderNo        string     `json:"order_no"`
	UserEmail      string     `json:"user_email"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	TotalAmount    int64      `json:"total_amount"`
	PayableAmount  int64      `json:"payable_amount"`
	PaidAmount     int64      `json:"paid_amount"`
	BalanceApplied int64      `json:"balance_applied"`
	CreatedAt      time.Time  `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at"`
	ProviderCode   *string    `json:"provider_code"`
	ProviderName   *string    `json:"provider_name"`
	PlanName       string     `json:"plan_name"`
	Interval       string     `json:"interval"`
	IntervalCount  int        `json:"interval_count"`
	ItemCount      int        `json:"item_count"`
	Manual         bool       `json:"manual"`
}

// adminOrdersResponse 是 GET /orders（后台）。
type adminOrdersResponse struct {
	Orders []adminOrderRowResponse `json:"orders"`
	Total  int64                   `json:"total"`
}

// adminOrderItemResponse 是后台详情里的一条订单行快照。
type adminOrderItemResponse struct {
	ID                   string          `json:"id"`
	PriceID              *string         `json:"price_id"`
	PlanID               *string         `json:"plan_id"`
	PlanVersionID        *string         `json:"plan_version_id"`
	ProductName          string          `json:"product_name"`
	PlanName             *string         `json:"plan_name"`
	PlanVersion          *int            `json:"plan_version"`
	Interval             *string         `json:"interval"`
	IntervalCount        *int            `json:"interval_count"`
	SnapshotEntitlements json.RawMessage `json:"snapshot_entitlements"`
	SnapshotQuotas       json.RawMessage `json:"snapshot_quotas"`
	Quantity             int             `json:"quantity"`
	UnitAmount           int64           `json:"unit_amount"`
	LineAmount           int64           `json:"line_amount"`
	CreatedAt            time.Time       `json:"created_at"`
}

// adminOrderDetailBody 是后台详情的 order 对象：列表行加明细。
type adminOrderDetailBody struct {
	adminOrderRowResponse
	UserID         string                   `json:"user_id"`
	StateVersion   int64                    `json:"state_version"`
	SubtotalAmount int64                    `json:"subtotal_amount"`
	DiscountAmount int64                    `json:"discount_amount"`
	CouponID       *string                  `json:"coupon_id"`
	ManualReason   *string                  `json:"manual_reason"`
	CreatedBy      *string                  `json:"created_by"`
	CreatedByEmail *string                  `json:"created_by_email"`
	SubscriptionID *string                  `json:"subscription_id"`
	ExpiresAt      *time.Time               `json:"expires_at"`
	FulfilledAt    *time.Time               `json:"fulfilled_at"`
	CancelledAt    *time.Time               `json:"cancelled_at"`
	ExpiredAt      *time.Time               `json:"expired_at"`
	CancelReason   *string                  `json:"cancel_reason"`
	UpdatedAt      time.Time                `json:"updated_at"`
	Items          []adminOrderItemResponse `json:"items"`
}

// adminOrderDetailResponse 是 GET /orders/:id（后台）。
type adminOrderDetailResponse struct {
	Order adminOrderDetailBody `json:"order"`
}

func adminOrderRowFrom(cc *cents, v service.AdminOrderView) adminOrderRowResponse {
	return adminOrderRowResponse{
		ID:             v.ID.String(),
		OrderNo:        v.OrderNo,
		UserEmail:      v.UserEmail,
		Kind:           v.Kind,
		Status:         v.Status,
		TotalAmount:    cc.of(v.Total),
		PayableAmount:  cc.of(v.Payable),
		PaidAmount:     cc.of(v.Paid),
		BalanceApplied: cc.of(v.BalanceApplied),
		CreatedAt:      v.CreatedAt,
		PaidAt:         v.PaidAt,
		ProviderCode:   v.ProviderCode,
		ProviderName:   v.ProviderName,
		PlanName:       strOrEmpty(v.PlanName),
		Interval:       strOrEmpty(v.Interval),
		IntervalCount:  intOrZero(v.IntervalCount),
		ItemCount:      v.ItemCount,
		Manual:         v.Manual,
	}
}

func adminOrdersFrom(l service.AdminOrderList) (adminOrdersResponse, error) {
	var cc cents
	out := adminOrdersResponse{Orders: make([]adminOrderRowResponse, 0, len(l.Orders)), Total: l.Total}
	for _, v := range l.Orders {
		out.Orders = append(out.Orders, adminOrderRowFrom(&cc, v))
	}
	return out, cc.err
}

// rawJSON 把快照原文作为 JSON 输出；空串输出空数组。
func rawJSON(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("[]")
	}
	return json.RawMessage(s)
}

func adminOrderDetailFrom(d service.AdminOrderDetail) (adminOrderDetailResponse, error) {
	var cc cents
	o := d.Order
	expires := o.ExpiresAt
	body := adminOrderDetailBody{
		adminOrderRowResponse: adminOrderRowFrom(&cc, o),
		UserID:                o.UserID.String(),
		StateVersion:          o.StateVersion,
		SubtotalAmount:        cc.of(o.Subtotal),
		DiscountAmount:        cc.of(o.Discount.Add(o.Waived)),
		CouponID:              idStr(o.CouponID),
		ManualReason:          o.ManualReason,
		CreatedBy:             idStr(o.CreatedBy),
		CreatedByEmail:        d.CreatedByEmail,
		SubscriptionID:        idStr(o.SubscriptionID),
		ExpiresAt:             &expires,
		FulfilledAt:           o.FulfilledAt,
		CancelledAt:           o.CancelledAt,
		ExpiredAt:             o.ExpiredAt,
		CancelReason:          o.CancelReason,
		UpdatedAt:             o.UpdatedAt,
		Items:                 make([]adminOrderItemResponse, 0, len(d.Items)),
	}
	for _, it := range d.Items {
		item := adminOrderItemResponse{
			ID:                   it.ID.String(),
			PriceID:              idStr(it.PriceID),
			PlanID:               idStr(it.PlanID),
			PlanVersionID:        idStr(it.PlanVersionID),
			ProductName:          it.Name,
			PlanVersion:          it.PlanVersion,
			Interval:             it.Interval,
			IntervalCount:        it.IntervalCount,
			SnapshotEntitlements: rawJSON(it.Entitlements),
			SnapshotQuotas:       rawJSON(it.Quotas),
			Quantity:             it.Quantity,
			UnitAmount:           cc.of(it.UnitAmount),
			LineAmount:           cc.of(it.LineAmount),
			CreatedAt:            it.CreatedAt,
		}
		if it.PlanID != nil {
			name := it.Name
			item.PlanName = &name
		}
		body.Items = append(body.Items, item)
	}
	return adminOrderDetailResponse{Order: body}, cc.err
}

// intentResponse 是订单的一次支付尝试。
type intentResponse struct {
	ID              string     `json:"id"`
	ProviderCode    string     `json:"provider_code"`
	ProviderName    string     `json:"provider_name"`
	Amount          int64      `json:"amount"`
	SurchargeAmount int64      `json:"surcharge_amount"`
	Status          string     `json:"status"`
	ProviderRef     *string    `json:"provider_ref"`
	ExpiresAt       *time.Time `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// paymentResponse 是订单的一笔收款。
type paymentResponse struct {
	ID                string    `json:"id"`
	PaymentIntentID   *string   `json:"payment_intent_id"`
	ProviderCode      string    `json:"provider_code"`
	ProviderName      string    `json:"provider_name"`
	ProviderPaymentID string    `json:"provider_payment_id"`
	Amount            int64     `json:"amount"`
	FeeAmount         int64     `json:"fee_amount"`
	SurchargeAmount   int64     `json:"surcharge_amount"`
	Status            string    `json:"status"`
	Method            *string   `json:"method"`
	PaidAt            time.Time `json:"paid_at"`
}

// orderPaymentsResponse 是 GET /orders/:id/payments：支付尝试与收款。
type orderPaymentsResponse struct {
	PaymentIntents []intentResponse  `json:"payment_intents"`
	Payments       []paymentResponse `json:"payments"`
}

func orderPaymentsFrom(h paymentsvc.OrderHistory) (orderPaymentsResponse, error) {
	var cc cents
	out := orderPaymentsResponse{
		PaymentIntents: make([]intentResponse, 0, len(h.Intents)),
		Payments:       make([]paymentResponse, 0, len(h.Payments)),
	}
	for _, in := range h.Intents {
		out.PaymentIntents = append(out.PaymentIntents, intentResponse{
			ID:              in.ID.String(),
			ProviderCode:    in.ProviderCode,
			ProviderName:    in.ProviderName,
			Amount:          cc.of(in.Amount),
			SurchargeAmount: cc.of(in.Surcharge),
			Status:          string(in.Status),
			ProviderRef:     in.ProviderRef,
			ExpiresAt:       in.ExpiresAt,
			CreatedAt:       in.CreatedAt,
			UpdatedAt:       in.UpdatedAt,
		})
	}
	for _, p := range h.Payments {
		out.Payments = append(out.Payments, paymentResponse{
			ID:                p.ID.String(),
			PaymentIntentID:   idStr(p.IntentID),
			ProviderCode:      p.ProviderCode,
			ProviderName:      p.ProviderName,
			ProviderPaymentID: p.ProviderPaymentID,
			Amount:            cc.of(p.Amount),
			FeeAmount:         cc.of(p.Fee),
			SurchargeAmount:   cc.of(p.Surcharge),
			Status:            p.Status,
			Method:            p.Method,
			PaidAt:            p.PaidAt,
		})
	}
	return out, cc.err
}
