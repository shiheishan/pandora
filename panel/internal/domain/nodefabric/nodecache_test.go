package nodefabric

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestTTLCacheServesUntilTTLThenReloads(t *testing.T) {
	clock := newFakeClock()
	c := newTTLCache[int](10*time.Second, 8, clock.Now)
	loads := 0
	load := func(context.Context) (int, error) { loads++; return loads, nil }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := c.get(ctx, "k", "g", load); err != nil || v != 1 {
			t.Fatalf("get #%d = %d, %v; want cached 1", i, v, err)
		}
	}
	clock.Advance(10*time.Second - time.Nanosecond)
	if v, _ := c.get(ctx, "k", "g", load); v != 1 || loads != 1 {
		t.Fatalf("entry reloaded before TTL: v=%d loads=%d", v, loads)
	}
	clock.Advance(time.Nanosecond)
	if v, _ := c.get(ctx, "k", "g", load); v != 2 || loads != 2 {
		t.Fatalf("entry not reloaded at TTL: v=%d loads=%d", v, loads)
	}
}

func TestTTLCacheDoesNotCacheErrors(t *testing.T) {
	c := newTTLCache[int](time.Minute, 8, nil)
	calls := 0
	fail := func(context.Context) (int, error) { calls++; return 0, errors.New("boom") }
	for i := 0; i < 2; i++ {
		if _, err := c.get(context.Background(), "k", "g", fail); err == nil {
			t.Fatal("error swallowed")
		}
	}
	if calls != 2 || c.len() != 0 {
		t.Fatalf("failed load was cached: calls=%d entries=%d", calls, c.len())
	}
}

// 同一把钥匙的并发请求只放一个加载，其余等它的结果。
func TestTTLCacheSingleFlight(t *testing.T) {
	c := newTTLCache[int](time.Minute, 8, nil)
	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 42, nil
	}
	const workers = 16
	var wg sync.WaitGroup
	results := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, _ := c.get(context.Background(), "k", "g", load)
			results <- v
		}()
	}
	// 等所有人都排上队再放行
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		queued := len(c.flights)
		c.mu.Unlock()
		if queued == 1 && calls.Load() == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for v := range results {
		if v != 42 {
			t.Fatalf("joiner got %d", v)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("load ran %d times for one key, want 1", calls.Load())
	}
}

// 加载期间发生的作废：结果交给这一批调用方，但不写进缓存，下一次重新加载。
func TestTTLCacheInvalidationDuringLoadIsNotStored(t *testing.T) {
	c := newTTLCache[int](time.Minute, 8, nil)
	loads := 0
	load := func(context.Context) (int, error) {
		loads++
		if loads == 1 {
			c.invalidateGroup("g") // 作废发生在读库之后、写缓存之前
		}
		return loads, nil
	}
	if v, _ := c.get(context.Background(), "k", "g", load); v != 1 {
		t.Fatalf("first get = %d", v)
	}
	if v, _ := c.get(context.Background(), "k", "g", load); v != 2 {
		t.Fatalf("stale result survived an invalidation that raced the load: got %d", v)
	}
}

func TestTTLCacheGroupInvalidationAndBound(t *testing.T) {
	c := newTTLCache[int](time.Minute, 2, nil)
	ctx := context.Background()
	value := func(v int) func(context.Context) (int, error) {
		return func(context.Context) (int, error) { return v, nil }
	}
	_, _ = c.get(ctx, "a1", "a", value(1))
	_, _ = c.get(ctx, "b1", "b", value(2))
	c.invalidateGroup("a")
	if _, ok := c.peek("a1"); ok {
		t.Fatal("group invalidation left the entry")
	}
	if _, ok := c.peek("b1"); !ok {
		t.Fatal("group invalidation removed another group")
	}
	_, _ = c.get(ctx, "c1", "c", value(3))
	_, _ = c.get(ctx, "d1", "d", value(4))
	if n := c.len(); n > 2 {
		t.Fatalf("cache grew past its bound: %d", n)
	}
}

// 吊销的上限：收不到作废事件时，缓存里的身份最多再用一个 TTL。
func TestIdentityCacheBoundsRevocationToOneTTL(t *testing.T) {
	clock := newFakeClock()
	caches := newNodeCaches(clock.Now)
	revoked := false
	load := func(context.Context) (Identity, error) {
		if revoked {
			return Identity{}, errors.New("节点身份无效")
		}
		return Identity{NodeID: "n1", Serial: 1, Status: "active"}, nil
	}
	key := nodeCacheGroup("t1", "n1")
	if _, err := caches.identity.get(context.Background(), key, key, load); err != nil {
		t.Fatal(err)
	}
	revoked = true // 库里已吊销，但没有事件
	clock.Advance(nodeAuthCacheTTL - time.Second)
	if _, err := caches.identity.get(context.Background(), key, key, load); err != nil {
		t.Fatalf("within TTL the cached identity should still be served: %v", err)
	}
	clock.Advance(time.Second)
	if _, err := caches.identity.get(context.Background(), key, key, load); err == nil {
		t.Fatal("revoked identity still accepted after one TTL")
	}
}

// 节点行变更（退役、状态、令牌、换池）与节点频道通知立即作废认证；订阅类表去抖
// 作废用户集；显式的 node.users.changed 立即作废用户集。
func TestApplyNodeCacheEvent(t *testing.T) {
	caches := newNodeCaches(nil)
	ctx := context.Background()
	seed := func() {
		ok := func(context.Context) (Identity, error) { return Identity{NodeID: "n1"}, nil }
		_, _ = caches.identity.get(ctx, nodeCacheGroup("t1", "n1"), nodeCacheGroup("t1", "n1"), ok)
		_, _ = caches.auth.get(ctx, authCacheKey("t1", "n1", []byte("h")), nodeCacheGroup("t1", "n1"),
			func(context.Context) (ServingNode, error) { return ServingNode{ID: "n1"}, nil })
	}
	seed()
	if got := applyNodeCacheEvent(caches, "t1", realtime.Event{Topic: "nodes.changed",
		Payload: map[string]any{"table": "nodes", "op": "UPDATE", "id": "n1"}}); got != usersUntouched {
		t.Fatalf("nodes event touched users: %v", got)
	}
	if caches.identity.len() != 0 || caches.auth.len() != 0 {
		t.Fatal("nodes row change did not invalidate the node's auth caches")
	}
	seed()
	applyNodeCacheEvent(caches, "t1", realtime.Event{Topic: realtime.TopicNodeConfigChanged,
		Payload: map[string]any{"node_id": "n1"}})
	if caches.identity.len() != 0 || caches.auth.len() != 0 {
		t.Fatal("node config notification did not invalidate the node's auth caches")
	}
	for _, table := range []string{"subscriptions", "plan_versions", "traffic_pack_grants"} {
		if got := applyNodeCacheEvent(caches, "t1", realtime.Event{Topic: "x",
			Payload: map[string]any{"table": table}}); got != usersDebounced {
			t.Fatalf("%s change = %v, want debounced users invalidation", table, got)
		}
	}
	if got := applyNodeCacheEvent(caches, "t1", realtime.Event{Topic: "x",
		Payload: map[string]any{"table": "tickets"}}); got != usersUntouched {
		t.Fatalf("unrelated table invalidated users: %v", got)
	}
	if got := applyNodeCacheEvent(caches, "t1", realtime.Event{Topic: realtime.TopicNodeUsersChanged,
		Payload: map[string]any{}}); got != usersNow {
		t.Fatalf("explicit users change = %v, want immediate", got)
	}
}

func TestUsersDebouncerLeadingThenTrailing(t *testing.T) {
	clock := newFakeClock()
	d := newUsersDebouncer(2*time.Second, clock.Now)
	if now, _ := d.event(); !now {
		t.Fatal("first event must invalidate immediately")
	}
	clock.Advance(500 * time.Millisecond)
	now, wait := d.event()
	if now || wait != 1500*time.Millisecond {
		t.Fatalf("second event = now:%v wait:%s; want trailing in 1.5s", now, wait)
	}
	if now, wait := d.event(); now || wait != 0 {
		t.Fatalf("third event must merge into the pending trailing one: now:%v wait:%s", now, wait)
	}
	clock.Advance(1500 * time.Millisecond)
	if !d.fire() {
		t.Fatal("trailing invalidation lost")
	}
	if d.fire() {
		t.Fatal("trailing invalidation fired twice")
	}
	clock.Advance(2 * time.Second)
	if now, _ := d.event(); !now {
		t.Fatal("event after a quiet gap must invalidate immediately")
	}
}

// 缓存路径：同池节点共用一份用户集，命中不碰库；版本是算好的那份；没进池的节点
// 直接得到空表。Service 没有连接池，任何一次回库都会 panic。
func TestListNodeUsersServesPoolCacheWithoutDatabase(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	svc.caches.watch("t1")
	pool := "pool-1"
	users := []ProxyUser{{ID: 1, UUID: "u-1"}, {ID: 2, UUID: "u-2"}}
	set := nodeUserSet{users: users, version: UserSetVersion(users)}
	_, _ = svc.caches.users.get(context.Background(), usersCacheKey("t1", pool), "t1",
		func(context.Context) (nodeUserSet, error) { return set, nil })

	for _, nodeID := range []string{"n1", "n2"} {
		got, version, err := svc.NodeUserSet(context.Background(), "t1", &ServingNode{ID: nodeID, PoolID: &pool})
		if err != nil || len(got) != 2 || version != set.version {
			t.Fatalf("%s: users=%v version=%q err=%v", nodeID, got, version, err)
		}
	}
	got, err := svc.ListNodeUsers(context.Background(), "t1", &ServingNode{ID: "n3"})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("pool-less node must get an empty list without a query: %v %v", got, err)
	}
	// 作废订阅不在跑的租户不走缓存（这里会回库，所以只检查开关本身）
	if svc.nodeCachesFor("t2") != nil {
		t.Fatal("cache enabled for a tenant without an invalidation subscription")
	}
}

// 令牌认证命中缓存：交出去的是副本，协议漂移按每次请求自己的声明算。
func TestAuthenticateNodeServesCachedCopy(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	svc.caches.watch("t1")
	cached := ServingNode{ID: "n1", NodeType: "vless", Protocol: []byte(`{"a":1}`)}
	key := authCacheKey("t1", "n1", crypto.HashToken("secret"))
	_, _ = svc.caches.auth.get(context.Background(), key, nodeCacheGroup("t1", "n1"),
		func(context.Context) (ServingNode, error) { return cached, nil })

	first, err := svc.AuthenticateNode(context.Background(), "t1", "n1", "secret", "shadowsocks")
	if err != nil || first.DeclaredType != "shadowsocks" {
		t.Fatalf("first auth = %+v, %v", first, err)
	}
	first.Protocol[0] = 'X'
	first.Routes = []NodeRoute{{OutboundTag: "block"}}
	second, err := svc.AuthenticateNode(context.Background(), "t1", "n1", "secret", "vless")
	if err != nil || second.DeclaredType != "" || string(second.Protocol) != `{"a":1}` || second.Routes != nil {
		t.Fatalf("cached node leaked a previous caller's mutation: %+v, %v", second, err)
	}
	if _, err := svc.AuthenticateNode(context.Background(), "t1", "", "secret", ""); err == nil {
		t.Fatal("missing node_id accepted")
	}
}

// 作废订阅端到端：起订阅后缓存才开；表级与节点级事件按规则作废；退出时关闭并清空。
func TestRunNodeCacheInvalidationLifecycle(t *testing.T) {
	hub := realtime.NewHub(nil, slog.Default())
	defer hub.Close()
	svc := NewService(nil, nil)
	svc.AttachRealtime(hub)
	svc.EnableNodeCaches()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunNodeCacheInvalidation(ctx, "t1", slog.Default())
	}()
	waitFor(t, "cache enabled", func() bool { return svc.nodeCachesFor("t1") != nil })

	pool := "p1"
	seedUsers := func() {
		_, _ = svc.caches.users.get(context.Background(), usersCacheKey("t1", pool), "t1",
			func(context.Context) (nodeUserSet, error) { return nodeUserSet{version: "v"}, nil })
	}
	seedUsers()
	hub.Publish(context.Background(), realtime.ChannelAdmin("t1"), "subscriptions.changed",
		map[string]any{"table": "subscriptions", "op": "UPDATE"})
	waitFor(t, "users invalidated by subscription change", func() bool { return svc.caches.users.len() == 0 })

	seedUsers()
	hub.Publish(context.Background(), realtime.ChannelNodeAll("t1"), realtime.TopicNodeUsersChanged, map[string]any{})
	waitFor(t, "users invalidated by node.users.changed", func() bool { return svc.caches.users.len() == 0 })

	_, _ = svc.caches.identity.get(context.Background(), nodeCacheGroup("t1", "n1"), nodeCacheGroup("t1", "n1"),
		func(context.Context) (Identity, error) { return Identity{NodeID: "n1"}, nil })
	hub.Publish(context.Background(), realtime.ChannelAdmin("t1"), "nodes.changed",
		map[string]any{"table": "nodes", "op": "UPDATE", "id": "n1"})
	waitFor(t, "identity invalidated by nodes change", func() bool { return svc.caches.identity.len() == 0 })

	seedUsers()
	cancel()
	<-done
	if svc.nodeCachesFor("t1") != nil {
		t.Fatal("cache still enabled after the invalidation subscription stopped")
	}
	if svc.caches.users.len() != 0 {
		t.Fatal("stopping the subscription left entries that no longer receive invalidations")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
