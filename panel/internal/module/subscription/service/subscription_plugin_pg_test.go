package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
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
	var out []pluginEvt
	for _, e := range r.events {
		if e.Event == event {
			out = append(out, e)
		}
	}
	return out
}

func TestPluginProvisionedFromOrder(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	orderID := newOrder(t, e, ctx, user, "new", nil)
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: monthly(), SourceOrderID: &orderID, Actor: Actor{Kind: ActorSystem},
	})
	if err != nil {
		t.Fatal(err)
	}
	evts := rec.find("subscription.provisioned")
	if len(evts) != 1 {
		t.Fatalf("事件数 %d", len(evts))
	}
	ev := evts[0]
	wantKey := "sub.prov:" + sub.ID.String() + ":" + orderID.String()
	if ev.Key != wantKey {
		t.Fatalf("key %q want %q", ev.Key, wantKey)
	}
	if ev.Payload["reason"] != "new" {
		t.Fatalf("reason %v", ev.Payload["reason"])
	}
	if ev.Payload["subscription_id"] != sub.ID.String() || ev.Payload["user_id"] != user.String() || ev.Payload["order_id"] != orderID.String() {
		t.Fatalf("payload ids: %+v", ev.Payload)
	}
}

func TestPluginProvisionWithoutOrderSilent(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx)
	if _, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: monthly(), Actor: Actor{Kind: ActorSystem},
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("不应有事件: %+v", rec.events)
	}
}

func TestPluginRenewProvisioned(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	orderID := newOrder(t, e, ctx, user, "renewal", &sub.ID)
	if _, err := e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: time.Now(),
		OrderID: &orderID, Actor: Actor{Kind: ActorPayment}, Source: "order",
	}); err != nil {
		t.Fatal(err)
	}
	evts := rec.find("subscription.provisioned")
	if len(evts) != 1 {
		t.Fatalf("事件数 %d", len(evts))
	}
	if evts[0].Payload["reason"] != "renewal" {
		t.Fatalf("reason %v", evts[0].Payload["reason"])
	}
	wantKey := "sub.prov:" + sub.ID.String() + ":" + orderID.String()
	if evts[0].Key != wantKey {
		t.Fatalf("key %q", evts[0].Key)
	}
}

func TestPluginChangePlanProvisioned(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planA, versionA := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	planB, versionB := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(200 * gib)})
	sub := provision(t, e, ctx, user, planA, versionA, "")
	orderID := newOrder(t, e, ctx, user, "upgrade", &sub.ID)
	if _, err := e.svc.ChangePlan(ctx, PlanChange{
		SubscriptionID: sub.ID, UserID: user, PlanID: planB, PlanVersionID: versionB,
		Price: monthly(), OrderID: &orderID, Actor: Actor{Kind: ActorPayment},
	}); err != nil {
		t.Fatal(err)
	}
	evts := rec.find("subscription.provisioned")
	if len(evts) != 1 {
		t.Fatalf("事件数 %d", len(evts))
	}
	if evts[0].Payload["reason"] != "upgrade" {
		t.Fatalf("reason %v", evts[0].Payload["reason"])
	}
}

func TestPluginExpired(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	var planName string
	if err := e.row(ctx, `SELECT name FROM plans WHERE id = ?`, planID).Scan(&planName); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	periodEnd := now.Add(-1 * time.Hour)
	a := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, a.ID, StatusActive, periodEnd)

	if _, err := e.svc.ExpireDue(ctx); err != nil {
		t.Fatal(err)
	}
	// 库里可能还有别的用例真提交留下的到期订阅：只看本用例这一份。
	var evts []pluginEvt
	for _, ev := range rec.find("subscription.expired") {
		if ev.Payload["subscription_id"] == a.ID.String() {
			evts = append(evts, ev)
		}
	}
	if len(evts) != 1 {
		t.Fatalf("事件数 %d", len(evts))
	}
	ev := evts[0]
	wantKey := fmt.Sprintf("sub.expired:%s:%d", a.ID, periodEnd.Unix())
	if ev.Key != wantKey {
		t.Fatalf("key %q want %q", ev.Key, wantKey)
	}
	if ev.Payload["plan"] != planName {
		t.Fatalf("plan %q want %q", ev.Payload["plan"], planName)
	}
	wantExpired := periodEnd.UTC().Format(time.RFC3339)
	if ev.Payload["expired_at"] != wantExpired {
		t.Fatalf("expired_at %q want %q", ev.Payload["expired_at"], wantExpired)
	}
	if ev.Payload["subscription_id"] != a.ID.String() || ev.Payload["user_id"] != user.String() {
		t.Fatalf("payload: %+v", ev.Payload)
	}
}

// 真提交：service 的 tx.Do 会加入 ctx 里已有的事务，回滚型 ctx 下业务失败也看不出回滚。
func TestPluginEmitErrorRollsBackProvision(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rec := &pluginRec{err: errors.New("boom")}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	orderID := newOrder(t, e, ctx, user, "new", nil)
	_, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planID, PlanVersionID: versionID,
		Price: monthly(), SourceOrderID: &orderID, Actor: Actor{Kind: ActorSystem},
	})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user); n != 0 {
		t.Fatalf("订阅数 %d，应回滚为 0", n)
	}
}
