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

// 后台列表：筛选参数交给 service（to 是日期时取次日零点）；plan_name、interval 恒为字符串；
// paid_at、provider_code、provider_name 为空时是 null；manual、item_count 恒有。
func TestAdminOrdersShape(t *testing.T) {
	userID := uuid.New()
	var got service.ListFilter
	f := &fakeSvc{adminOrders: func(lf service.ListFilter) (service.AdminOrderList, error) {
		got = lf
		return service.AdminOrderList{Total: 1, Orders: []service.AdminOrderView{{
			OrderRow: service.OrderRow{ID: uuid.New(), OrderNo: "AO1", Kind: "topup", Status: "pending_payment", Currency: money.CNY,
				Total: money.FromCents(5000), Payable: money.FromCents(5000)},
			UserEmail: "a@example.test", ItemCount: 1,
		}}}, nil
	}}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodGet, "/v1/orders?q=%20AO1%20&status=paid,fulfilled&user_id="+userID.String()+"&from=2026-10-01&to=2026-10-12&limit=5&offset=10", "")
	mustStatus(t, w, http.StatusOK)
	if got.Q != "AO1" || len(got.Statuses) != 2 || got.UserID == nil || *got.UserID != userID || got.Limit != 5 || got.Offset != 10 {
		t.Fatalf("筛选 %+v", got)
	}
	if got.From == nil || !got.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		got.To == nil || !got.To.Equal(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("日期区间 from=%v to=%v", got.From, got.To)
	}
	m := obj(t, w)
	row := m["orders"].([]any)[0].(map[string]any)
	if row["plan_name"] != "" || row["interval"] != "" || num(t, row, "interval_count") != 0 || num(t, row, "item_count") != 1 || row["manual"] != false {
		t.Fatalf("行 %v", row)
	}
	if num(t, row, "total_amount") != 5000 || row["user_email"] != "a@example.test" {
		t.Fatalf("金额 %v", row)
	}
	mustNull(t, row, "paid_at", "provider_code", "provider_name")
}

// 后台列表的 422：user_id、from、to 格式不对。
func TestAdminOrdersValidation(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	b := mustErr(t, do(e, http.MethodGet, "/v1/orders?user_id=nope", ""), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["user_id"] != "格式不正确" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	b = mustErr(t, do(e, http.MethodGet, "/v1/orders?from=2026/10/01", ""), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["from"] == "" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
	b = mustErr(t, do(e, http.MethodGet, "/v1/orders?to=yesterday", ""), http.StatusUnprocessableEntity, string(errs.ValidationFailed))
	if b.Error.Fields["to"] == "" {
		t.Fatalf("fields %v", b.Error.Fields)
	}
}

// 后台详情：库里没有的列不输出（organization_id、tax_amount、行的 product_id）；
// 可空字段为空时是 null；plan_name 只在套餐行有；快照是原样 JSON，空串输出 []。
func TestAdminOrderDetailShape(t *testing.T) {
	orderID, planID := uuid.New(), uuid.New()
	f := &fakeSvc{adminOrder: func(id uuid.UUID) (service.AdminOrderDetail, error) {
		if id != orderID {
			return service.AdminOrderDetail{}, service.ErrNotFound
		}
		return service.AdminOrderDetail{
			Order: service.AdminOrderView{OrderRow: service.OrderRow{ID: orderID, Kind: "new", Status: "pending_payment", Currency: money.CNY,
				Subtotal: money.FromCents(3000), Discount: money.FromCents(500), Total: money.FromCents(2500), Payable: money.FromCents(2500),
				StateVersion: 3}},
			Items: []service.ItemRow{
				{ID: uuid.New(), PlanID: &planID, Name: "月付套餐", Entitlements: `["a"]`, Quotas: `[{"k":"traffic.bytes","v":1}]`,
					Quantity: 1, UnitAmount: money.FromCents(3000), LineAmount: money.FromCents(3000), Currency: money.CNY},
				{ID: uuid.New(), Name: "流量包", Quantity: 1, UnitAmount: money.FromCents(100), LineAmount: money.FromCents(100), Currency: money.CNY},
			},
		}, nil
	}}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodGet, "/v1/orders/"+orderID.String(), "")
	mustStatus(t, w, http.StatusOK)
	o := sub(t, obj(t, w), "order")
	mustAbsent(t, o, "organization_id", "tax_amount", "refunded_amount")
	mustNull(t, o, "coupon_id", "manual_reason", "created_by", "created_by_email", "subscription_id",
		"fulfilled_at", "cancelled_at", "expired_at", "cancel_reason", "paid_at", "provider_code", "provider_name")
	if num(t, o, "subtotal_amount") != 3000 || num(t, o, "discount_amount") != 500 || num(t, o, "state_version") != 3 {
		t.Fatalf("金额 %v", o)
	}
	if o["expires_at"] == nil {
		t.Fatal("expires_at 应有值")
	}
	items := o["items"].([]any)
	plan, pack := items[0].(map[string]any), items[1].(map[string]any)
	mustAbsent(t, plan, "product_id")
	if plan["plan_name"] != "月付套餐" || plan["product_name"] != "月付套餐" {
		t.Fatalf("套餐行 %v", plan)
	}
	if q, ok := plan["snapshot_quotas"].([]any); !ok || len(q) != 1 {
		t.Fatalf("快照应原样输出 %#v", plan["snapshot_quotas"])
	}
	mustNull(t, pack, "plan_name", "plan_id")
	mustAbsent(t, pack, "product_id")
	if q, ok := pack["snapshot_entitlements"].([]any); !ok || len(q) != 0 {
		t.Fatalf("空快照应是 []，得到 %#v", pack["snapshot_entitlements"])
	}
	mustErr(t, do(e, http.MethodGet, "/v1/orders/"+uuid.NewString(), ""), http.StatusNotFound, string(errs.NotFound))
	mustErr(t, do(e, http.MethodGet, "/v1/orders/123", ""), http.StatusNotFound, string(errs.NotFound))
}

// 后台支付记录：provider_ref、payment_intent_id、method 可空。
func TestAdminOrderPaymentsShape(t *testing.T) {
	orderID := uuid.New()
	f := &fakeSvc{adminPayments: func(uuid.UUID) (paymentsvc.OrderHistory, error) {
		return paymentsvc.OrderHistory{
			Intents: []paymentsvc.IntentView{{ID: uuid.New(), ProviderCode: "epay", ProviderName: "易支付", Currency: money.CNY,
				Amount: money.FromCents(1500), Status: paymentsvc.IntentSucceeded}},
			Payments: []paymentsvc.PaymentView{{ID: uuid.New(), ProviderCode: "offline", ProviderName: "线下", ProviderPaymentID: "R1",
				Currency: money.CNY, Amount: money.FromCents(1500), Fee: money.FromCents(9), Status: "succeeded"}},
		}, nil
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/orders/"+orderID.String()+"/payments", "")
	mustStatus(t, w, http.StatusOK)
	m := obj(t, w)
	in := m["payment_intents"].([]any)[0].(map[string]any)
	mustNull(t, in, "provider_ref", "expires_at")
	if in["status"] != "succeeded" || num(t, in, "amount") != 1500 {
		t.Fatalf("意图 %v", in)
	}
	p := m["payments"].([]any)[0].(map[string]any)
	mustNull(t, p, "payment_intent_id", "method")
	if num(t, p, "fee_amount") != 9 || p["provider_payment_id"] != "R1" {
		t.Fatalf("收款 %v", p)
	}
}

// 后台取消：两层 already_terminal（顶层与 order 里各一份，值相同）；挂 admin_order_cancel 幂等；乐观锁冲突 409。
func TestAdminCancelTwoLayers(t *testing.T) {
	orderID := uuid.New()
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	var got service.CancelInput
	f := &fakeSvc{cancel: func(in service.CancelInput) (service.CancelResult, error) {
		got = in
		return service.CancelResult{Order: service.OrderRow{ID: orderID, Status: service.StatusCancelled, StateVersion: 4,
			CancelledAt: &at, CancelReason: ptr("客户要求取消")}, AlreadyTerminal: true}, nil
	}}
	rec := &kitRec{}
	e := newEngine(f, true, rec)
	w := do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/cancel", `{"expected_state_version":3,"reason":"客户要求取消"}`)
	mustStatus(t, w, http.StatusOK)
	if !got.Admin || got.ExpectedStateVersion == nil || *got.ExpectedStateVersion != 3 || got.Reason != "客户要求取消" {
		t.Fatalf("入参 %+v", got)
	}
	m := obj(t, w)
	inner := sub(t, m, "order")
	if m["already_terminal"] != true || inner["already_terminal"] != true {
		t.Fatalf("两层 already_terminal 都应为真: %v", m)
	}
	if inner["order_id"] != orderID.String() || inner["status"] != "cancelled" || num(t, inner, "state_version") != 4 || inner["cancel_reason"] != "客户要求取消" {
		t.Fatalf("order %v", inner)
	}
	mustContain(t, rec.scopes, ScopeAdminCancel)
	if rec.reauth != 0 {
		t.Fatal("后台取消不该要求重认证")
	}

	f.cancel = func(service.CancelInput) (service.CancelResult, error) {
		return service.CancelResult{}, service.ErrStateChanged
	}
	mustErr(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/cancel", `{"expected_state_version":1,"reason":"客户要求取消"}`),
		http.StatusConflict, string(errs.Conflict))
	mustErr(t, do(e, http.MethodPost, "/v1/orders/x/cancel", `{}`), http.StatusNotFound, string(errs.NotFound))
}

// 标记已支付：要重认证、挂 admin_order_mark_paid 幂等；没有的 id 输出空串。
func TestMarkPaidShape(t *testing.T) {
	orderID, payID := uuid.New(), uuid.New()
	var got service.MarkPaidInput
	f := &fakeSvc{markPaid: func(in service.MarkPaidInput) (service.MarkPaidResult, error) {
		got = in
		return service.MarkPaidResult{Processed: true, PaymentID: &payID}, nil
	}}
	rec := &kitRec{}
	e := newEngine(f, true, rec)
	w := do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/mark-paid", `{"reason":"线下转账已到账","reference":"BANK-1"}`)
	mustStatus(t, w, http.StatusOK)
	if got.OrderID != orderID || got.Reason != "线下转账已到账" || got.Reference != "BANK-1" {
		t.Fatalf("入参 %+v", got)
	}
	m := obj(t, w)
	if m["processed"] != true || m["already_handled"] != false || m["payment_id"] != payID.String() || m["subscription_id"] != "" || m["ledger_txn_id"] != "" {
		t.Fatalf("响应 %v", m)
	}
	if rec.reauth != 1 {
		t.Fatalf("标记已支付应要求重认证，记到 %d 次", rec.reauth)
	}
	mustContain(t, rec.scopes, ScopeAdminMarkPaid)

	f.markPaid = func(service.MarkPaidInput) (service.MarkPaidResult, error) {
		return service.MarkPaidResult{}, service.ErrPaymentLapsed
	}
	mustErr(t, do(e, http.MethodPost, "/v1/orders/"+orderID.String()+"/mark-paid", `{"reason":"线下转账已到账"}`), http.StatusConflict, string(errs.OrderLapsed))
}

// 后台查单：挂 admin_order_query 幂等、不要重认证；quarantine_kind 为空时省略。
func TestAdminQuery(t *testing.T) {
	orderID := uuid.New()
	f := &fakeSvc{adminQuery: func(id uuid.UUID) (service.QueryResult, error) {
		return service.QueryResult{OrderID: id, OrderNo: "AO1", ProviderCode: "epay", ChannelStatus: "unpaid", OrderStatus: "pending_payment"}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, true, rec), http.MethodPost, "/v1/orders/"+orderID.String()+"/query", "")
	mustStatus(t, w, http.StatusOK)
	m := obj(t, w)
	if m["order_id"] != orderID.String() || m["reconciled"] != false || m["already_recorded"] != false {
		t.Fatalf("响应 %v", m)
	}
	mustAbsent(t, m, "quarantine_kind")
	mustContain(t, rec.scopes, ScopeAdminQuery)
	if rec.reauth != 0 {
		t.Fatal("后台查单不该要求重认证")
	}
}
