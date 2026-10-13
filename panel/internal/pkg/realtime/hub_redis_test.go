package realtime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 真 Redis（CI 的 redis:8，缺省 RESP3）上走一遍：订阅确认按时到、会话吊销与 JSON 事件都能送达。
// 本机没有 Redis 时跳过。
func TestHubRealRedis(t *testing.T) {
	url := os.Getenv("PANDORA_TEST_REDIS_URL")
	if url == "" {
		t.Skip("PANDORA_TEST_REDIS_URL 未设置")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })

	h := NewHub(rdb, nil, 100)
	t.Cleanup(h.Close)
	waitReady(t, h)

	owner := uuid.New()
	sid := "real-" + uuid.NewString()
	sub, err := h.Subscribe(owner, []string{ChannelSession(sid), ChannelUser(owner)})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	ctx := context.Background()
	p := NewPublisher(rdb)
	p.Publish(ctx, Event{Topic: TopicOrders, Payload: map[string]any{"id": "x"}}, ChannelUser(owner))
	if err := rdb.Publish(ctx, ChannelSession(sid), "revoked").Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.Events:
		if ev.Topic != TopicOrders {
			t.Fatalf("topic %q", ev.Topic)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等订单事件超时")
	}
	select {
	case <-sub.Revoked:
	case <-time.After(3 * time.Second):
		t.Fatal("等会话吊销超时")
	}
}
