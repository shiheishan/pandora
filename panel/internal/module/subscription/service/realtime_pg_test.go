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

// listenUser 订阅用户私有频道并注入实时推送。
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

// collectEvents 在 wait 内从频道收集已解析的实时事件。
func collectEvents(t *testing.T, ps *redis.PubSub, wait time.Duration) []realtime.Event {
	t.Helper()
	ch := ps.Channel()
	deadline := time.Now().Add(wait)
	var evs []realtime.Event
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return evs
			}
			var ev realtime.Event
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				t.Fatalf("解析事件: %v", err)
			}
			evs = append(evs, ev)
		case <-time.After(remaining):
			return evs
		}
	}
	return evs
}

// hasEvent 判断是否有一条 subscriptions.changed 且 payload 匹配。
func hasEvent(evs []realtime.Event, table, op, id string) bool {
	for _, ev := range evs {
		if ev.Topic != realtime.TopicSubscriptions {
			continue
		}
		if ev.Payload == nil {
			continue
		}
		tbl, _ := ev.Payload["table"].(string)
		operation, _ := ev.Payload["op"].(string)
		eid, _ := ev.Payload["id"].(string)
		if tbl == table && operation == op && eid == id {
			return true
		}
	}
	return false
}

// hasCredentialInsert 判断是否有订阅凭据插入事件。
func hasCredentialInsert(evs []realtime.Event) bool {
	for _, ev := range evs {
		if ev.Topic != realtime.TopicSubscriptions || ev.Payload == nil {
			continue
		}
		tbl, _ := ev.Payload["table"].(string)
		op, _ := ev.Payload["op"].(string)
		if tbl == "subscription_credentials" && op == realtime.OpInsert {
			return true
		}
	}
	return false
}

// TestRealtimeProvisionAndPack 验证开通与发流量包在提交后会推实时事件。
func TestRealtimeProvisionAndPack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	ps := listenUser(t, e, user)
	sub := provision(t, e, ctx, user, planID, versionID, "")
	evs := collectEvents(t, ps, time.Second)
	if !hasEvent(evs, "subscriptions", realtime.OpInsert, sub.ID.String()) {
		t.Fatalf("应有 subscriptions INSERT，事件: %+v", evs)
	}
	if !hasCredentialInsert(evs) {
		t.Fatalf("应有 subscription_credentials INSERT，事件: %+v", evs)
	}
	pack, err := e.svc.GrantTrafficPack(ctx, PackGrant{
		UserID: user, SubscriptionID: &sub.ID, Source: PackSourceAdmin,
		SourceID: uuid.New(), Bytes: gib,
	})
	if err != nil {
		t.Fatal(err)
	}
	evs = collectEvents(t, ps, time.Second)
	if !hasEvent(evs, "traffic_pack_grants", realtime.OpInsert, pack.ID.String()) {
		t.Fatalf("应有 traffic_pack_grants INSERT，事件: %+v", evs)
	}
}

var errBoom = errors.New("故意回滚")

// TestRealtimeRollbackNoEvent 验证事务回滚后不会推送实时事件。
func TestRealtimeRollbackNoEvent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	ps := listenUser(t, e, user)
	err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		if _, err := e.svc.GrantTrafficPack(ctx, PackGrant{
			UserID: user, SubscriptionID: &sub.ID, Source: PackSourceAdmin,
			SourceID: uuid.New(), Bytes: gib,
		}); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("应回滚，得 %v", err)
	}
	evs := collectEvents(t, ps, 500*time.Millisecond)
	if len(evs) != 0 {
		t.Fatalf("回滚后不应有事件，得 %+v", evs)
	}
}
