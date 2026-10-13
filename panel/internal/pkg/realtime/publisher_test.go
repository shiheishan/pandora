package realtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	// miniredis 在 RESP3 下的订阅确认要等到读超时才到，测试客户端用 RESP2；真 Redis 两种协议都正常
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), Protocol: 2})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func TestChannelsFor(t *testing.T) {
	uid := uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	cases := []struct {
		name  string
		topic string
		owner *uuid.UUID
		want  []string
	}{
		{
			name:  "owner",
			owner: &uid,
			want:  []string{ChannelUser(uid), ChannelAdmin()},
		},
		{
			name:  "nodes",
			topic: TopicNodes,
			want:  []string{ChannelAdmin()},
		},
		{
			name:  "public",
			topic: TopicOrders,
			want:  []string{ChannelPublic(), ChannelAdmin()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := channelsFor(tc.topic, tc.owner)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}

func TestNilPublisherNoPanic(t *testing.T) {
	var p *Publisher
	ctx := context.Background()
	id := uuid.New()
	p.Publish(ctx, Event{Topic: TopicPlans})
	p.Changed(ctx, TopicOrders, "orders", OpInsert, id, nil)
	p.Notify(ctx, TopicSwitches, map[string]any{"k": 1})
}

func TestChangedPublishesAfterCommit(t *testing.T) {
	_, rdb := testRedis(t)
	p := NewPublisher(rdb)
	ctx := context.Background()
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000099")

	sub := rdb.Subscribe(ctx, ChannelPublic(), ChannelAdmin())
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatal(err)
	}

	p.Changed(ctx, TopicOrders, "orders", OpInsert, id, nil)

	msg, err := sub.ReceiveMessage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Channel != ChannelPublic() && msg.Channel != ChannelAdmin() {
		t.Fatalf("channel %q", msg.Channel)
	}
	var ev Event
	if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Topic != TopicOrders || ev.Payload["table"] != "orders" || ev.Payload["op"] != OpInsert {
		t.Fatalf("ev %+v", ev)
	}
	if ev.Payload["id"] != id.String() {
		t.Fatalf("id %v", ev.Payload["id"])
	}
}
