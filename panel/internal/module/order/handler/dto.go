package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/order/service"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 金额一律整数分；JSON 形状对照旧接口与前端 zod（portal/screens/common/orders.ts、quote-schema.ts）。

// cents 把元换成分，记下第一处换算错误（金额超出 2 位小数是程序错误，对外 500）。
type cents struct{ err error }

func (c *cents) of(d decimal.Decimal) int64 {
	v, err := money.ToCents(d)
	if err != nil && c.err == nil {
		c.err = errs.Wrap(errs.Internal, err)
	}
	return v
}

// ---------------------------------------------------------------------------
// 请求解析的小工具
// ---------------------------------------------------------------------------

// parseID 解析请求体里的 id：空串回「必填」，格式不对回「格式不正确」（都是 422）。
func parseID(field, raw string) (uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.Nil, errs.Invalid(map[string]string{field: "必填"})
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, errs.Invalid(map[string]string{field: "格式不正确"})
	}
	return id, nil
}

// optID 解析可空的 id：空串为 nil。
func optID(field, raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := parseID(field, raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// rejectNUL 拒收含 NUL 的字符串（PG 文本列存不了）。
func rejectNUL(fields map[string]string) error {
	for k, v := range fields {
		if strings.ContainsRune(v, 0) {
			return errs.Invalid(map[string]string{k: "不能包含非法字符"})
		}
	}
	return nil
}

func idStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func idOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func intOrZero(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// ---------------------------------------------------------------------------
// 门户：建单
// ---------------------------------------------------------------------------

// expectReq 是确认下单时带回的报价金额（分）。
type expectReq struct {
	Total          int64 `json:"total"`
	BalanceApplied int64 `json:"balance_applied"`
	Payable        int64 `json:"payable"`
}

// checkoutReq 是三种建单请求共有的部分（旧 quoteConfirmReq 加余额与优惠码）。
type checkoutReq struct {
	UseBalance int64      `json:"use_balance"`
	CouponCode string     `json:"coupon_code"`
	AsOf       *time.Time `json:"as_of,omitempty"`
	Expect     *expectReq `json:"expect,omitempty"`
}

// checkout 转成 service 的 Checkout；没带 expect 时 as_of 也不用（旧代码同口径）。
func (r checkoutReq) checkout() service.Checkout {
	out := service.Checkout{
		UseBalance: money.FromCents(r.UseBalance),
		CouponCode: r.CouponCode,
	}
	if r.Expect != nil {
		e := &service.Expect{
			Total:          money.FromCents(r.Expect.Total),
			BalanceApplied: money.FromCents(r.Expect.BalanceApplied),
			Payable:        money.FromCents(r.Expect.Payable),
		}
		if r.AsOf != nil {
			e.AsOf = *r.AsOf
		}
		out.Expect = e
	}
	return out
}

// createOrderRequest 是 POST /orders（新购）。
type createOrderRequest struct {
	PlanID  string `json:"plan_id"`
	PriceID string `json:"price_id"`
	NewCopy bool   `json:"new_copy"`
	Label   string `json:"label"`
	checkoutReq
}

// createRenewalRequest 是 POST /me/subscriptions/:id/renew；price_id 为空续当前价格档。
type createRenewalRequest struct {
	PriceID string `json:"price_id"`
	checkoutReq
}

// createPackOrderRequest 是 POST /me/traffic-pack-orders。
type createPackOrderRequest struct {
	PackID         string `json:"pack_id"`
	SubscriptionID string `json:"subscription_id"`
	checkoutReq
}

// createTopupRequest 是 POST /me/topups：金额是分，币种为空取 CNY。
type createTopupRequest struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// createPlanChangeRequest 是 POST /me/subscriptions/:id/change-plan：换成 plan_id 的 price_id 档。
type createPlanChangeRequest struct {
	PlanID  string `json:"plan_id"`
	PriceID string `json:"price_id"`
	checkoutReq
}

// orderCreatedResponse 是建单的 201 响应（旧 billing.CreateOrderOutput）。discount_amount 照旧含免掉的零头（旧 waiveIntoDiscount），
// 前端按「小计 − 折扣 − 剩余价值 = 总额」核对；库里零头单独记在 waived_amount。换套餐单另有 proration_credit 与
// balance_refund 两项（旧 PlanChangeOrderOutput），其余种类不出现。
type orderCreatedResponse struct {
	DiscountAmount  int64  `json:"discount_amount"`
	OrderID         string `json:"order_id"`
	OrderNo         string `json:"order_no"`
	Currency        string `json:"currency"`
	TotalAmount     int64  `json:"total_amount"`
	BalanceApplied  int64  `json:"balance_applied"`
	PayableAmount   int64  `json:"payable_amount"`
	Status          string `json:"status"`
	ProrationCredit *int64 `json:"proration_credit,omitempty"`
	BalanceRefund   *int64 `json:"balance_refund,omitempty"`
}

// orderCreatedFrom 是建单首发与幂等重放共用的唯一转换。
func orderCreatedFrom(c service.Created) (orderCreatedResponse, error) {
	var cc cents
	out := orderCreatedResponse{
		DiscountAmount: cc.of(c.Discount.Add(c.Waived)),
		OrderID:        c.OrderID.String(),
		OrderNo:        c.OrderNo,
		Currency:       string(c.Currency),
		TotalAmount:    cc.of(c.Total),
		BalanceApplied: cc.of(c.BalanceApplied),
		PayableAmount:  cc.of(c.Payable),
		Status:         c.Status,
	}
	if c.Change != nil {
		credit, refund := cc.of(c.Change.ProrationCredit), cc.of(c.Change.BalanceRefund)
		out.ProrationCredit, out.BalanceRefund = &credit, &refund
	}
	return out, cc.err
}

// createdReply 是交给 service 的幂等重放响应：与首发同一个转换、同为 201。
func createdReply(c service.Created) (idemsvc.Response, error) {
	out, err := orderCreatedFrom(c)
	if err != nil {
		return idemsvc.Response{}, err
	}
	return idemsvc.JSON(http.StatusCreated, out)
}

// topupCreatedResponse 是充值建单的 200 响应（旧 billing.CreateTopupOutput）。
type topupCreatedResponse struct {
	OrderID  string `json:"order_id"`
	OrderNo  string `json:"order_no"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// topupCreatedFrom 是充值首发与幂等重放共用的唯一转换。
func topupCreatedFrom(t service.TopupCreated) (topupCreatedResponse, error) {
	var cc cents
	out := topupCreatedResponse{
		OrderID:  t.OrderID.String(),
		OrderNo:  t.OrderNo,
		Amount:   cc.of(t.Amount),
		Currency: string(t.Currency),
	}
	return out, cc.err
}

// topupReply 是充值的幂等重放响应（旧代码 200）。
func topupReply(t service.TopupCreated) (idemsvc.Response, error) {
	out, err := topupCreatedFrom(t)
	if err != nil {
		return idemsvc.Response{}, err
	}
	return idemsvc.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// 门户：报价
// ---------------------------------------------------------------------------

// quoteRequest 是 POST /me/checkout/quote。
type quoteRequest struct {
	Action         string `json:"action"`
	SubscriptionID string `json:"subscription_id"`
	PlanID         string `json:"plan_id"`
	PackID         string `json:"pack_id"`
	CouponCode     string `json:"coupon_code"`
	NewCopy        bool   `json:"new_copy"`
}

// balanceSplitResponse 是一种付法（旧 purchase.Balance）。
type balanceSplitResponse struct {
	Applied      int64 `json:"applied"`
	Payable      int64 `json:"payable"`
	Kept         int64 `json:"kept"`
	Forced       bool  `json:"forced"`
	BelowMinimum bool  `json:"below_minimum"`
	SmallDue     bool  `json:"small_due"`
	Waived       int64 `json:"waived"`
}

// quoteCouponResponse 是报价里生效的优惠码。
type quoteCouponResponse struct {
	Code string `json:"code"`
}

// creditDetailResponse 是换套餐剩余价值的明细（只在 action=change 的报价行上非 null）。
type creditDetailResponse struct {
	Paid         int64 `json:"paid"`
	DaysLeft     int   `json:"days_left"`
	DaysTotal    int   `json:"days_total"`
	TrafficLeft  int64 `json:"traffic_left"`
	TrafficTotal int64 `json:"traffic_total"`
	RatioPPM     int64 `json:"ratio_ppm"`
}

// quoteRowResponse 是一条报价。
type quoteRowResponse struct {
	SubscriptionID *string               `json:"subscription_id"`
	PlanID         *string               `json:"plan_id"`
	PackID         *string               `json:"pack_id"`
	PriceID        *string               `json:"price_id"`
	Interval       string                `json:"interval"`
	IntervalCount  int                   `json:"interval_count"`
	Subtotal       int64                 `json:"subtotal"`
	Discount       int64                 `json:"discount"`
	Coupon         *quoteCouponResponse  `json:"coupon"`
	CouponError    *string               `json:"coupon_error"`
	Credit         int64                 `json:"credit"`
	CreditDetail   *creditDetailResponse `json:"credit_detail"`
	Total          int64                 `json:"total"`
	Refund         int64                 `json:"refund"`
	WithBalance    balanceSplitResponse  `json:"with_balance"`
	WithoutBalance balanceSplitResponse  `json:"without_balance"`
	PeriodStart    *time.Time            `json:"period_start"`
	PeriodEnd      *time.Time            `json:"period_end"`
	PreviousEnd    *time.Time            `json:"previous_end"`
}

// quoteResponse 是报价响应（旧 billing.QuoteOutput）。
type quoteResponse struct {
	AsOf       time.Time          `json:"as_of"`
	Currency   string             `json:"currency"`
	Balance    int64              `json:"balance"`
	MinPayment int64              `json:"min_payment"`
	Quotes     []quoteRowResponse `json:"quotes"`
}

func balanceSplitFrom(cc *cents, b service.BalancePlan) balanceSplitResponse {
	return balanceSplitResponse{
		Applied:      cc.of(b.Applied),
		Payable:      cc.of(b.Payable),
		Kept:         cc.of(b.Kept),
		Forced:       b.Forced,
		BelowMinimum: b.BelowMinimum,
		SmallDue:     b.SmallDue,
		Waived:       cc.of(b.Waived),
	}
}

func quoteFrom(q service.QuoteResult) (quoteResponse, error) {
	var cc cents
	out := quoteResponse{
		AsOf:       q.AsOf,
		Currency:   string(q.Currency),
		Balance:    cc.of(q.Balance),
		MinPayment: cc.of(q.MinPayment),
		Quotes:     make([]quoteRowResponse, 0, len(q.Quotes)),
	}
	for _, r := range q.Quotes {
		row := quoteRowResponse{
			SubscriptionID: idStr(r.SubscriptionID),
			PlanID:         idStr(r.PlanID),
			PackID:         idStr(r.PackID),
			PriceID:        idStr(r.PriceID),
			Interval:       r.Interval,
			IntervalCount:  r.IntervalCount,
			Subtotal:       cc.of(r.Subtotal),
			Discount:       cc.of(r.Discount),
			CouponError:    r.CouponError,
			Credit:         cc.of(r.Credit),
			Total:          cc.of(r.Total),
			Refund:         cc.of(r.Refund),
			WithBalance:    balanceSplitFrom(&cc, r.WithBalance),
			WithoutBalance: balanceSplitFrom(&cc, r.WithoutBalance),
			PeriodStart:    r.PeriodStart,
			PeriodEnd:      r.PeriodEnd,
			PreviousEnd:    r.PreviousEnd,
		}
		if r.CouponCode != nil {
			row.Coupon = &quoteCouponResponse{Code: *r.CouponCode}
		}
		if d := r.CreditDetail; d != nil {
			row.CreditDetail = &creditDetailResponse{
				Paid:         cc.of(d.Paid),
				DaysLeft:     d.DaysLeft,
				DaysTotal:    d.DaysTotal,
				TrafficLeft:  d.TrafficLeft,
				TrafficTotal: d.TrafficTotal,
				RatioPPM:     d.RatioPPM,
			}
		}
		out.Quotes = append(out.Quotes, row)
	}
	return out, cc.err
}

// ---------------------------------------------------------------------------
// 门户：支付、取消、查单
// ---------------------------------------------------------------------------

// payRequest 是 POST /orders/:id/pay。
type payRequest struct {
	Provider  string `json:"provider"`
	Method    string `json:"method"`
	ReturnURL string `json:"return_url"`
}

// payResponse 是收银台跳转信息（201）。
type payResponse struct {
	IntentID    string            `json:"intent_id"`
	HTTPMethod  string            `json:"http_method"`
	RedirectURL string            `json:"redirect_url"`
	FormFields  map[string]string `json:"form_fields"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Reused      bool              `json:"reused"`
}

func payFrom(r paymentsvc.IntentResult) (payResponse, error) {
	var cc cents
	fields := r.FormFields
	if fields == nil {
		fields = map[string]string{}
	}
	out := payResponse{
		IntentID:    r.IntentID.String(),
		HTTPMethod:  r.HTTPMethod,
		RedirectURL: r.RedirectURL,
		FormFields:  fields,
		Amount:      cc.of(r.Amount),
		Currency:    string(r.Currency),
		Reused:      r.Reused,
	}
	return out, cc.err
}

// releasedOrderResponse 是取消的结果（旧 billing.ReleaseOrderOutput；cancelled_at、cancel_reason 带 omitempty）。
type releasedOrderResponse struct {
	OrderID         string     `json:"order_id"`
	Status          string     `json:"status"`
	StateVersion    int64      `json:"state_version"`
	CancelledAt     *time.Time `json:"cancelled_at,omitempty"`
	CancelReason    *string    `json:"cancel_reason,omitempty"`
	AlreadyTerminal bool       `json:"already_terminal"`
}

func releasedFrom(r service.CancelResult) releasedOrderResponse {
	return releasedOrderResponse{
		OrderID:         r.Order.ID.String(),
		Status:          r.Order.Status,
		StateVersion:    r.Order.StateVersion,
		CancelledAt:     r.Order.CancelledAt,
		CancelReason:    r.Order.CancelReason,
		AlreadyTerminal: r.AlreadyTerminal,
	}
}

// queryResponse 是主动查单的结果（旧 billing.OrderPaymentQuery；quarantine_kind 带 omitempty）。
type queryResponse struct {
	OrderID         string `json:"order_id"`
	OrderNo         string `json:"order_no"`
	ProviderCode    string `json:"provider_code"`
	ChannelStatus   string `json:"channel_status"`
	Reconciled      bool   `json:"reconciled"`
	AlreadyRecorded bool   `json:"already_recorded"`
	QuarantineKind  string `json:"quarantine_kind,omitempty"`
	OrderStatus     string `json:"order_status"`
}

func queryFrom(q service.QueryResult) queryResponse {
	return queryResponse{
		OrderID:         q.OrderID.String(),
		OrderNo:         q.OrderNo,
		ProviderCode:    q.ProviderCode,
		ChannelStatus:   q.ChannelStatus,
		Reconciled:      q.Reconciled,
		AlreadyRecorded: q.AlreadyRecorded,
		QuarantineKind:  strOrEmpty(q.QuarantineKind),
		OrderStatus:     q.OrderStatus,
	}
}

// ---------------------------------------------------------------------------
// 门户：我的订单
// ---------------------------------------------------------------------------

// myOrderRowResponse 是「我的订单」的一行（旧 billing.MyOrderRow）：前端 zod 里 optional 的字段一律 omitempty。
type myOrderRowResponse struct {
	ID               string     `json:"id"`
	OrderNo          string     `json:"order_no"`
	Kind             string     `json:"kind"`
	Status           string     `json:"status"`
	Currency         string     `json:"currency"`
	TotalAmount      int64      `json:"total_amount"`
	DiscountAmount   int64      `json:"discount_amount"`
	BalanceApplied   int64      `json:"balance_applied"`
	PayableAmount    int64      `json:"payable_amount"`
	PaidAmount       int64      `json:"paid_amount"`
	RefundedAmount   int64      `json:"refunded_amount"`
	PlanName         string     `json:"plan_name,omitempty"`
	Interval         string     `json:"interval,omitempty"`
	IntervalCount    int        `json:"interval_count,omitempty"`
	ItemName         string     `json:"item_name"`
	Cancellable      bool       `json:"cancellable"`
	HasPaymentIntent bool       `json:"has_payment_intent"`
	CreatedAt        time.Time  `json:"created_at"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
	CancelledAt      *time.Time `json:"cancelled_at,omitempty"`
	CancelReason     *string    `json:"cancel_reason,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

// myOrderCountsResponse 是分组计数。
type myOrderCountsResponse struct {
	Open     int64 `json:"open"`
	Paid     int64 `json:"paid"`
	Closed   int64 `json:"closed"`
	Refunded int64 `json:"refunded"`
}

// myOrdersResponse 是 GET /orders。
type myOrdersResponse struct {
	Orders []myOrderRowResponse  `json:"orders"`
	Total  int64                 `json:"total"`
	Counts myOrderCountsResponse `json:"counts"`
}

// myOrderItemResponse 是详情里的一条订单行。
type myOrderItemResponse struct {
	Name       string `json:"name"`
	Quantity   int    `json:"quantity"`
	UnitAmount int64  `json:"unit_amount"`
	LineAmount int64  `json:"line_amount"`
}

// myOrderPaymentResponse 是详情里的一笔收款（不回渠道单号）。
type myOrderPaymentResponse struct {
	Status       string    `json:"status"`
	Amount       int64     `json:"amount"`
	Currency     string    `json:"currency"`
	Method       *string   `json:"method,omitempty"`
	ProviderName string    `json:"provider_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// myOrderDetailBody 是详情的 order 对象：列表行加明细。
type myOrderDetailBody struct {
	myOrderRowResponse
	CouponCode            *string                  `json:"coupon_code,omitempty"`
	SubscriptionPeriodEnd *time.Time               `json:"subscription_period_end,omitempty"`
	SubscriptionID        *string                  `json:"subscription_id"`
	Items                 []myOrderItemResponse    `json:"items"`
	Payments              []myOrderPaymentResponse `json:"payments"`
}

// myOrderDetailResponse 是 GET /orders/:id。
type myOrderDetailResponse struct {
	Order myOrderDetailBody `json:"order"`
}

func myOrderRowFrom(cc *cents, v service.MyOrderView) myOrderRowResponse {
	expires := v.ExpiresAt
	return myOrderRowResponse{
		ID:               v.ID.String(),
		OrderNo:          v.OrderNo,
		Kind:             v.Kind,
		Status:           v.Status,
		Currency:         string(v.Currency),
		TotalAmount:      cc.of(v.Total),
		DiscountAmount:   cc.of(v.Discount.Add(v.Waived)),
		BalanceApplied:   cc.of(v.BalanceApplied),
		PayableAmount:    cc.of(v.Payable),
		PaidAmount:       cc.of(v.Paid),
		RefundedAmount:   0,
		PlanName:         strOrEmpty(v.PlanName),
		Interval:         strOrEmpty(v.Interval),
		IntervalCount:    intOrZero(v.IntervalCount),
		ItemName:         v.ItemName,
		Cancellable:      v.Cancellable,
		HasPaymentIntent: v.HasPaymentIntent,
		CreatedAt:        v.CreatedAt,
		PaidAt:           v.PaidAt,
		CancelledAt:      v.CancelledAt,
		CancelReason:     v.CancelReason,
		ExpiresAt:        &expires,
	}
}

func myOrdersFrom(l service.MyOrderList) (myOrdersResponse, error) {
	var cc cents
	out := myOrdersResponse{
		Orders: make([]myOrderRowResponse, 0, len(l.Orders)),
		Total:  l.Total,
		Counts: myOrderCountsResponse{
			Open:     l.Counts.Open,
			Paid:     l.Counts.Paid,
			Closed:   l.Counts.Closed,
			Refunded: l.Counts.Refunded,
		},
	}
	for _, v := range l.Orders {
		out.Orders = append(out.Orders, myOrderRowFrom(&cc, v))
	}
	return out, cc.err
}

func myOrderDetailFrom(d service.MyOrderDetail) (myOrderDetailResponse, error) {
	var cc cents
	body := myOrderDetailBody{
		myOrderRowResponse:    myOrderRowFrom(&cc, d.Order),
		CouponCode:            d.CouponCode,
		SubscriptionPeriodEnd: d.SubscriptionPeriodEnd,
		SubscriptionID:        idStr(d.Order.SubscriptionID),
		Items:                 make([]myOrderItemResponse, 0, len(d.Items)),
		Payments:              make([]myOrderPaymentResponse, 0, len(d.Payments)),
	}
	for _, it := range d.Items {
		body.Items = append(body.Items, myOrderItemResponse{
			Name:       it.Name,
			Quantity:   it.Quantity,
			UnitAmount: cc.of(it.UnitAmount),
			LineAmount: cc.of(it.LineAmount),
		})
	}
	for _, p := range d.Payments {
		body.Payments = append(body.Payments, myOrderPaymentResponse{
			Status:       p.Status,
			Amount:       cc.of(p.Amount),
			Currency:     string(p.Currency),
			Method:       p.Method,
			ProviderName: p.ProviderName,
			CreatedAt:    p.CreatedAt,
		})
	}
	return myOrderDetailResponse{Order: body}, cc.err
}
