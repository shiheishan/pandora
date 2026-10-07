package subscription

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// fakeClock 是可手动拨动的时钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestNodeCache(clock *fakeClock) *nodeCache {
	c := newNodeCache(nodeCacheTTL, 4)
	c.now = clock.Now
	return c
}

func countingFill(calls *atomic.Int32, name string) func(context.Context) ([]Node, error) {
	return func(context.Context) ([]Node, error) {
		calls.Add(1)
		return []Node{{Name: name}}, nil
	}
}

func TestNodeCacheHitsUntilTTL(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := newTestNodeCache(clock)
	key := nodeCacheKey{tenant: "t", planVersion: "pv", userGroup: ""}
	var calls atomic.Int32

	for range 3 {
		got, err := c.load(context.Background(), key, countingFill(&calls, "a"))
		if err != nil || len(got) != 1 || got[0].Name != "a" {
			t.Fatalf("load = %v, %v", got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fill called %d times within TTL, want 1", calls.Load())
	}
	// 用户组是键的一部分：同套餐版本、不同组要各查一次
	if _, err := c.load(context.Background(), nodeCacheKey{tenant: "t", planVersion: "pv", userGroup: "g"},
		countingFill(&calls, "g")); err != nil || calls.Load() != 2 {
		t.Fatalf("other user group must miss: calls=%d err=%v", calls.Load(), err)
	}
	clock.Add(nodeCacheTTL)
	if _, err := c.load(context.Background(), key, countingFill(&calls, "b")); err != nil || calls.Load() != 3 {
		t.Fatalf("entry must expire after TTL: calls=%d err=%v", calls.Load(), err)
	}
}

func TestNodeCacheInvalidateDropsTenantOnly(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := newTestNodeCache(clock)
	a := nodeCacheKey{tenant: "a", planVersion: "pv"}
	b := nodeCacheKey{tenant: "b", planVersion: "pv"}
	var calls atomic.Int32
	_, _ = c.load(context.Background(), a, countingFill(&calls, "a"))
	_, _ = c.load(context.Background(), b, countingFill(&calls, "b"))
	c.invalidate("a")
	_, _ = c.load(context.Background(), a, countingFill(&calls, "a2"))
	_, _ = c.load(context.Background(), b, countingFill(&calls, "b2"))
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want 3 (tenant a refilled, tenant b still cached)", calls.Load())
	}
}

// 现查进行中收到失效信号：查出来的那份是信号之前的旧答案，不能写回。
func TestNodeCacheDiscardsFillStartedBeforeInvalidation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := newTestNodeCache(clock)
	key := nodeCacheKey{tenant: "t", planVersion: "pv"}
	_, err := c.load(context.Background(), key, func(context.Context) ([]Node, error) {
		c.invalidate("t")
		return []Node{{Name: "stale"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	got, _ := c.load(context.Background(), key, countingFill(&calls, "fresh"))
	if calls.Load() != 1 || got[0].Name != "fresh" {
		t.Fatalf("stale fill was cached: calls=%d got=%v", calls.Load(), got)
	}
}

// 同一个键的并发未命中只查一次；现查失败时等待者自己再查，不继承别人的错误。
func TestNodeCacheCoalescesConcurrentMisses(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := newTestNodeCache(clock)
	key := nodeCacheKey{tenant: "t", planVersion: "pv"}
	release := make(chan struct{})
	var calls atomic.Int32
	leaderStarted := make(chan struct{})
	go func() {
		_, _ = c.load(context.Background(), key, func(context.Context) ([]Node, error) {
			calls.Add(1)
			close(leaderStarted)
			<-release
			return []Node{{Name: "leader"}}, nil
		})
	}()
	<-leaderStarted
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.load(context.Background(), key, countingFill(&calls, "waiter"))
			if err == nil && len(got) == 1 {
				results[i] = got[0].Name
			}
		}()
	}
	// 等待者都已挂在进行中的现查上，再放行
	waitUntil(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.flights[key] != nil
	})
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("fill called %d times, want 1", calls.Load())
	}
	for i, name := range results {
		if name != "leader" {
			t.Fatalf("waiter %d got %q, want leader's result", i, name)
		}
	}

	// 现查失败：等待者各自再查
	c.invalidate("t")
	failRelease := make(chan struct{})
	failStarted := make(chan struct{})
	go func() {
		_, _ = c.load(context.Background(), key, func(context.Context) ([]Node, error) {
			close(failStarted)
			<-failRelease
			return nil, context.Canceled
		})
	}()
	<-failStarted
	done := make(chan []Node, 1)
	go func() {
		got, _ := c.load(context.Background(), key, countingFill(&calls, "retry"))
		done <- got
	}()
	time.Sleep(20 * time.Millisecond)
	close(failRelease)
	if got := <-done; len(got) != 1 || got[0].Name != "retry" {
		t.Fatalf("waiter after a failed leader got %v, want its own fill", got)
	}
}

func TestNodeCacheIsBounded(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := newTestNodeCache(clock) // 上限 4
	var calls atomic.Int32
	for i := range 10 {
		key := nodeCacheKey{tenant: "t", planVersion: string(rune('a' + i))}
		if _, err := c.load(context.Background(), key, countingFill(&calls, "x")); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		n := len(c.entries)
		c.mu.Unlock()
		if n > 4 {
			t.Fatalf("cache grew to %d entries, limit 4", n)
		}
	}
}

func TestNilCachesPassThrough(t *testing.T) {
	var c *nodeCache
	var calls atomic.Int32
	for range 2 {
		if _, err := c.load(context.Background(), nodeCacheKey{}, countingFill(&calls, "x")); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("nil node cache must not cache")
	}
	var p *prefixCache
	p.put("t", "abc")
	if _, ok := p.get("t"); ok {
		t.Fatal("nil prefix cache must not cache")
	}
	wantErr := errors.New("boom")
	if _, err := c.load(context.Background(), nodeCacheKey{}, func(context.Context) ([]Node, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("nil cache must return fill error, got %v", err)
	}
}

func TestPrefixCacheTTLAndEmptyPrefix(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	p := newPrefixCache(prefixCacheTTL)
	p.now = clock.Now
	if _, ok := p.get("t"); ok {
		t.Fatal("empty cache must miss")
	}
	// 没配前缀（空串）同样缓存：扫描流量不能因此每次都打到库上
	p.put("t", "")
	if got, ok := p.get("t"); !ok || got != "" {
		t.Fatalf("empty prefix not cached: %q %v", got, ok)
	}
	p.put("t", "a1b2c3d4e5f6")
	clock.Add(prefixCacheTTL - time.Second)
	if got, ok := p.get("t"); !ok || got != "a1b2c3d4e5f6" {
		t.Fatalf("prefix expired early: %q %v", got, ok)
	}
	clock.Add(time.Second)
	if _, ok := p.get("t"); ok {
		t.Fatal("prefix must expire after TTL")
	}
}

// 节点变更信号经 hub 到达后，该租户的缓存立刻失效（没有 Valkey 时 hub 在本进程内分发）。
func TestNodeCacheInvalidatesOnNodeSignal(t *testing.T) {
	hub := realtime.NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer hub.Close()
	svc := New(nil, nil, nil)
	svc.AttachRealtime(hub)
	key := nodeCacheKey{tenant: "tenant-a", planVersion: "pv"}
	var calls atomic.Int32
	if _, err := svc.nodes.load(context.Background(), key, countingFill(&calls, "v1")); err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{realtime.TopicNodeConfigChanged, realtime.TopicNodeUsersChanged} {
		before := calls.Load()
		hub.Publish(context.Background(), realtime.ChannelNodeAll("tenant-a"), topic, map[string]any{})
		waitUntil(t, func() bool {
			svc.nodes.mu.Lock()
			defer svc.nodes.mu.Unlock()
			_, ok := svc.nodes.entries[key]
			return !ok
		})
		if _, err := svc.nodes.load(context.Background(), key, countingFill(&calls, topic)); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != before+1 {
			t.Fatalf("%s did not invalidate the cache", topic)
		}
	}
	// 别的租户的信号不动本租户
	before := calls.Load()
	hub.Publish(context.Background(), realtime.ChannelNodeAll("tenant-b"), realtime.TopicNodeUsersChanged, map[string]any{})
	time.Sleep(20 * time.Millisecond)
	if _, err := svc.nodes.load(context.Background(), key, countingFill(&calls, "other")); err != nil || calls.Load() != before {
		t.Fatalf("other tenant's signal invalidated this tenant: calls=%d err=%v", calls.Load(), err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(time.Millisecond)
	}
}
