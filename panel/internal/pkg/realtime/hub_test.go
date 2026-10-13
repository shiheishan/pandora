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
	case <-sub.Ended:
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
	case <-sub.Ended:
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
	// 重建要先等 resubscribeDelay（2 秒），留足余量
	deadline := time.Now().Add(6 * time.Second)
	for !h.Live() {
		if time.Now().After(deadline) {
			t.Fatal("hub 订阅未就绪")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitEnded(t *testing.T, sub *Subscription, what string) {
	t.Helper()
	select {
	case <-sub.Ended:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s：连接没有结束", what)
	}
}

// 关闭后既有连接结束、新连接被拒：停机时 HTTP Shutdown 不会被长连接拖住。
func TestHubCloseEndsStreams(t *testing.T) {
	h := NewHub(nil, nil, 100)
	sub, err := h.Subscribe(uuid.New(), []string{ChannelPublic()})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	h.Close()
	waitEnded(t, sub, "Hub 关闭")
	if _, err := h.Subscribe(uuid.New(), []string{ChannelPublic()}); err != ErrNotLive {
		t.Fatalf("关闭后订阅应被拒，got %v", err)
	}
}

// 广播订阅断过：本机连接全部结束（断线期间的吊销可能丢了），重建后恢复接新连接。
func TestHubResubscribeEndsStreams(t *testing.T) {
	mr, rdb := testRedis(t)
	h := newHub(rdb, nil, 100, 200*time.Millisecond)
	t.Cleanup(h.Close)
	waitReady(t, h)
	sub, err := h.Subscribe(uuid.New(), []string{ChannelPublic()})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	addr := mr.Addr()
	mr.Close()
	waitEnded(t, sub, "订阅中断")
	if h.Live() {
		t.Fatal("订阅中断后仍报 live")
	}
	if _, err := h.Subscribe(uuid.New(), []string{ChannelPublic()}); err != ErrNotLive {
		t.Fatalf("中断期间新连接应被拒，got %v", err)
	}
	if err := mr.StartAddr(addr); err != nil {
		t.Fatal(err)
	}
	waitReady(t, h)

	again, err := h.Subscribe(uuid.New(), []string{ChannelPublic()})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err := rdb.Publish(context.Background(), ChannelPublic(), `{"topic":"plans.changed"}`).Err(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-again.Events:
		if ev.Topic != TopicPlans {
			t.Fatalf("topic %q", ev.Topic)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("重建后收不到事件")
	}
}

// 空闲超过健康间隔时发 PING 保活，订阅不被误判断开。
func TestHubIdlePingKeepsLive(t *testing.T) {
	_, rdb := testRedis(t)
	h := newHub(rdb, nil, 100, 100*time.Millisecond)
	t.Cleanup(h.Close)
	waitReady(t, h)
	sub, err := h.Subscribe(uuid.New(), []string{ChannelPublic()})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	time.Sleep(700 * time.Millisecond)
	select {
	case <-sub.Ended:
		t.Fatal("空闲时连接被误结束")
	default:
	}
	if !h.Live() {
		t.Fatal("空闲后不再 live")
	}
}
