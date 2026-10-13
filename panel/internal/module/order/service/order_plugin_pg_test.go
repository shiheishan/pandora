package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

type pluginRec struct {
	events []pluginEvt
	err    error
}

type pluginEvt struct {
	Event, Key string
	Payload    map[string]any
}

func (r *pluginRec) Emit(_ context.Context, event, key string, payload any) error {
	if r.err != nil {
		return r.err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	r.events = append(r.events, pluginEvt{Event: event, Key: key, Payload: m})
	return nil
}

func (r *pluginRec) find(event string) []pluginEvt {
	out := []pluginEvt{}
	for _, e := range r.events {
		if e.Event == event {
			out = append(out, e)
		}
	}
	return out
}

func payloadStr(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func payloadFloat(m map[string]any, k string) float64 {
	v, ok := m[k]
	if !ok || v == nil {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return f
}

func TestPluginOrderCreatedNew(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", intp(10), intp(3))
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})

	created := rec.find("order.created")
	if len(created) != 1 {
		t.Fatalf("order.created %d 条，期望 1", len(created))
	}
	ev := created[0]
	if ev.Key != "order.created:"+c.OrderID.String() {
		t.Fatalf("key %q", ev.Key)
	}
	p := ev.Payload
	if payloadStr(p, "order_id") != c.OrderID.String() {
		t.Fatalf("order_id %v", p["order_id"])
	}
	if payloadStr(p, "order_no") != c.OrderNo {
		t.Fatalf("order_no %v", p["order_no"])
	}
	if payloadStr(p, "user_id") != user.String() {
		t.Fatalf("user_id %v", p["user_id"])
	}
	if payloadStr(p, "currency") != "CNY" {
		t.Fatalf("currency %v", p["currency"])
	}
	if payloadFloat(p, "total") != float64(money.MustCents(c.Total)) {
		t.Fatalf("total %v，期望 %d 分", p["total"], money.MustCents(c.Total))
	}
	if len(rec.find("order.paid")) != 0 {
		t.Fatalf("待付单不应有 order.paid")
	}
}

func TestPluginOrderPaidZeroPay(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month", intp(5), intp(2))
	code := couponCode()
	e.mkCoupon(t, ctx, code, 500)
	e.giveBalance(t, ctx, user, 5000)

	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{UseBalance: dec("50"), CouponCode: code})
	if len(rec.events) < 2 {
		t.Fatalf("事件数 %d", len(rec.events))
	}
	if rec.events[0].Event != "order.created" || rec.events[1].Event != "order.paid" {
		t.Fatalf("顺序: %s, %s", rec.events[0].Event, rec.events[1].Event)
	}
	paid := rec.events[1].Payload
	if payloadStr(paid, "kind") != "new" {
		t.Fatalf("kind %v", paid["kind"])
	}
	subID := payloadStr(paid, "subscription_id")
	if subID == "" {
		t.Fatalf("缺少 subscription_id")
	}
	o := e.order(t, ctx, c.OrderID)
	if o.SubscriptionID == nil || subID != o.SubscriptionID.String() {
		t.Fatalf("subscription_id %s vs 订单 %v", subID, o.SubscriptionID)
	}
	// 0 元单照旧代码口径：currency 为 ""、amount 为 0
	if v, ok := paid["currency"]; !ok || v != "" {
		t.Fatalf("currency %v", paid["currency"])
	}
	if v, ok := paid["amount"]; !ok || v != float64(0) {
		t.Fatalf("amount %v", paid["amount"])
	}
	// 0 元单不发 subscription.provisioned（只在外部支付结算发）
	if n := len(rec.find("subscription.provisioned")); n != 0 {
		t.Fatalf("0 元单不应有 subscription.provisioned，得 %d 条", n)
	}
}

func TestPluginOrderPaidWebhook(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	code := demoCode()
	e.mkDemoProvider(t, ctx, code)
	user, _, orderID := e.pendingWithEverything(t, ctx)

	ack := e.payAndNotify(t, ctx, user, orderID, code, "pay-"+randHex(8))
	if ack.HTTPStatus != http.StatusOK {
		t.Fatalf("回执 %d", ack.HTTPStatus)
	}

	paid := rec.find("order.paid")
	if len(paid) != 1 {
		t.Fatalf("order.paid %d 条", len(paid))
	}
	p := paid[0].Payload
	o := e.order(t, ctx, orderID)
	if payloadFloat(p, "amount") != float64(money.MustCents(o.TotalAmount)) {
		t.Fatalf("amount %v", p["amount"])
	}
	if o.SubscriptionID == nil {
		t.Fatal("订单无订阅")
	}
	if payloadStr(p, "subscription_id") != o.SubscriptionID.String() {
		t.Fatalf("subscription_id %v", p["subscription_id"])
	}

	prov := rec.find("subscription.provisioned")
	if len(prov) != 1 {
		t.Fatalf("subscription.provisioned %d 条", len(prov))
	}
	wantKey := "sub.prov:" + o.SubscriptionID.String() + ":" + orderID.String()
	if prov[0].Key != wantKey {
		t.Fatalf("key %q want %q", prov[0].Key, wantKey)
	}
	pp := prov[0].Payload
	if payloadStr(pp, "subscription_id") != o.SubscriptionID.String() || payloadStr(pp, "user_id") != user.String() ||
		payloadStr(pp, "order_id") != orderID.String() || payloadStr(pp, "reason") != o.Kind {
		t.Fatalf("payload %+v", pp)
	}
}

func TestPluginOrderCancelled(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	user, _, orderID := e.pendingWithEverything(t, ctx)
	_, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: orderID})
	if err != nil {
		t.Fatalf("取消: %v", err)
	}

	cancelled := rec.find("order.cancelled")
	if len(cancelled) != 1 {
		t.Fatalf("order.cancelled %d 条", len(cancelled))
	}
	p := cancelled[0].Payload
	if payloadStr(p, "target") != "cancelled" {
		t.Fatalf("target %v", p["target"])
	}
	if payloadStr(p, "reason") != ReasonUserCancelled {
		t.Fatalf("reason %v", p["reason"])
	}
}

func TestPluginPackOrderCreated(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	sub := e.provision(t, ctx, user, planID, versionID, priceID, 1000)
	packID := e.mkPack(t, ctx, 600, 50*gib)

	c, err := e.svc.CreatePackOrder(asUser(ctx, user), PackOrderInput{PackID: packID, SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("流量包建单: %v", err)
	}
	created := rec.find("order.created")
	if len(created) != 1 {
		t.Fatalf("order.created %d 条", len(created))
	}
	if payloadStr(created[0].Payload, "order_id") != c.OrderID.String() {
		t.Fatalf("order_id 不对")
	}
}

// 事件口报错时整笔建单回滚：要真提交的环境（回滚型环境里 Do 加入外层事务，看不出回滚）。
func TestPluginEmitErrorFailsOrder(t *testing.T) {
	e, ctx := newCommitEnv(t)
	rec := &pluginRec{err: errors.New("boom")}
	e.svc.UsePluginEvents(rec)

	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", intp(10), intp(3))
	_, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, Checkout: Checkout{}})
	if err == nil {
		t.Fatal("期望建单失败")
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ?`, user); n != 0 {
		t.Fatalf("库里订单 %d 张", n)
	}
}
