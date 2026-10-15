package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/service"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// created 是建单桩的固定结果：总额 25.00、折扣 5.00、余额 10.00、应付 15.00。
func created() service.Created {
	return service.Created{
		OrderID:        uuid.New(),
		OrderNo:        "AO20261012-ABCDEFGH",
		Currency:       money.CNY,
		Total:          money.FromCents(2500),
		Discount:       money.FromCents(500),
		BalanceApplied: money.FromCents(1000),
		Payable:        money.FromCents(1500),
		Status:         service.StatusPending,
	}
}

// 新购：201，金额是整数分；请求里的分换成元、expect 与 as_of 一起交给 service；挂 checkout 开关与 order_create 幂等。
func TestCreateOrderShape(t *testing.T) {
	planID, priceID := uuid.New(), uuid.New()
	var got service.NewOrderInput
	c := created()
	f := &fakeSvc{createNew: func(in service.NewOrderInput) (service.Created, error) { got = in; return c, nil }}
	rec := &kitRec{}
	body := `{"plan_id":"` + planID.String() + `","price_id":"` + priceID.String() + `","use_balance":1000,"coupon_code":"SAVE5",` +
		`"as_of":"2026-10-12T08:00:00Z","expect":{"total":2500,"balance_applied":1000,"payable":1500},"new_copy":true,"label":"家里"}`
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/orders", body)
	mustStatus(t, w, http.StatusCreated)
	m := obj(t, w)
	if m["order_id"] != c.OrderID.String() || m["order_no"] != c.OrderNo || m["status"] != "pending_payment" || m["currency"] != "CNY" {
		t.Fatalf("响应 %v", m)
	}
	if num(t, m, "total_amount") != 2500 || num(t, m, "discount_amount") != 500 || num(t, m, "balance_applied") != 1000 || num(t, m, "payable_amount") != 1500 {
		t.Fatalf("金额应是整数分: %v", m)
	}
	if got.PlanID != planID || got.PriceID != priceID || !got.NewCopy || got.Label != "家里" || got.CouponCode != "SAVE5" {
		t.Fatalf("交给 service 的入参 %+v", got)
	}
	if !got.UseBalance.Equal(money.FromCents(1000)) || got.Expect == nil || !got.Expect.Payable.Equal(money.FromCents(1500)) ||
		!got.Expect.AsOf.Equal(time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("checkout 入参 %+v expect=%+v", got.Checkout, got.Expect)
	}
	if got.Reply == nil {
		t.Fatal("没把幂等重放的响应构造交给 service")
	}
	mustContain(t, rec.scopes, ScopeOrderCreate)
	mustContain(t, rec.switches, SwitchCheckout)
}

// 新购的 422：缺 plan_id、price_id；id 格式不对；未知字段 400。
func TestCreateOrderValidation(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, false, nil)
	b := mustErr(t, do(e, http.MethodPost, "/v1/orders", `{}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["plan_id"] != "必填" || b.Error.Fields["price_id"] != "必填" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	b = mustErr(t, do(e, http.MethodPost, "/v1/orders", `{"plan_id":"x","price_id":"`+uuid.NewString()+`"}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["plan_id"] != "格式不正确" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	mustErr(t, do(e, http.MethodPost, "/v1/orders", `{"plan_id":"`+uuid.NewString()+`","price_id":"`+uuid.NewString()+`","bogus":1}`),
		http.StatusBadRequest, string(errs.BadRequest))
}

// 新购的业务错误：409 quote_changed；order_pending 带 fields.order_id。
func TestCreateOrderBusinessErrors(t *testing.T) {
	pendingID := uuid.NewString()
	cases := []struct {
		err  error
		code string
	}{
		{service.ErrQuoteChanged, string(errs.QuoteChanged)},
		{func() error {
			e := errs.Newf(errs.OrderPending, "你有一张还没付款的订单")
			e.Fields = map[string]string{"order_id": pendingID}
			return e
		}(), string(errs.OrderPending)},
	}
	for _, tc := range cases {
		f := &fakeSvc{createNew: func(service.NewOrderInput) (service.Created, error) { return service.Created{}, tc.err }}
		w := do(newEngine(f, false, nil), http.MethodPost, "/v1/orders", `{"plan_id":"`+uuid.NewString()+`","price_id":"`+uuid.NewString()+`"}`)
		b := mustErr(t, w, http.StatusConflict, tc.code)
		if tc.code == string(errs.OrderPending) && b.Error.Fields["order_id"] != pendingID {
			t.Fatalf("order_pending 应带 fields.order_id，得到 %v", b.Error.Fields)
		}
	}
}

// 续费：201，订阅 id 来自路径、price_id 可空；路径 id 格式不对 404。
func TestCreateRenewal(t *testing.T) {
	subID := uuid.New()
	var got service.RenewalOrderInput
	f := &fakeSvc{createRenewal: func(in service.RenewalOrderInput) (service.Created, error) { got = in; return created(), nil }}
	rec := &kitRec{}
	e := newEngine(f, false, rec)
	w := do(e, http.MethodPost, "/v1/me/subscriptions/"+subID.String()+"/renew", `{}`)
	mustStatus(t, w, http.StatusCreated)
	if got.SubscriptionID != subID || got.PriceID != nil || got.Reply == nil {
		t.Fatalf("入参 %+v", got)
	}
	mustContain(t, rec.scopes, ScopeRenewalCreate)
	mustErr(t, do(e, http.MethodPost, "/v1/me/subscriptions/not-a-uuid/renew", `{}`), http.StatusNotFound, string(errs.NotFound))
}

// 流量包：201；缺目标订阅 422（文案「请选择加到哪一份」）。
func TestCreatePackOrder(t *testing.T) {
	packID, subID := uuid.New(), uuid.New()
	var got service.PackOrderInput
	f := &fakeSvc{createPack: func(in service.PackOrderInput) (service.Created, error) { got = in; return created(), nil }}
	rec := &kitRec{}
	e := newEngine(f, false, rec)
	w := do(e, http.MethodPost, "/v1/me/traffic-pack-orders", `{"pack_id":"`+packID.String()+`","subscription_id":"`+subID.String()+`"}`)
	mustStatus(t, w, http.StatusCreated)
	if got.PackID != packID || got.SubscriptionID != subID {
		t.Fatalf("入参 %+v", got)
	}
	mustContain(t, rec.scopes, ScopeOrderCreate)
	b := mustErr(t, do(e, http.MethodPost, "/v1/me/traffic-pack-orders", `{"pack_id":"`+packID.String()+`"}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["subscription_id"] != "请选择加到哪一份" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
}

// 充值回 200（不是 201）；金额是分；币种去空白后交给 service。
func TestCreateTopupIs200(t *testing.T) {
	var got service.TopupInput
	id := uuid.New()
	f := &fakeSvc{createTopup: func(in service.TopupInput) (service.TopupCreated, error) {
		got = in
		return service.TopupCreated{OrderID: id, OrderNo: "AO20261012-TOPUP001", Amount: in.Amount, Currency: money.CNY}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/me/topups", `{"amount":10000,"currency":" CNY "}`)
	mustStatus(t, w, http.StatusOK)
	m := obj(t, w)
	if num(t, m, "amount") != 10000 || m["order_id"] != id.String() || m["currency"] != "CNY" {
		t.Fatalf("响应 %v", m)
	}
	if !got.Amount.Equal(money.FromCents(10000)) || got.Currency != money.CNY || got.Reply == nil {
		t.Fatalf("入参 %+v", got)
	}
	mustContain(t, rec.scopes, ScopeTopupCreate)
}

// 建单与充值的幂等重放响应：建单 201、充值 200，与首发同形状。
func TestReplyStatus(t *testing.T) {
	r, err := createdReply(created())
	if err != nil || r.Status != http.StatusCreated {
		t.Fatalf("建单重放 %d %v", r.Status, err)
	}
	r, err = topupReply(service.TopupCreated{OrderID: uuid.New(), OrderNo: "AO1", Amount: money.FromCents(100), Currency: money.CNY})
	if err != nil || r.Status != http.StatusOK {
		t.Fatalf("充值重放 %d %v", r.Status, err)
	}
}

// 报价：金额是分；没有剩余价值明细时 credit_detail 为 null；券为空时 coupon / coupon_error 都是 null；有券时 coupon 是对象。
func TestQuoteShape(t *testing.T) {
	planID, priceID := uuid.New(), uuid.New()
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	code := "SAVE5"
	f := &fakeSvc{quote: func(in service.QuoteInput) (service.QuoteResult, error) {
		if in.Action != "new" || in.PlanID == nil || *in.PlanID != planID {
			t.Fatalf("入参 %+v", in)
		}
		return service.QuoteResult{
			AsOf: start, Currency: money.CNY, Balance: money.FromCents(800), MinPayment: money.FromCents(1),
			Quotes: []service.Quote{
				{PlanID: &planID, PriceID: &priceID, Currency: money.CNY, Interval: "month", IntervalCount: 1,
					Subtotal: money.FromCents(3000), Total: money.FromCents(3000),
					WithBalance:    service.BalancePlan{Applied: money.FromCents(800), Payable: money.FromCents(2200)},
					WithoutBalance: service.BalancePlan{Payable: money.FromCents(3000)},
					PeriodStart:    &start, PeriodEnd: &end},
				{PlanID: &planID, PriceID: &priceID, Currency: money.CNY, Interval: "year", IntervalCount: 1,
					Subtotal: money.FromCents(30000), Discount: money.FromCents(500), MemberDiscount: money.FromCents(3000), MemberPercent: 10, Total: money.FromCents(26500), CouponCode: &code},
			},
		}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/me/checkout/quote", `{"action":"new","plan_id":"`+planID.String()+`"}`)
	mustStatus(t, w, http.StatusOK)
	m := obj(t, w)
	if num(t, m, "balance") != 800 || num(t, m, "min_payment") != 1 {
		t.Fatalf("响应 %v", m)
	}
	quotes, ok := m["quotes"].([]any)
	if !ok || len(quotes) != 2 {
		t.Fatalf("quotes %v", m["quotes"])
	}
	q0 := quotes[0].(map[string]any)
	mustNull(t, q0, "credit_detail", "coupon", "coupon_error", "subscription_id", "pack_id", "previous_end")
	if num(t, q0, "subtotal") != 3000 || num(t, sub(t, q0, "with_balance"), "applied") != 800 || num(t, sub(t, q0, "with_balance"), "payable") != 2200 {
		t.Fatalf("第一条报价 %v", q0)
	}
	if num(t, q0, "member_discount") != 0 || num(t, q0, "member_percent") != 0 {
		t.Fatalf("第一条报价会员折扣字段 %v", q0)
	}
	if q0["period_start"] == nil || q0["period_end"] == nil {
		t.Fatalf("周期应有值 %v", q0)
	}
	q1 := quotes[1].(map[string]any)
	if sub(t, q1, "coupon")["code"] != code || num(t, q1, "discount") != 500 {
		t.Fatalf("第二条报价 %v", q1)
	}
	if num(t, q1, "member_discount") != 3000 || num(t, q1, "member_percent") != 10 {
		t.Fatalf("第二条报价会员折扣字段 %v", q1)
	}
	mustContain(t, rec.switches, SwitchCheckout)
	if len(rec.scopes) != 0 {
		t.Fatalf("报价不该挂幂等，得到 %v", rec.scopes)
	}
}

// 发起支付：201；form_fields 为空时是 {}；缺 provider 422；过了付款期限 409 order_lapsed。
func TestPay(t *testing.T) {
	orderID, intentID := uuid.New(), uuid.New()
	f := &fakeSvc{pay: func(in service.PayInput) (paymentsvc.IntentResult, error) {
		if in.OrderID != orderID || in.Provider != "epay" {
			t.Fatalf("入参 %+v", in)
		}
		return paymentsvc.IntentResult{IntentID: intentID, HTTPMethod: "GET", RedirectURL: "https://pay.example.test/x",
			Amount: money.FromCents(1500), Currency: money.CNY}, nil
	}}
	e := newEngine(f, false, nil)
	w := do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/pay", `{"provider":" epay "}`)
	mustStatus(t, w, http.StatusCreated)
	m := obj(t, w)
	if m["intent_id"] != intentID.String() || num(t, m, "amount") != 1500 || m["reused"] != false {
		t.Fatalf("响应 %v", m)
	}
	if ff, ok := m["form_fields"].(map[string]any); !ok || len(ff) != 0 {
		t.Fatalf("form_fields 应为 {}，得到 %#v", m["form_fields"])
	}
	b := mustErr(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/pay", `{}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["provider"] != "必填" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	f.pay = func(service.PayInput) (paymentsvc.IntentResult, error) {
		return paymentsvc.IntentResult{}, service.ErrPaymentLapsed
	}
	mustErr(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/pay", `{"provider":"epay"}`), http.StatusConflict, string(errs.OrderLapsed))
}

// 门户取消：already_terminal 在顶层；cancelled_at、cancel_reason 为空时省略，有值时输出；别人的单 404。
func TestPortalCancel(t *testing.T) {
	orderID := uuid.New()
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	f := &fakeSvc{cancel: func(in service.CancelInput) (service.CancelResult, error) {
		if in.OrderID != orderID || in.Admin {
			t.Fatalf("入参 %+v", in)
		}
		return service.CancelResult{Order: service.OrderRow{ID: orderID, Status: service.StatusCancelled, StateVersion: 2,
			CancelledAt: &at, CancelReason: ptr(service.ReasonUserCancelled)}, AlreadyTerminal: true}, nil
	}}
	e := newEngine(f, false, nil)
	w := do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/cancel", "")
	mustStatus(t, w, http.StatusOK)
	m := obj(t, w)
	if m["already_terminal"] != true || m["status"] != "cancelled" || num(t, m, "state_version") != 2 || m["cancel_reason"] != "user_cancelled" {
		t.Fatalf("响应 %v", m)
	}
	if _, ok := m["cancelled_at"]; !ok {
		t.Fatal("有取消时间时应输出 cancelled_at")
	}

	f.cancel = func(service.CancelInput) (service.CancelResult, error) {
		return service.CancelResult{Order: service.OrderRow{ID: orderID, Status: service.StatusPending, StateVersion: 1}}, nil
	}
	m = obj(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/cancel", ""))
	mustAbsent(t, m, "cancelled_at", "cancel_reason")

	f.cancel = func(service.CancelInput) (service.CancelResult, error) {
		return service.CancelResult{}, service.ErrNotFound
	}
	mustErr(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/cancel", ""), http.StatusNotFound, string(errs.NotFound))
}

// 门户查单：quarantine_kind 为空时省略、有值时输出。
func TestPortalQuery(t *testing.T) {
	orderID := uuid.New()
	res := service.QueryResult{OrderID: orderID, OrderNo: "AO1", ProviderCode: "epay", ChannelStatus: "paid", Reconciled: true, OrderStatus: "fulfilled"}
	f := &fakeSvc{query: func(id uuid.UUID) (service.QueryResult, error) { return res, nil }}
	rec := &kitRec{}
	e := newEngine(f, false, rec)
	m := obj(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/query", ""))
	if m["reconciled"] != true || m["order_status"] != "fulfilled" || m["channel_status"] != "paid" {
		t.Fatalf("响应 %v", m)
	}
	mustAbsent(t, m, "quarantine_kind")
	res.QuarantineKind = ptr("released_order")
	m = obj(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/query", ""))
	if m["quarantine_kind"] != "released_order" {
		t.Fatalf("quarantine_kind %v", m["quarantine_kind"])
	}
	if len(rec.switches) != 0 {
		t.Fatalf("查单不该挂 checkout 开关，得到 %v", rec.switches)
	}
}

// 我的订单：金额是分；可选字段为空时省略；expires_at 恒有；status 逗号分隔。
func TestMyOrdersShape(t *testing.T) {
	created := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	var got service.ListFilter
	f := &fakeSvc{myOrders: func(lf service.ListFilter) (service.MyOrderList, error) {
		got = lf
		return service.MyOrderList{
			Orders: []service.MyOrderView{{
				OrderRow: service.OrderRow{ID: uuid.New(), OrderNo: "AO1", Kind: "topup", Status: "pending_payment", Currency: money.CNY,
					Total: money.FromCents(10000), Payable: money.FromCents(10000), CreatedAt: created, ExpiresAt: created.Add(30 * time.Minute)},
				ItemName: "余额充值", Cancellable: true,
			}},
			Total:  1,
			Counts: service.MyOrderCounts{Open: 1, Paid: 2, Closed: 3},
		}, nil
	}}
	w := do(newEngine(f, false, nil), http.MethodGet, "/v1/orders?status=pending_payment,%20paid&limit=10", "")
	mustStatus(t, w, http.StatusOK)
	if len(got.Statuses) != 2 || got.Statuses[1] != "paid" || got.Limit != 10 {
		t.Fatalf("筛选 %+v", got)
	}
	m := obj(t, w)
	counts := sub(t, m, "counts")
	if num(t, counts, "open") != 1 || num(t, m, "total") != 1 {
		t.Fatalf("计数 %v", m)
	}
	row := m["orders"].([]any)[0].(map[string]any)
	if num(t, row, "total_amount") != 10000 || row["item_name"] != "余额充值" || row["cancellable"] != true {
		t.Fatalf("行 %v", row)
	}
	mustAbsent(t, row, "plan_name", "interval", "interval_count", "paid_at", "cancelled_at", "cancel_reason")
	if row["expires_at"] == nil {
		t.Fatal("expires_at 应恒有")
	}
}

// 订单详情：subscription_id 为空时是 null；coupon_code、subscription_period_end 为空时省略；明细与收款是数组。
func TestMyOrderDetailShape(t *testing.T) {
	orderID := uuid.New()
	f := &fakeSvc{myOrder: func(id uuid.UUID) (service.MyOrderDetail, error) {
		if id != orderID {
			return service.MyOrderDetail{}, service.ErrNotFound
		}
		return service.MyOrderDetail{
			Order: service.MyOrderView{OrderRow: service.OrderRow{ID: orderID, Kind: "new", Status: "pending_payment", Currency: money.CNY,
				Total: money.FromCents(1000), Payable: money.FromCents(1000)}, ItemName: "月付套餐", PlanName: ptr("月付套餐"),
				Interval: ptr("month"), IntervalCount: ptr(1)},
			Items: []service.ItemRow{{Name: "月付套餐", Quantity: 1, UnitAmount: money.FromCents(1000), LineAmount: money.FromCents(1000)}},
			Payments: []service.PaymentLine{{Status: "succeeded", Amount: money.FromCents(1000), Currency: money.CNY,
				ProviderName: "易支付"}},
		}, nil
	}}
	e := newEngine(f, false, nil)
	w := do(e, http.MethodGet, "/v1/orders/"+orderID.String(), "")
	mustStatus(t, w, http.StatusOK)
	o := sub(t, obj(t, w), "order")
	mustNull(t, o, "subscription_id")
	mustAbsent(t, o, "coupon_code", "subscription_period_end")
	if o["plan_name"] != "月付套餐" || o["interval"] != "month" || num(t, o, "interval_count") != 1 {
		t.Fatalf("套餐字段 %v", o)
	}
	items := o["items"].([]any)
	if len(items) != 1 || num(t, items[0].(map[string]any), "unit_amount") != 1000 {
		t.Fatalf("items %v", o["items"])
	}
	pay := o["payments"].([]any)[0].(map[string]any)
	mustAbsent(t, pay, "method")
	if pay["provider_name"] != "易支付" || num(t, pay, "amount") != 1000 {
		t.Fatalf("payments %v", pay)
	}
	mustErr(t, do(e, http.MethodGet, "/v1/orders/"+uuid.NewString(), ""), http.StatusNotFound, string(errs.NotFound))
	mustErr(t, do(e, http.MethodGet, "/v1/orders/bad-id", ""), http.StatusNotFound, string(errs.NotFound))
}
