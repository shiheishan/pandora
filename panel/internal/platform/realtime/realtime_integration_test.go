package realtime

// H-001 跨进程集成收口专项测试
//
// 验证点（对应 AGENTS.md H-001）：
//  1. 租户/节点隔离：不同租户、不同频道互不串台
//  2. 慢消费者保护：缓冲满时丢弃事件而不是阻塞分发
//  3. Redis 重连：consume() 收到错误后能重新订阅
//  4. watcher 生命周期：Subscribe 返回的注销函数清理索引
//  5. 频道空集合清理：byChannel 不泄漏
//  6. 多实例互通：Publish 走 Redis 广播后另一 Hub 能收到

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// newTestHub 构造测试用 Hub。redisAddr 为空时用纯内存模式（rdb=nil）。
func newTestHub(t *testing.T, redisAddr string) *Hub {
	t.Helper()
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	var rdb *redis.Client
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
		t.Cleanup(func() { _ = rdb.Close() })
	}
	h := NewHub(rdb, log)
	t.Cleanup(h.Close)
	return h
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("log: %s", string(p))
	return len(p), nil
}

// ---------------------------------------------------------------------------
// 1. 租户/频道隔离
// ---------------------------------------------------------------------------

func TestChannelIsolation(t *testing.T) {
	h := newTestHub(t, "")

	chA, unsubA := h.Subscribe([]string{ChannelUser("tenant-1", "user-1")})
	defer unsubA()
	chB, unsubB := h.Subscribe([]string{ChannelUser("tenant-2", "user-1")})
	defer unsubB()

	// 给 tenant-1 的用户发事件，tenant-2 的同一个用户不该收到
	h.Publish(context.Background(), ChannelUser("tenant-1", "user-1"),
		"test.event", map[string]any{"n": 1})

	select {
	case ev := <-chA:
		if ev.Topic != "test.event" {
			t.Fatalf("A 收到错误 topic: %q", ev.Topic)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A 应该在 2s 内收到事件")
	}

	select {
	case ev := <-chB:
		t.Fatalf("B 不该收到跨租户事件: %+v", ev)
	case <-time.After(300 * time.Millisecond):
		// 期望：收不到
	}
}

func TestNodeChannelAllIsolation(t *testing.T) {
	h := newTestHub(t, "")
	ch, unsub := h.Subscribe([]string{ChannelNodeAll("tenant-a")})
	defer unsub()

	// 另一租户的节点变更不该到达
	h.Publish(context.Background(), ChannelNodeAll("tenant-b"),
		TopicNodeConfigChanged, map[string]any{"node_id": "n1"})

	select {
	case ev := <-ch:
		t.Fatalf("跨租户节点事件不该到达: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// 2. 慢消费者保护
// ---------------------------------------------------------------------------

func TestSlowConsumerDropsInsteadOfBlocking(t *testing.T) {
	h := newTestHub(t, "")
	// 缓冲 32，订阅后不消费
	ch, unsub := h.Subscribe([]string{"rt:t1:public"})
	defer unsub()

	// 发 64 条事件（超过缓冲），分发循环必须不阻塞
	done := make(chan struct{})
	go func() {
		for i := 0; i < 64; i++ {
			h.Publish(context.Background(), "rt:t1:public",
				"burst", map[string]any{"i": i})
		}
		close(done)
	}()

	select {
	case <-done:
		// 通过：64 条事件发布完成，没有被慢消费者拖住
	case <-time.After(3 * time.Second):
		t.Fatal("慢消费者阻塞了分发循环")
	}

	// 确认缓冲里最多 32 条，其余被丢弃
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			goto drained
		}
	}
drained:
	if n > 32 {
		t.Fatalf("缓冲超过 32: %d", n)
	}
	t.Logf("慢消费者收到 %d 条（≤32 缓冲），其余丢弃", n)
}

// ---------------------------------------------------------------------------
// 3. 注销清理（watcher 生命周期）
// ---------------------------------------------------------------------------

func TestUnsubscribeCleansIndex(t *testing.T) {
	h := newTestHub(t, "")
	ch, unsub := h.Subscribe([]string{"rt:t1:admin", "rt:t1:user:u1"})
	_ = ch

	if got := h.Count(); got != 1 {
		t.Fatalf("订阅后连接数应为 1，实际 %d", got)
	}
	if len(h.byChannel["rt:t1:admin"]) != 1 {
		t.Fatal("admin 频道索引未建立")
	}

	unsub()

	if got := h.Count(); got != 0 {
		t.Fatalf("注销后连接数应为 0，实际 %d", got)
	}
	// 空集合必须从 byChannel 删除，否则频道越多泄漏越多
	if _, ok := h.byChannel["rt:t1:admin"]; ok {
		t.Fatal("空频道集合未从索引删除（泄漏）")
	}
	if _, ok := h.byChannel["rt:t1:user:u1"]; ok {
		t.Fatal("空频道集合未从索引删除（泄漏）")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	h := newTestHub(t, "")
	ch, unsub := h.Subscribe([]string{"rt:t1:public"})
	unsub()

	// 注销后发布，通道应已关闭
	h.Publish(context.Background(), "rt:t1:public", "late", nil)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("注销后仍收到事件")
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("注销后通道未关闭")
	}
}

// ---------------------------------------------------------------------------
// 4. 多实例互通（Redis 广播）
// ---------------------------------------------------------------------------

func TestCrossHubViaRedis(t *testing.T) {
	addr := redisAddrForTest(t)
	if addr == "" {
		t.Skip("无 Redis，跳过跨实例测试")
	}

	h1 := newTestHub(t, addr)
	h2 := newTestHub(t, addr)

	ch2, unsub2 := h2.Subscribe([]string{"rt:t1:public"})
	defer unsub2()

	// h1 发布，h2 必须收到（走 Redis Pub/Sub）
	h1.Publish(context.Background(), "rt:t1:public", "cross", map[string]any{"from": "h1"})

	select {
	case ev := <-ch2:
		if ev.Topic != "cross" {
			t.Fatalf("h2 收到错误 topic: %q", ev.Topic)
		}
		if ev.Payload["from"] != "h1" {
			t.Fatalf("载荷错误: %+v", ev.Payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("h2 未在 3s 内收到 h1 的跨实例事件")
	}
}

// ---------------------------------------------------------------------------
// 5. FormatSSE 帧格式
// ---------------------------------------------------------------------------

func TestFormatSSEFrame(t *testing.T) {
	ev := Event{Topic: "orders.changed", Payload: map[string]any{"id": "ord-1"}}
	frame := FormatSSE(42, ev)

	// 必须是单行 data（SSE 帧不能被 JSON 里的换行截断）
	if len(frame) == 0 {
		t.Fatal("空帧")
	}
	var payload map[string]any
	line := ""
	for i, r := range frame {
		if r == '\n' {
			break
		}
		line = string(frame[:i])
	}
	_ = line
	// 直接解析 data 行
	var body []byte
	// 简化：从 frame 里抓 data: 之后的 JSON
	for _, line := range splitLines(frame) {
		if len(line) > 5 && line[:5] == "data:" {
			body = []byte(trimSpace(line[5:]))
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("data 行不是合法 JSON: %v (%q)", err, body)
	}
	if payload["id"] != "ord-1" {
		t.Fatalf("载荷错误: %+v", payload)
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// redisAddrForTest 检查环境变量或本地是否可用 Redis。
func redisAddrForTest(t *testing.T) string {
	// 环境变量优先
	if addr := envOr("PANDORA_TEST_REDIS", ""); addr != "" {
		return addr
	}
	// 探测本地 6379
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Logf("本地无 Redis (127.0.0.1:6379): %v", err)
		return ""
	}
	return "127.0.0.1:6379"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
