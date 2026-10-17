package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
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
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
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
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
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
	// 0 元单：amount 为 0
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

	code := payCode()
	e.mkPayProvider(t, ctx, code)
	user, _, orderID := e.pendingWithEverything(t, ctx)

	ack := e.payAndNotify(t, ctx, user, orderID, code, "pay-"+randHex(8))
	if !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
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

// 超时作废：事件里的 reason 固定是 reservation_expired，预留的 release_reason 仍是 expired。
func TestPluginOrderExpiredReason(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	_, _, orderID := e.pendingWithEverything(t, ctx)
	e.mustExec(t, ctx, `UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = ?`, orderID)
	e.mustExec(t, ctx, `UPDATE order_reservations SET expires_at = now() - interval '1 minute' WHERE order_id = ?`, orderID)
	if _, err := e.svc.ExpireDue(ctx, 0); err != nil {
		t.Fatalf("超时释放: %v", err)
	}

	var mine []pluginEvt
	for _, ev := range rec.find("order.cancelled") {
		if payloadStr(ev.Payload, "order_id") == orderID.String() {
			mine = append(mine, ev)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("order.cancelled %d 条", len(mine))
	}
	if payloadStr(mine[0].Payload, "target") != StatusExpired || payloadStr(mine[0].Payload, "reason") != "reservation_expired" {
		t.Fatalf("payload %+v", mine[0].Payload)
	}
	if r := e.strCol(t, ctx, `SELECT release_reason FROM order_reservations WHERE order_id = ?`, orderID); r != ReasonExpired {
		t.Fatalf("release_reason %q，期望 expired", r)
	}
}

// 0 元续费不发 order.paid（只有新购与流量包的 0 元单发，插件事件口径是产品决定）。
func TestPluginZeroPayRenewalNoPaid(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, v1, priceID := e.mkPlan(t, ctx, 1000, "month")
	sub := e.provision(t, ctx, user, planID, v1, priceID, 1000)
	e.giveBalance(t, ctx, user, 5000)

	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	c, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID, Checkout: Checkout{UseBalance: dec("10")}})
	if err != nil {
		t.Fatalf("续费建单: %v", err)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("0 元续费单应已履约，得 %s", st)
	}
	if n := len(rec.find("order.paid")); n != 0 {
		t.Fatalf("0 元续费不应发 order.paid，得 %d 条", n)
	}
	if n := len(rec.find("subscription.provisioned")); n != 0 {
		t.Fatalf("0 元续费不应发 subscription.provisioned，得 %d 条", n)
	}
}

func TestPluginStackOrderCreated(t *testing.T) {
	e, ctx := newEnv(t)
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)

	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 1000, 100*gib, intp(30))
	pid := priceID
	sub, err := e.subs.Provision(ctx, subsvc.Provision{
		UserID:        user,
		PlanID:        planID,
		PlanVersionID: versionID,
		Price: subsvc.PriceSnapshot{
			PriceID:       &pid,
			Amount:        money.FromCents(1000),
			Interval:      "one_time",
			IntervalCount: 1,
			ValidityDays:  intp(30),
		},
		Actor: subsvc.Actor{Kind: subsvc.ActorSystem},
	})
	if err != nil {
		t.Fatalf("开通一次性订阅: %v", err)
	}

	c, err := e.svc.CreateStackOrder(asUser(ctx, user), StackOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("叠加建单: %v", err)
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
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month")
	_, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID, Checkout: Checkout{}})
	if err == nil {
		t.Fatal("期望建单失败")
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ?`, user); n != 0 {
		t.Fatalf("库里订单 %d 张", n)
	}
}
