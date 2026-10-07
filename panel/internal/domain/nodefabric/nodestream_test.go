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
	h.PushUsers("tenant-a", "n1", users, UserSetVersion(users))

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
	if c.UsersVersion() != UserSetVersion(users) {
		t.Errorf("连接版本 = %q，没有记住", c.UsersVersion())
	}
}

// pushVersion 推一版名单并取走这条消息，返回版本。
func pushVersion(t *testing.T, h *StreamHub, c *StreamConn, users []ProxyUser) (string, StreamMessage) {
	t.Helper()
	v := UserSetVersion(users)
	h.PushUsers(c.TenantID, c.NodeID, users, v)
	return v, drain(t, c)
}

// 版本对得上就发增量：起点是这条连接上一次推到的版本。
func TestPushUsersSendsDeltaWhenVersionKnown(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)

	old := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	oldVersion, _ := pushVersion(t, h, c, old)

	now := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 4, UUID: "u4"}}
	_, m := pushVersion(t, h, c, now)
	if m.Event != EventSyncUserDelta {
		t.Fatalf("事件 = %s，期望增量", m.Event)
	}
	var p SyncUserDeltaPayload
	if err := json.Unmarshal(m.Data, &p); err != nil {
		t.Fatal(err)
	}
	if p.FromVersion != oldVersion || p.ToVersion != UserSetVersion(now) {
		t.Errorf("版本 = %q→%q，期望 %q→%q", p.FromVersion, p.ToVersion, oldVersion, UserSetVersion(now))
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
	pushVersion(t, h, c, users)
	h.PushUsers("tenant-a", "n1", users, UserSetVersion(users))

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

	pushVersion(t, h, c, []ProxyUser{{ID: 1}, {ID: 2}, {ID: 3}})
	// 全换一批：3 删 3 增 = 6 条，比全量的 3 条还多
	if _, m := pushVersion(t, h, c, []ProxyUser{{ID: 7}, {ID: 8}, {ID: 9}}); m.Event != EventSyncUsers {
		t.Errorf("事件 = %s，这种情况该发全量", m.Event)
	}
}

// 节点经 REST 拿过别的版本之后，事件流不能再从旧版本算增量：pdnd 只核对流版本，
// 不核对内核里实际的名单，增量会打在一份不一样的名单上。
func TestPushUsersFallsBackToFullAfterRESTDeliveredAnotherVersion(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)
	v1 := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	v2 := append(append([]ProxyUser{}, v1...), ProxyUser{ID: 4, UUID: "u4"})
	v3 := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}, {ID: 5, UUID: "u5"}}
	pushVersion(t, h, c, v1)

	// REST 送出了 v2（节点装上了含 4 号的名单），流还记着 v1
	h.BeginUsersPull("tenant-a", "n1")(UserSetVersion(v2), true)
	if _, m := pushVersion(t, h, c, v3); m.Event != EventSyncUsers {
		t.Fatalf("REST 送过别的版本后事件 = %s，必须是全量（否则 4 号留在节点上）", m.Event)
	}
	// 全量之后节点手上就是 v3，下一次可以增量
	v4 := append(append([]ProxyUser{}, v3...), ProxyUser{ID: 6, UUID: "u6"})
	if _, m := pushVersion(t, h, c, v4); m.Event != EventSyncUserDelta {
		t.Fatalf("全量之后事件 = %s，应恢复增量", m.Event)
	}
	// REST 送的就是流记着的那一版、或回了 304：不标脏
	h.BeginUsersPull("tenant-a", "n1")(UserSetVersion(v4), true)
	h.BeginUsersPull("tenant-a", "n1")("", false)
	if _, m := pushVersion(t, h, c, v3); m.Event != EventSyncUserDelta {
		t.Fatalf("REST 送的是同一版后事件 = %s，应仍是增量", m.Event)
	}
}

// REST 拉取在途时只推全量：它送达的名单可能晚于增量被应用。
func TestPushUsersSendsFullWhileRESTPullInFlight(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)
	v1 := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	pushVersion(t, h, c, v1)

	done := h.BeginUsersPull("tenant-a", "n1")
	v2 := append(append([]ProxyUser{}, v1...), ProxyUser{ID: 4, UUID: "u4"})
	if _, m := pushVersion(t, h, c, v2); m.Event != EventSyncUsers {
		t.Fatalf("拉取在途时事件 = %s，必须是全量", m.Event)
	}
	done("", false)
	v3 := append(append([]ProxyUser{}, v2...), ProxyUser{ID: 5, UUID: "u5"})
	if _, m := pushVersion(t, h, c, v3); m.Event != EventSyncUserDelta {
		t.Fatalf("拉取结束后事件 = %s，应恢复增量", m.Event)
	}
}

// 重连时节点报的版本就是当前版：不推首个全量；但下一次变更推全量（报的可能只是流版本）。
func TestPushInitialUsersSkipsClaimedCurrentVersion(t *testing.T) {
	h := NewStreamHub()
	c := NewStreamConn("tenant-a", "n1", 4)
	h.Add(c)
	v1 := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	h.PushInitialUsers(c, v1, UserSetVersion(v1), UserSetVersion(v1))
	select {
	case m := <-c.Send:
		t.Fatalf("节点已是当前版，仍推了 %s", m)
	default:
	}
	v2 := append(append([]ProxyUser{}, v1...), ProxyUser{ID: 4, UUID: "u4"})
	if _, m := pushVersion(t, h, c, v2); m.Event != EventSyncUsers {
		t.Fatalf("跳过首推后的第一次变更 = %s，应为全量", m.Event)
	}

	// 报的版本不是当前版（或没报）：照常推全量
	d := NewStreamConn("tenant-a", "n2", 4)
	h.Add(d)
	h.PushInitialUsers(d, v2, UserSetVersion(v2), UserSetVersion(v1))
	if m := drain(t, d); m.Event != EventSyncUsers {
		t.Fatalf("版本过期的重连首推 = %s，应为全量", m.Event)
	}
}

// 同一版本的全量、同一对版本的增量只编码一次：所有连接拿到的是同一份字节。
func TestPushUsersSharesEncodedBytesAcrossConns(t *testing.T) {
	h := NewStreamHub()
	a := NewStreamConn("t1", "n1", 4)
	b := NewStreamConn("t1", "n2", 4)
	h.Add(a)
	h.Add(b)
	v1 := []ProxyUser{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}}
	h.PushUsers("t1", "n1", v1, UserSetVersion(v1))
	h.PushUsers("t1", "n2", v1, UserSetVersion(v1))
	fa, fb := <-a.Send, <-b.Send
	if &fa[0] != &fb[0] {
		t.Fatal("同一版本的全量给两条连接各编码了一份")
	}
	v2 := append(append([]ProxyUser{}, v1...), ProxyUser{ID: 4, UUID: "u4"})
	h.PushUsers("t1", "n1", v2, UserSetVersion(v2))
	h.PushUsers("t1", "n2", v2, UserSetVersion(v2))
	da, db := <-a.Send, <-b.Send
	if &da[0] != &db[0] {
		t.Fatal("同一对版本的增量给两条连接各编码了一份")
	}
}

func TestEncodeStreamMessageMatchesMarshal(t *testing.T) {
	data, _ := json.Marshal(SyncUsersPayload{Users: []ProxyUser{{ID: 1, UUID: "<u&1>"}}, Version: `"u1-x"`})
	for _, m := range []StreamMessage{
		{Event: EventSyncUsers, Data: data, Timestamp: 1700000000123},
		{Event: EventSyncConfig, Data: json.RawMessage(`{"config":{},"etag":"e"}`)},
		{Event: EventPing},
	} {
		want, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if got := encodeStreamMessage(m.Event, m.Data, m.Timestamp); string(got) != string(want) {
			t.Fatalf("encodeStreamMessage = %s\nwant %s", got, want)
		}
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
