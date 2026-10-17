package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// fakeResetSvc 扩充 fakeSvc 加上 CreateResetOrder 方法。
type fakeResetSvc struct {
	fakeSvc
	createReset func(service.ResetOrderInput) (service.Created, error)
}

func (f *fakeResetSvc) CreateResetOrder(_ context.Context, in service.ResetOrderInput) (service.Created, error) {
	if f.createReset != nil {
		return f.createReset(in)
	}
	return service.Created{}, nil
}

// 流量重置单：201，收到路径里的订阅 id、coupon_code、use_balance（分→元）；挂 checkout 开关与 order_create 幂等。
func TestCreateResetOrderShape(t *testing.T) {
	subID := uuid.New()
	var got service.ResetOrderInput
	c := created()
	f := &fakeResetSvc{
		createReset: func(in service.ResetOrderInput) (service.Created, error) {
			got = in
			return c, nil
		},
	}
	rec := &kitRec{}
	body := `{"use_balance":1000,"coupon_code":"SAVE5","as_of":"2026-10-12T08:00:00Z","expect":{"total":2500,"balance_applied":1000,"payable":1500}}`
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/me/subscriptions/"+subID.String()+"/traffic-reset", body)
	mustStatus(t, w, http.StatusCreated)
	m := obj(t, w)
	if m["order_id"] != c.OrderID.String() || m["order_no"] != c.OrderNo || m["status"] != "pending_payment" {
		t.Fatalf("响应 %v", m)
	}
	if num(t, m, "total_amount") != 2500 || num(t, m, "discount_amount") != 500 || num(t, m, "balance_applied") != 1000 || num(t, m, "payable_amount") != 1500 {
		t.Fatalf("金额应是整数分: %v", m)
	}
	mustAbsent(t, m, "member_discount_amount", "currency")
	if got.SubscriptionID != subID || got.CouponCode != "SAVE5" {
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

// 流量重置单的 404 与未知字段校验错误。
func TestCreateResetOrderErrors(t *testing.T) {
	subID := uuid.New()
	f := &fakeResetSvc{}
	e := newEngine(f, false, nil)

	// 非法 id 回 404（pathID 行为）
	mustErr(t, do(e, http.MethodPost, "/v1/me/subscriptions/not-a-uuid/traffic-reset", `{}`),
		http.StatusNotFound, string(errs.NotFound))

	// 多余字段回 400 bad_request（httpx.BindJSON 未知字段检查）
	mustErr(t, do(e, http.MethodPost, "/v1/me/subscriptions/"+subID.String()+"/traffic-reset", `{"bogus":1}`),
		http.StatusBadRequest, string(errs.BadRequest))
}
