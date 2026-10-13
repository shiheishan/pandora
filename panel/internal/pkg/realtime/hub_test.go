package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHubPublishDelivered(t *testing.T) {
	h := NewHub(nil, nil, 100)
	owner := uuid.New()
	ch := ChannelPublic()
	sub, err := h.Subscribe(owner, []string{ch})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	h.dispatch(ch, Event{Topic: TopicOrders, Payload: map[string]any{"id": "x"}})

	select {
	case ev := <-sub.Events:
		if ev.Topic != TopicOrders {
			t.Fatalf("topic %q", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestHubSessionRevokedPayload(t *testing.T) {
	_, rdb := testRedis(t)
	h := NewHub(rdb, nil, 100)
	t.Cleanup(h.Close)
	waitReady(t, h)
	sid := "sess-abc"
	owner := uuid.New()
	sub, err := h.Subscribe(owner, []string{ChannelSession(sid)})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	ctx := context.Background()
	if err := rdb.Publish(ctx, ChannelSession(sid), "revoked").Err(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sub.Revoked:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

// 缓冲塞满时吊销照样送达：吊销走单独的信号，不和普通事件抢缓冲。
func TestHubRevokeWhenBufferFull(t *testing.T) {
	h := NewHub(nil, nil, 100)
	sid := "sess-full"
	sub, err := h.Subscribe(uuid.New(), []string{ChannelSession(sid), ChannelPublic()})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for i := 0; i < 100; i++ {
		h.dispatch(ChannelPublic(), Event{Topic: TopicPlans})
	}
	h.dispatch(ChannelSession(sid), Event{Topic: topicSessionRevoked})
	select {
	case <-sub.Revoked:
	default:
		t.Fatal("缓冲满时吊销丢了")
	}
}

func TestHubPerUserLimit(t *testing.T) {
	h := NewHub(nil, nil, 1000)
	owner := uuid.New()
	ch := ChannelPublic()
	for i := 0; i < MaxStreamsPerUser; i++ {
		sub, err := h.Subscribe(owner, []string{ch})
		if err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		defer sub.Close()
	}
	_, err := h.Subscribe(owner, []string{ch})
	if err != ErrTooManyUserStreams {
		t.Fatalf("got %v", err)
	}
}

func TestHubTotalLimit(t *testing.T) {
	h := NewHub(nil, nil, 2)
	ch := ChannelPublic()
	for i := 0; i < 2; i++ {
		sub, err := h.Subscribe(uuid.New(), []string{ch})
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
	}
	_, err := h.Subscribe(uuid.New(), []string{ch})
	if err != ErrTooManyStreams {
		t.Fatalf("got %v", err)
	}
}

func TestHubUnsubscribeCount(t *testing.T) {
	h := NewHub(nil, nil, 100)
	owner := uuid.New()
	sub, err := h.Subscribe(owner, []string{ChannelPublic()})
	if err != nil {
		t.Fatal(err)
	}
	if h.Count() != 1 {
		t.Fatalf("count %d", h.Count())
	}
	sub.Close()
	if h.Count() != 0 {
		t.Fatalf("count %d", h.Count())
	}
}

// waitReady 等 Hub 的模式订阅被 Redis 确认；之前发布的消息收不到。
func waitReady(t *testing.T, h *Hub) {
	t.Helper()
	select {
	case <-h.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("hub 订阅未就绪")
	}
}
