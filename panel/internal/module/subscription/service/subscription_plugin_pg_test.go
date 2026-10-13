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

// subscription.provisioned 只在 order 的外部支付结算路径发（与旧代码一致），本模块的开通、续期、换套餐都不发。
func TestPluginProvisionPathsSilent(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	rec := &pluginRec{}
	e.svc.UsePluginEvents(rec)
	user := newUser(t, e, ctx)
	planA, versionA := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(100 * gib)})
	planB, versionB := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(200 * gib)})

	newID := newOrder(t, e, ctx, user, "new", nil)
	sub, err := e.svc.Provision(ctx, Provision{
		UserID: user, PlanID: planA, PlanVersionID: versionA,
		Price: monthly(), SourceOrderID: &newID, Actor: Actor{Kind: ActorSystem},
	})
	if err != nil {
		t.Fatal(err)
	}
	renewID := newOrder(t, e, ctx, user, "renewal", &sub.ID)
	if _, err := e.svc.Renew(ctx, Renewal{
		SubscriptionID: sub.ID, UserID: user, Price: monthly(), PaidAt: time.Now(),
		OrderID: &renewID, Actor: Actor{Kind: ActorPayment}, Source: "order",
	}); err != nil {
		t.Fatal(err)
	}
	fulfilOrder(t, e, ctx, renewID)
	upID := newOrder(t, e, ctx, user, "upgrade", &sub.ID)
	if _, err := e.svc.ChangePlan(ctx, PlanChange{
		SubscriptionID: sub.ID, UserID: user, PlanID: planB, PlanVersionID: versionB,
		Price: monthly(), OrderID: &upID, Actor: Actor{Kind: ActorPayment},
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("不应有事件: %+v", rec.events)
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

// 事件写不进去时到期扫描失败、错误原样带回。ctx 是回滚型事务（service 的 tx.Do 会加入它），这里看不出回滚本身；
// 「事件与业务同生共死」由 plugin 模块的 TestPGEmitRollsBackWithBusiness 在真提交下验证。
func TestPluginExpiredEmitErrorFailsScan(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	emitErr := errors.New("boom")
	e.svc.UsePluginEvents(&pluginRec{err: emitErr})
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	a := provision(t, e, ctx, user, planID, versionID, "")
	expireSetPeriod(t, e, ctx, a.ID, StatusActive, time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour))
	if _, err := e.svc.ExpireDue(ctx); !errors.Is(err, emitErr) {
		t.Fatalf("期望到期扫描因事件失败而失败，得 %v", err)
	}
}
