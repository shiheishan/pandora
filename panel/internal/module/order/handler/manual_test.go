package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// changeCreated 是换套餐单的桩结果：新价 30.00，剩余价值 12.34 抵扣，退余额 0。
func changeCreated() service.Created {
	c := created()
	c.Total = money.FromCents(1766)
	c.Discount = money.FromCents(0)
	c.BalanceApplied = money.FromCents(0)
	c.Payable = money.FromCents(1766)
	c.Change = &service.ChangeAmounts{ProrationCredit: money.FromCents(1234), BalanceRefund: money.FromCents(0)}
	return c
}

// 换套餐：201；订阅 id 来自路径，plan_id、price_id 与 checkout 交给 service；挂 checkout 开关与 subscription_change_plan_create 幂等；
// 响应多 proration_credit、balance_refund 两项（为 0 也出现）。
func TestCreatePlanChangeShape(t *testing.T) {
	subID, planID, priceID := uuid.New(), uuid.New(), uuid.New()
	var got service.PlanChangeOrderInput
	f := &fakeSvc{createChange: func(in service.PlanChangeOrderInput) (service.Created, error) { got = in; return changeCreated(), nil }}
	rec := &kitRec{}
	body := `{"plan_id":"` + planID.String() + `","price_id":"` + priceID.String() + `","use_balance":500,"coupon_code":"UP",` +
		`"as_of":"2026-10-12T08:00:00Z","expect":{"total":1766,"balance_applied":0,"payable":1766}}`
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/me/subscriptions/"+subID.String()+"/change-plan", body)
	mustStatus(t, w, http.StatusCreated)
	m := obj(t, w)
	if num(t, m, "total_amount") != 1766 || num(t, m, "proration_credit") != 1234 || num(t, m, "balance_refund") != 0 {
		t.Fatalf("响应 %v", m)
	}
	if got.SubscriptionID != subID || got.PlanID != planID || got.PriceID != priceID || got.CouponCode != "UP" || got.Reply == nil {
		t.Fatalf("入参 %+v", got)
	}
	if !got.UseBalance.Equal(money.FromCents(500)) || got.Expect == nil || !got.Expect.Total.Equal(money.FromCents(1766)) ||
		!got.Expect.AsOf.Equal(time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("checkout 入参 %+v", got.Checkout)
	}
	mustContain(t, rec.scopes, ScopePlanChangeCreate)
	mustContain(t, rec.switches, SwitchCheckout)

	// 重放与首发同形状，同为 201
	r, err := createdReply(changeCreated())
	if err != nil || r.Status != http.StatusCreated {
		t.Fatalf("重放响应 %+v err %v", r, err)
	}
}

// 别的建单不出现 proration_credit、balance_refund。
func TestCreatedOmitsChangeFields(t *testing.T) {
	f := &fakeSvc{createRenewal: func(service.RenewalOrderInput) (service.Created, error) { return created(), nil }}
	w := do(newEngine(f, false, nil), http.MethodPost, "/v1/me/subscriptions/"+uuid.NewString()+"/renew", `{}`)
	mustStatus(t, w, http.StatusCreated)
	mustAbsent(t, obj(t, w), "proration_credit", "balance_refund")
}

// 换套餐的 422、404 与业务错误。
func TestCreatePlanChangeErrors(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, false, nil)
	path := "/v1/me/subscriptions/" + uuid.NewString() + "/change-plan"
	b := mustErr(t, do(e, http.MethodPost, path, `{}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["plan_id"] != "必填" || b.Error.Fields["price_id"] != "必填" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	b = mustErr(t, do(e, http.MethodPost, path, `{"plan_id":"x","price_id":"`+uuid.NewString()+`"}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["plan_id"] != "格式不正确" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	mustErr(t, do(e, http.MethodPost, "/v1/me/subscriptions/x/change-plan", `{}`), http.StatusNotFound, string(errs.NotFound))

	body := `{"plan_id":"` + uuid.NewString() + `","price_id":"` + uuid.NewString() + `"}`
	f.createChange = func(service.PlanChangeOrderInput) (service.Created, error) {
		return service.Created{}, service.ErrOpenChange
	}
	mustErr(t, do(e, http.MethodPost, path, body), http.StatusConflict, string(errs.Conflict))
	f.createChange = func(service.PlanChangeOrderInput) (service.Created, error) {
		return service.Created{}, service.ErrPlanChangeSamePlan
	}
	mustErr(t, do(e, http.MethodPost, path, body), http.StatusConflict, string(errs.Conflict))
	f.createChange = func(service.PlanChangeOrderInput) (service.Created, error) {
		return service.Created{}, service.ErrQuoteChanged
	}
	mustErr(t, do(e, http.MethodPost, path, body), http.StatusConflict, string(errs.QuoteChanged))
}

// 换套餐报价：credit_detail 是对象、金额是分。
func TestQuoteCreditDetail(t *testing.T) {
	subID, planID, priceID := uuid.New(), uuid.New(), uuid.New()
	f := &fakeSvc{quote: func(in service.QuoteInput) (service.QuoteResult, error) {
		if in.Action != service.QuoteChange || in.SubscriptionID == nil || *in.SubscriptionID != subID {
			t.Fatalf("入参 %+v", in)
		}
		return service.QuoteResult{Quotes: []service.Quote{{
			SubscriptionID: &subID, PlanID: &planID, PriceID: &priceID, Interval: "month", IntervalCount: 1,
			Subtotal: money.FromCents(3000), Credit: money.FromCents(1234), Total: money.FromCents(1766),
			CreditDetail: &service.CreditDetail{Paid: money.FromCents(2000), DaysLeft: 19, DaysTotal: 31,
				TrafficLeft: 70, TrafficTotal: 100, RatioPPM: 612903},
		}}}, nil
	}}
	w := do(newEngine(f, false, nil), http.MethodPost, "/v1/me/checkout/quote",
		`{"action":"change","subscription_id":"`+subID.String()+`","plan_id":"`+planID.String()+`"}`)
	mustStatus(t, w, http.StatusOK)
	q := obj(t, w)["quotes"].([]any)[0].(map[string]any)
	d := sub(t, q, "credit_detail")
	if num(t, q, "credit") != 1234 || num(t, d, "paid") != 2000 || num(t, d, "days_left") != 19 || num(t, d, "days_total") != 31 ||
		num(t, d, "traffic_left") != 70 || num(t, d, "traffic_total") != 100 || num(t, d, "ratio_ppm") != 612903 {
		t.Fatalf("credit_detail %v", q)
	}
}

// 人工开单：要重认证、挂 order_create 幂等；target 转成 Choice；201 与门户建单同形状。
func TestCreateManualOrderShape(t *testing.T) {
	userID, planID, priceID, subID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var got service.ManualOrderInput
	f := &fakeSvc{createManual: func(in service.ManualOrderInput) (service.Created, error) {
		got = in
		c := changeCreated()
		c.Status = service.StatusFulfilled
		return c, nil
	}}
	rec := &kitRec{}
	e := newEngine(f, true, rec)
	body := `{"user_id":"` + userID.String() + `","plan_id":"` + planID.String() + `","price_id":"` + priceID.String() +
		`","reason":"客户线下转账补开","settlement":"offline","reference":"BANK-9","target":{"kind":"change","subscription_id":"` + subID.String() + `"}}`
	w := do(e, http.MethodPost, "/v1/orders/manual", body)
	mustStatus(t, w, http.StatusCreated)
	m := obj(t, w)
	if m["status"] != "fulfilled" || num(t, m, "proration_credit") != 1234 {
		t.Fatalf("响应 %v", m)
	}
	if got.UserID != userID || got.PlanID != planID || got.PriceID != priceID || got.Reason != "客户线下转账补开" ||
		got.Settlement != "offline" || got.Reference != "BANK-9" || got.Reply == nil {
		t.Fatalf("入参 %+v", got)
	}
	if got.Target == nil || got.Target.Kind != service.PlacementChange || got.Target.SubscriptionID == nil || *got.Target.SubscriptionID != subID {
		t.Fatalf("target %+v", got.Target)
	}
	if rec.reauth != 1 {
		t.Fatalf("人工开单应要求重认证，记到 %d 次", rec.reauth)
	}
	mustContain(t, rec.scopes, ScopeOrderCreate)

	// 没给 target、kind 为空都是 nil（选项只有一个时由 service 自己选）
	w = do(e, http.MethodPost, "/v1/orders/manual", `{"user_id":"`+userID.String()+`","plan_id":"`+planID.String()+
		`","price_id":"`+priceID.String()+`","reason":"补偿五天的故障","target":{"kind":""}}`)
	mustStatus(t, w, http.StatusCreated)
	if got.Target != nil || got.Settlement != "" {
		t.Fatalf("target %+v settlement %q", got.Target, got.Settlement)
	}
}

// 人工开单的 422 与业务错误：缺 id、id 格式不对、target 的订阅 id 不对；service 的原因、结算方式校验原样透出。
func TestCreateManualOrderErrors(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	b := mustErr(t, do(e, http.MethodPost, "/v1/orders/manual", `{}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["user_id"] != "必填" || b.Error.Fields["plan_id"] != "必填" || b.Error.Fields["price_id"] != "必填" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	ids := `"user_id":"` + uuid.NewString() + `","plan_id":"` + uuid.NewString() + `","price_id":"` + uuid.NewString() + `"`
	b = mustErr(t, do(e, http.MethodPost, "/v1/orders/manual", `{`+ids+`,"target":{"kind":"renew","subscription_id":"x"}}`),
		http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["target"] == "" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	f.createManual = func(service.ManualOrderInput) (service.Created, error) {
		return service.Created{}, errs.Invalid(map[string]string{"reason": "请写清开单原因，5 到 500 个字。这条会进审计，是日后对账的唯一依据"})
	}
	b = mustErr(t, do(e, http.MethodPost, "/v1/orders/manual", `{`+ids+`,"reason":"补偿"}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["reason"] == "" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	mustErr(t, do(e, http.MethodPost, "/v1/orders/manual", `{`+ids+`,"bogus":1}`), http.StatusBadRequest, string(errs.BadRequest))
}

// 开单预览：只读，不要重认证、不挂幂等；空值省略，due、below_minimum 总在。
func TestManualPreviewShape(t *testing.T) {
	for _, tc := range []struct {
		name        string
		optionKey   string
		optionKind  string
		badge       string
		credit      int64
		unavailable string
		belowMin    bool
	}{
		{
			name:        "change",
			optionKey:   "change:",
			optionKind:  service.PlacementChange,
			credit:      2950,
			unavailable: "已有未完结的续费或变更单",
			belowMin:    true,
		},
		{
			name:       "stack",
			optionKey:  "stack:",
			optionKind: service.PlacementStack,
			badge:      service.BadgeSamePlan,
			credit:     0,
			belowMin:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userID, planID, priceID, subID, entry := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			newEnd := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
			var got service.ManualPreviewInput
			optKey := tc.optionKey + subID.String()
			f := &fakeSvc{manualPreview: func(in service.ManualPreviewInput) (service.ManualPreview, error) {
				got = in
				curPlan := uuid.New()
				return service.ManualPreview{
					DefaultKey: optKey,
					MinPayment: money.FromCents(100),
					Options: []service.Placement{
						{Key: "new", Kind: service.PlacementNew, NewPeriodEnd: &newEnd, Due: money.FromCents(3000)},
						{Key: optKey, Kind: tc.optionKind, SubscriptionID: &subID, Expired: tc.optionKind == service.PlacementChange,
							Badge: tc.badge, Label: "家里", PlanID: &curPlan, PlanName: "标准", State: subsvc.StateRevivable, PeriodEnd: &end, NewPeriodEnd: &newEnd,
							Credit: money.FromCents(tc.credit), GiftDaysLost: 3, TrafficUsed: 5, TrafficCap: 10,
							Due: money.FromCents(50), BelowMinimum: tc.belowMin, Unavailable: tc.unavailable},
					},
				}, nil
			}}
			rec := &kitRec{}
			e := newEngine(f, true, rec)
			w := do(e, http.MethodPost, "/v1/orders/manual/preview", `{"user_id":"`+userID.String()+`","plan_id":"`+planID.String()+
				`","price_id":"`+priceID.String()+`","entry_subscription_id":"`+entry.String()+`"}`)
			mustStatus(t, w, http.StatusOK)
			if got.UserID != userID || got.PlanID != planID || got.PriceID == nil || *got.PriceID != priceID ||
				got.EntrySubscriptionID == nil || *got.EntrySubscriptionID != entry {
				t.Fatalf("入参 %+v", got)
			}
			m := obj(t, w)
			if m["default_key"] != optKey || num(t, m, "min_payment") != 100 {
				t.Fatalf("响应 %v", m)
			}
			opts := m["options"].([]any)
			o0, o1 := opts[0].(map[string]any), opts[1].(map[string]any)
			mustAbsent(t, o0, "subscription_id", "expired", "badge", "label", "plan_id", "plan_name", "state", "period_end",
				"credit", "currency", "gift_days_lost", "traffic_used", "traffic_cap", "pack_remaining", "unavailable_reason")
			mustAbsent(t, o1, "currency", "pack_remaining")
			if tc.unavailable != "" {
				if o1["unavailable_reason"] != tc.unavailable {
					t.Fatalf("不可选原因 %v", o1["unavailable_reason"])
				}
			} else {
				mustAbsent(t, o1, "unavailable_reason")
			}
			if o0["key"] != "new" || num(t, o0, "due") != 3000 || o0["below_minimum"] != false || o0["new_period_end"] == nil {
				t.Fatalf("new 选项 %v", o0)
			}
			if o1["key"] != optKey || o1["kind"] != tc.optionKind || o1["subscription_id"] != subID.String() ||
				o1["state"] != "revivable" ||
				num(t, o1, "gift_days_lost") != 3 || num(t, o1, "due") != 50 || o1["below_minimum"] != tc.belowMin ||
				num(t, o1, "traffic_cap") != 10 || o1["plan_name"] != "标准" {
				t.Fatalf("选项 %v", o1)
			}
			// credit 为 0 时省略
			if tc.credit == 0 {
				mustAbsent(t, o1, "credit")
			} else if num(t, o1, "credit") != float64(tc.credit) {
				t.Fatalf("credit %v", o1["credit"])
			}
			if tc.badge != "" && o1["badge"] != tc.badge {
				t.Fatalf("badge 不符: %v", o1["badge"])
			}
			if rec.reauth != 0 || len(rec.scopes) != 0 {
				t.Fatalf("预览不该要求重认证或幂等：reauth=%d scopes=%v", rec.reauth, rec.scopes)
			}
		})
	}

	userID, planID := uuid.New(), uuid.New()
	f := &fakeSvc{manualPreview: func(in service.ManualPreviewInput) (service.ManualPreview, error) {
		return service.ManualPreview{}, nil
	}}
	e := newEngine(f, true, nil)
	// price_id、entry_subscription_id 可空；格式不对 422
	w := do(e, http.MethodPost, "/v1/orders/manual/preview", `{"user_id":"`+userID.String()+`","plan_id":"`+planID.String()+`"}`)
	mustStatus(t, w, http.StatusOK)
	b := mustErr(t, do(e, http.MethodPost, "/v1/orders/manual/preview", `{"user_id":"`+userID.String()+`","plan_id":"`+planID.String()+
		`","price_id":"x"}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["price_id"] != "格式不正确" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	b = mustErr(t, do(e, http.MethodPost, "/v1/orders/manual/preview", `{}`), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["user_id"] != "必填" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
}

// 免掉的零头对外并进 discount_amount：前端「小计 − 折扣 − 剩余价值 = 应付」照旧对得上；建单响应与我的订单列表同口径。
func TestDiscountAmountIncludesWaived(t *testing.T) {
	c := changeCreated()
	c.Discount = money.FromCents(300)
	c.Waived = money.FromCents(50)
	f := &fakeSvc{
		createChange: func(service.PlanChangeOrderInput) (service.Created, error) { return c, nil },
		myOrders: func(service.ListFilter) (service.MyOrderList, error) {
			return service.MyOrderList{Orders: []service.MyOrderView{{OrderRow: service.OrderRow{
				ID: uuid.New(), OrderNo: "AO2", Kind: "upgrade", Status: "fulfilled",
				Subtotal: money.FromCents(1550), Discount: money.FromCents(0), Waived: money.FromCents(50), ProrationCredit: money.FromCents(1500),
			}}}, Total: 1}, nil
		},
	}
	e := newEngine(f, false, nil)
	w := do(e, http.MethodPost, "/v1/me/subscriptions/"+uuid.NewString()+"/change-plan",
		`{"plan_id":"`+uuid.NewString()+`","price_id":"`+uuid.NewString()+`"}`)
	mustStatus(t, w, http.StatusCreated)
	if m := obj(t, w); num(t, m, "discount_amount") != 350 {
		t.Fatalf("建单 discount_amount %v", m["discount_amount"])
	}
	w = do(e, http.MethodGet, "/v1/orders", "")
	mustStatus(t, w, http.StatusOK)
	if row := obj(t, w)["orders"].([]any)[0].(map[string]any); num(t, row, "discount_amount") != 50 || num(t, row, "total_amount") != 0 {
		t.Fatalf("列表行 %v", row)
	}
}
