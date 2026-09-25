package nodefabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/realtime"
)

func drain(t *testing.T, c *StreamConn) StreamMessage {
	t.Helper()
	select {
	case raw := <-c.Send:
		var m StreamMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("消息不是合法 JSON：%v", err)
		}
		return m
	default:
		t.Fatal("队列里没有消息")
		return StreamMessage{}
	}
}

// 新连接拿全量：它手上什么都没有，发增量它没法用。
func TestPushUsersSendsFullToFreshConn(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)

	users := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}
	h.PushUsers("tenant-a", "n1", users, nil)

	m := drain(t, c)
	if m.Event != EventSyncUsers {
		t.Fatalf("事件 = %s，期望全量", m.Event)
	}
	var p SyncUsersPayload
	if err := json.Unmarshal(m.Data, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Users) != 2 || p.Version == "" {
		t.Errorf("载荷不完整：%+v", p)
	}
	// 推完要记住这条连接到了哪一版，否则下次还发全量
	if c.Version != UserSetVersion(users) {
		t.Errorf("连接版本 = %q，没有记住", c.Version)
	}
}

// 版本对得上就发增量。
func TestPushUsersSendsDeltaWhenVersionKnown(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)

	old := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	oldVersion := UserSetVersion(old)
	h.SetVersion(c, oldVersion)

	now := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 4, UUID: "u4"}}
	h.PushUsers("tenant-a", "n1", now, map[string][]ProxyUser{oldVersion: old})

	m := drain(t, c)
	if m.Event != EventSyncUserDelta {
		t.Fatalf("事件 = %s，期望增量", m.Event)
	}
	var p SyncUserDeltaPayload
	if err := json.Unmarshal(m.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.FromVersion != oldVersion {
		t.Errorf("FromVersion = %q，期望 %q", p.FromVersion, oldVersion)
	}
	if len(p.Delta.Added) != 1 || p.Delta.Added[0].ID != 4 {
		t.Errorf("Added = %+v，期望只有 4", p.Delta.Added)
	}
	if len(p.Delta.Removed) != 1 || p.Delta.Removed[0] != 3 {
		t.Errorf("Removed = %v，期望 [3]", p.Delta.Removed)
	}
}

// 已经是最新版的连接不该收到任何东西。
func TestPushUsersSkipsUpToDateConn(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)

	users := []ProxyUser{{ID: 1, UUID: "u1"}}
	h.SetVersion(c, UserSetVersion(users))
	h.PushUsers("tenant-a", "n1", users, nil)

	select {
	case <-c.Send:
		t.Error("给已是最新的连接推了消息")
	default:
	}
}

// 增量比全量还大就发全量——批量改动时常见。
func TestPushUsersPrefersFullWhenDeltaIsLarger(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)

	old := []ProxyUser{{ID: 1}, {ID: 2}, {ID: 3}}
	oldVersion := UserSetVersion(old)
	h.SetVersion(c, oldVersion)

	// 全换一批：3 删 3 增 = 6 条，比全量的 3 条还多
	now := []ProxyUser{{ID: 7}, {ID: 8}, {ID: 9}}
	h.PushUsers("tenant-a", "n1", now, map[string][]ProxyUser{oldVersion: old})

	if m := drain(t, c); m.Event != EventSyncUsers {
		t.Errorf("事件 = %s，这种情况该发全量", m.Event)
	}
}

// 慢消费者要被断开，不能拖住推送方。
//
// 这是长连接服务最经典的死法：一个卡住的节点让所有推送排队，最后拖垮
// 整个面板。宁可断开——节点会重连，重连走全量，不丢数据。
func TestPushDropsSlowConsumer(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 1) // 队列只有 1 格
	h.Add(c)

	// 不消费，连推几次把队列塞满
	for i := 0; i < 5; i++ {
		h.PushConfig("tenant-a", "n1", json.RawMessage(`{"server_port":443}`), "etag")
	}

	if h.Count("tenant-a", "n1") != 0 {
		t.Error("慢消费者没有被断开，推送方会被它拖住")
	}
	select {
	case <-c.Closed():
	default:
		t.Error("连接没有被标记为已关闭")
	}
}

// 断开的连接不该再收到消息，重复移除也不能 panic。
func TestRemoveIsIdempotent(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)
	h.Remove(c)
	h.Remove(c) // 再来一次不能 panic

	h.PushConfig("tenant-a", "n1", json.RawMessage(`{}`), "e")
	if h.Total() != 0 {
		t.Error("移除后仍在计数里")
	}
}

func TestPushConfigDoesNotCrossTenantBoundary(t *testing.T) {
	h := NewStreamHub()
	first := NewStreamConn("tenant-a", "shared-node-id", 1)
	second := NewStreamConn("tenant-b", "shared-node-id", 1)
	h.Add(first)
	h.Add(second)

	h.PushConfig("tenant-a", "shared-node-id", json.RawMessage(`{}`), "etag-a")
	if event := drain(t, first); event.Event != EventSyncConfig {
		t.Fatalf("first tenant event = %q, want %q", event.Event, EventSyncConfig)
	}
	select {
	case event := <-second.Send:
		t.Fatalf("other tenant received event: %s", event)
	default:
	}
}

func TestNotifyNodeChangedPublishesTenantNodeChannel(t *testing.T) {
	hub := realtime.NewHub(nil, slog.Default())
	defer hub.Close()

	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelNodeAll("tenant-a")})
	defer unsubscribe()

	svc := NewService(nil, nil)
	svc.AttachRealtime(hub)
	svc.notifyNodeChanged(context.Background(), "tenant-a", "node-a")

	select {
	case event := <-events:
		if event.Topic != realtime.TopicNodeConfigChanged {
			t.Fatalf("topic = %q, want %q", event.Topic, realtime.TopicNodeConfigChanged)
		}
		if got, _ := event.Payload["node_id"].(string); got != "node-a" {
			t.Fatalf("node_id = %q, want node-a", got)
		}
	default:
		t.Fatal("node change was not published to the tenant node channel")
	}
}

// 交付集合变化是租户级事件：不带 node_id，WatchNodeChanges 据此给本进程上
// 该租户的每个节点重算用户列表（R104）。
func TestNotifyUsersChangedPublishesTenantLevelEvent(t *testing.T) {
	hub := realtime.NewHub(nil, slog.Default())
	defer hub.Close()

	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelNodeAll("tenant-a")})
	defer unsubscribe()

	svc := NewService(nil, nil)
	svc.NotifyUsersChanged(context.Background(), "tenant-a") // 没挂 realtime：静默
	svc.AttachRealtime(hub)
	svc.NotifyUsersChanged(context.Background(), "tenant-a")

	select {
	case event := <-events:
		if event.Topic != realtime.TopicNodeUsersChanged {
			t.Fatalf("topic = %q, want %q", event.Topic, realtime.TopicNodeUsersChanged)
		}
		if _, ok := event.Payload["node_id"]; ok {
			t.Fatalf("tenant-level event must not carry node_id: %v", event.Payload)
		}
	default:
		t.Fatal("users change was not published to the tenant node channel")
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected second event: %+v", event)
	default:
	}
}

func TestRegisterStreamSharesTenantWatcherUntilLastConnectionLeaves(t *testing.T) {
	hub := realtime.NewHub(nil, slog.Default())
	defer hub.Close()

	svc := NewService(nil, nil)
	svc.AttachStream(NewStreamHub())
	svc.AttachRealtime(hub)

	first := NewStreamConn("tenant-a", "node-a", 1)
	second := NewStreamConn("tenant-a", "node-b", 1)
	svc.RegisterStream(first, slog.Default())
	svc.RegisterStream(second, slog.Default())

	if got := svc.nodeChangeWatcherCount(); got != 1 {
		t.Fatalf("watchers after two tenant connections = %d, want 1", got)
	}

	svc.UnregisterStream(first)
	if got := svc.nodeChangeWatcherCount(); got != 1 {
		t.Fatalf("watchers after one tenant connection leaves = %d, want 1", got)
	}

	svc.UnregisterStream(second)
	if got := svc.nodeChangeWatcherCount(); got != 0 {
		t.Fatalf("watchers after last tenant connection leaves = %d, want 0", got)
	}
}
