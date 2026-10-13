package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// errBoom 是回滚用例里故意返回的错误。
var errBoom = errors.New("故意回滚")

// listenUser 给 service 注入接 miniredis 的推送，并订阅该用户的私有频道。
func listenUser(t *testing.T, e *env, user uuid.UUID) *redis.PubSub {
	t.Helper()
	mr, _ := testkit.Redis(t)
	// miniredis 在 RESP3 下订阅确认要等到读超时，测试用 RESP2
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = rdb.Close() })
	e.svc.UseRealtime(realtime.NewPublisher(rdb))
	ps := rdb.Subscribe(context.Background(), realtime.ChannelUser(user))
	if _, err := ps.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// nextEvent 在 wait 内取一条事件；超时返回 false。
func nextEvent(t *testing.T, ps *redis.PubSub, wait time.Duration) (realtime.Event, bool) {
	t.Helper()
	ch := ps.Channel()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case msg, ok := <-ch:
		if !ok {
			return realtime.Event{}, false
		}
		var ev realtime.Event
		if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
			t.Fatal(err)
		}
		return ev, true
	case <-timer.C:
		return realtime.Event{}, false
	}
}

// TestRealtimeOrderCreateAndCancel 验证建单与取消提交后会在用户频道收到 INSERT/UPDATE 订单事件。
func TestRealtimeOrderCreateAndCancel(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", intp(10), nil)
	ps := listenUser(t, e, user)
	c := e.buyNew(t, ctx, user, planID, priceID, Checkout{})
	ev, ok := nextEvent(t, ps, 2*time.Second)
	if !ok {
		t.Fatal("建单后未收到实时事件")
	}
	if ev.Topic != realtime.TopicOrders {
		t.Fatalf("topic %q", ev.Topic)
	}
	if ev.Payload["table"] != "orders" {
		t.Fatalf("table %v", ev.Payload["table"])
	}
	if ev.Payload["op"] != realtime.OpInsert {
		t.Fatalf("op %v", ev.Payload["op"])
	}
	if ev.Payload["id"] != c.OrderID.String() {
		t.Fatalf("id %v", ev.Payload["id"])
	}
	if _, err := e.svc.Cancel(asUser(ctx, user), CancelInput{OrderID: c.OrderID}); err != nil {
		t.Fatal(err)
	}
	ev, ok = nextEvent(t, ps, 2*time.Second)
	if !ok {
		t.Fatal("取消后未收到实时事件")
	}
	if ev.Payload["op"] != realtime.OpUpdate {
		t.Fatalf("op %v", ev.Payload["op"])
	}
	if ev.Payload["id"] != c.OrderID.String() {
		t.Fatalf("id %v", ev.Payload["id"])
	}
}

// TestRealtimeOrderRollbackNoEvent 验证事务回滚时不会发出订单实时事件。
func TestRealtimeOrderRollbackNoEvent(t *testing.T) {
	e, ctx := newCommitEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, priceID := e.mkPlan(t, ctx, 3000, "month", intp(10), nil)
	ps := listenUser(t, e, user)
	err := e.txm.Do(ctx, func(ctx context.Context) error {
		if _, err := e.svc.CreateNewOrder(asUser(ctx, user), NewOrderInput{PlanID: planID, PriceID: priceID}); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("期望 errBoom，得到 %v", err)
	}
	if _, ok := nextEvent(t, ps, 500*time.Millisecond); ok {
		t.Fatal("回滚后不应收到实时事件")
	}
}
