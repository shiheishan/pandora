package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 下面的用例由 nodefabric/nodecache_test.go 与 epoch_watch_test.go 里测通用缓存的那几条原样搬来
// （断言不变，只把 ttlCache 换成 Cache），另加 Clear / Put 两条。

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

func always[V any](V) bool { return true }

// epochValue 是测试用的条目：带纪元、版本与自己的到期时刻。
type epochValue struct {
	epoch      int64
	version    string
	nextExpiry time.Time
}

func newCache[V any](ttl time.Duration, max int, now func() time.Time) *Cache[V] {
	return New(Options[V]{TTL: ttl, Max: max, Now: now})
}

func TestCacheServesUntilTTLThenReloads(t *testing.T) {
	clock := newFakeClock()
	c := newCache[int](10*time.Second, 8, clock.Now)
	loads := 0
	load := func(context.Context) (int, error) { loads++; return loads, nil }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := c.Get(ctx, "k", "", always[int], nil, load); err != nil || v != 1 {
			t.Fatalf("get #%d = %d, %v; want cached 1", i, v, err)
		}
	}
	clock.Advance(10*time.Second - time.Nanosecond)
	if v, _ := c.Get(ctx, "k", "", always[int], nil, load); v != 1 || loads != 1 {
		t.Fatalf("entry reloaded before TTL: v=%d loads=%d", v, loads)
	}
	clock.Advance(time.Nanosecond)
	if v, _ := c.Get(ctx, "k", "", always[int], nil, load); v != 2 || loads != 2 {
		t.Fatalf("entry not reloaded at TTL: v=%d loads=%d", v, loads)
	}
}

// 纪元落后的条目不能用：调用方要求的纪元比条目新，就重算。
func TestCacheReloadsWhenEntryIsOlderThanRequired(t *testing.T) {
	c := newCache[epochValue](time.Minute, 8, nil)
	epoch := int64(7)
	loads := 0
	load := func(context.Context) (epochValue, error) {
		loads++
		return epochValue{epoch: epoch, version: string(rune('a' + loads))}, nil
	}
	get := func(want int64) epochValue {
		t.Helper()
		v, err := c.Get(context.Background(), "pool", "f"+string(rune('0'+want)),
			func(v epochValue) bool { return v.epoch >= want }, nil, load)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if get(7).version != "b" || get(5).version != "b" || get(7).version != "b" || loads != 1 {
		t.Fatalf("entry at epoch 7 was not reused for requests at epoch <= 7: loads=%d", loads)
	}
	epoch = 9 // 写方推进了纪元
	if v := get(8); v.version != "c" || v.epoch != 9 || loads != 2 {
		t.Fatalf("stale entry served to a newer epoch: %+v loads=%d", v, loads)
	}
}

func TestCacheDoesNotCacheErrors(t *testing.T) {
	c := newCache[int](time.Minute, 8, nil)
	calls := 0
	fail := func(context.Context) (int, error) { calls++; return 0, errors.New("boom") }
	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "k", "", always[int], nil, fail); err == nil {
			t.Fatal("error swallowed")
		}
	}
	if calls != 2 || c.Len() != 0 {
		t.Fatalf("failed load was cached: calls=%d entries=%d", calls, c.Len())
	}
}

// 同一把钥匙、同一个单飞标签的并发请求只放一个加载，其余等它的结果。
func TestCacheSingleFlight(t *testing.T) {
	c := newCache[int](time.Minute, 8, nil)
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
			v, _ := c.Get(context.Background(), "k", "7", always[int], nil, load)
			results <- v
		}()
	}
	time.Sleep(50 * time.Millisecond)
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

func TestCacheStaysWithinBound(t *testing.T) {
	c := newCache[int](time.Minute, 2, nil)
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c", "d"} {
		_, _ = c.Get(ctx, k, "", always[int], nil, func(context.Context) (int, error) { return 1, nil })
	}
	if n := c.Len(); n > 2 {
		t.Fatalf("cache grew past its bound: %d", n)
	}
}

// 过了 TTL：宽限期内先回旧值、后台重算一次；过了宽限期同步重算；纪元落后照旧同步。
func TestCacheServesStaleWhileRevalidating(t *testing.T) {
	clock := newFakeClock()
	c := New(Options[epochValue]{TTL: 5 * time.Second, Max: 8, Now: clock.Now,
		StaleGrace: 10 * time.Second, Rank: func(v epochValue) int64 { return v.epoch }})
	var loads atomic.Int64
	release := make(chan struct{})
	load := func(context.Context) (epochValue, error) {
		n := loads.Add(1)
		if n == 2 {
			<-release // 后台那一趟卡住，证明前台没在等它
		}
		return epochValue{version: "v" + string(rune('0'+n)), epoch: 7}, nil
	}
	ctx := context.Background()
	valid := func(v epochValue) bool { return v.epoch >= 7 }
	if v, _ := c.Get(ctx, "k", "7", valid, nil, load); v.version != "v1" {
		t.Fatalf("first load = %+v", v)
	}
	clock.Advance(6 * time.Second)
	if v, err := c.Get(ctx, "k", "7", valid, nil, load); err != nil || v.version != "v1" {
		t.Fatalf("stale entry not served while revalidating: %+v %v", v, err)
	}
	for start := time.Now(); loads.Load() < 2; time.Sleep(time.Millisecond) {
		if time.Since(start) > 2*time.Second {
			t.Fatal("no background refresh started")
		}
	}
	if v, _ := c.Get(ctx, "k", "7", valid, nil, load); v.version != "v1" || loads.Load() != 2 {
		t.Fatalf("second stale read started another refresh: loads=%d", loads.Load())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if e, ok := c.Peek("k"); ok && e.version == "v2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never replaced the entry")
		}
		time.Sleep(time.Millisecond)
	}
	// 过了宽限期：同步重算
	clock.Advance(20 * time.Second)
	if v, _ := c.Get(ctx, "k", "7", valid, nil, load); v.version != "v3" {
		t.Fatalf("entry past the grace window was served: %+v", v)
	}
	// 纪元前进：即使在 TTL 内也同步重算，不回旧值
	if v, _ := c.Get(ctx, "k", "9", func(v epochValue) bool { return v.epoch >= 9 }, nil, func(context.Context) (epochValue, error) {
		return epochValue{version: "v9", epoch: 9}, nil
	}); v.version != "v9" {
		t.Fatalf("advanced epoch served a stale entry: %+v", v)
	}
	// 晚完成的旧纪元加载不把新条目换回去
	c.Put("k", epochValue{version: "old", epoch: 3})
	if e, _ := c.Peek("k"); e.version != "v9" {
		t.Fatalf("older load overwrote a newer entry: %+v", e)
	}
}

// 条目自己的到期时刻与 TTL 取早者。
func TestCacheNeverOutlivesEntryExpiry(t *testing.T) {
	clock := newFakeClock()
	expires := clock.Now().Add(time.Minute)
	c := New(Options[epochValue]{TTL: 10 * time.Minute, Max: 8, Now: clock.Now,
		Expiry: func(v epochValue) time.Time { return v.nextExpiry }})
	loads := 0
	load := func(context.Context) (epochValue, error) {
		loads++
		return epochValue{epoch: 1, nextExpiry: expires}, nil
	}
	_, _ = c.Get(context.Background(), "k", "0", always[epochValue], nil, load)
	clock.Advance(59 * time.Second)
	_, _ = c.Get(context.Background(), "k", "0", always[epochValue], nil, load)
	if loads != 1 {
		t.Fatalf("entry reloaded before its expiry: %d", loads)
	}
	clock.Advance(time.Second)
	_, _ = c.Get(context.Background(), "k", "0", always[epochValue], nil, load)
	if loads != 2 {
		t.Fatal("entry served past its own expiry")
	}
}

// 按自己的到期时刻硬过期：到点同步重算，不走「先回旧值」的宽限。只到 TTL 的条目照旧宽限。
func TestCacheHardExpiresAtEntryExpiry(t *testing.T) {
	clock := newFakeClock()
	c := New(Options[epochValue]{TTL: 5 * time.Second, Max: 8, Now: clock.Now, StaleGrace: 10 * time.Second,
		Expiry: func(v epochValue) time.Time { return v.nextExpiry }})
	expiry := clock.Now().Add(2 * time.Second)
	loads := 0
	load := func(context.Context) (epochValue, error) {
		loads++
		if loads == 1 {
			return epochValue{epoch: 1, version: "with-expiring", nextExpiry: expiry}, nil
		}
		return epochValue{epoch: 1, version: "after-expiry"}, nil
	}
	get := func() epochValue {
		t.Helper()
		v, err := c.Get(context.Background(), "pool", "1", func(v epochValue) bool { return v.epoch >= 1 }, nil, load)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := get(); v.version != "with-expiring" {
		t.Fatalf("first load = %+v", v)
	}
	clock.Advance(time.Second)
	if v := get(); v.version != "with-expiring" || loads != 1 {
		t.Fatalf("entry reloaded before it expired: %+v loads=%d", v, loads)
	}
	clock.Advance(time.Second)
	if v := get(); v.version != "after-expiry" || loads != 2 {
		t.Fatalf("expired entry served from the stale grace window: %+v loads=%d", v, loads)
	}
}

// pinned 认可的条目不看 TTL；条目自己的到期时刻照样作数。
func TestCachePinnedEntriesIgnoreTTLButNotHardExpiry(t *testing.T) {
	clock := newFakeClock()
	c := New(Options[epochValue]{TTL: 5 * time.Second, Max: 8, Now: clock.Now,
		Expiry: func(v epochValue) time.Time { return v.nextExpiry }})
	loads := 0
	hard := clock.Now().Add(time.Minute)
	load := func(context.Context) (epochValue, error) {
		loads++
		return epochValue{version: "v", nextExpiry: hard}, nil
	}
	get := func(pinned func(epochValue) bool) {
		t.Helper()
		if _, err := c.Get(context.Background(), "k", "", always[epochValue], pinned, load); err != nil {
			t.Fatal(err)
		}
	}
	get(always[epochValue])
	clock.Advance(30 * time.Second) // 过了 TTL，没到 hard
	get(always[epochValue])
	if loads != 1 {
		t.Fatalf("pinned entry reloaded before its hard expiry: loads=%d", loads)
	}
	get(nil) // 不 pinned 时照旧按 TTL 重算
	if loads != 2 {
		t.Fatalf("unpinned entry past TTL was not reloaded: loads=%d", loads)
	}
	clock.Advance(2 * time.Minute) // 过了 hard
	hard = clock.Now().Add(time.Minute)
	get(always[epochValue])
	if loads != 3 {
		t.Fatalf("pinned entry served past its hard expiry: loads=%d", loads)
	}
}

// 过了到期时刻就同步重算：pinned 路径与宽限路径都不能再回旧值（到期晚于 TTL、入库时 hard=false）。
func TestCachePastExpiryNeverServedStale(t *testing.T) {
	for name, pinned := range map[string]func(epochValue) bool{
		"pinned": always[epochValue],
		"grace":  func(epochValue) bool { return false },
	} {
		clock := newFakeClock()
		c := New(Options[epochValue]{TTL: 5 * time.Second, Max: 8, Now: clock.Now, StaleGrace: 10 * time.Second,
			Expiry: func(v epochValue) time.Time { return v.nextExpiry }})
		expiry := clock.Now().Add(8 * time.Second)
		loads := 0
		load := func(context.Context) (epochValue, error) {
			loads++
			if loads == 1 {
				return epochValue{version: "with-expiring", nextExpiry: expiry}, nil
			}
			return epochValue{version: "after-expiry"}, nil
		}
		if got, _ := c.Get(context.Background(), "k", "f", always[epochValue], pinned, load); got.version != "with-expiring" {
			t.Fatalf("%s: first load %q", name, got.version)
		}
		clock.Advance(9 * time.Second) // 过了到期 1 秒，仍在 TTL + StaleGrace 之内
		if got, _ := c.Get(context.Background(), "k", "f", always[epochValue], pinned, load); got.version != "after-expiry" {
			t.Fatalf("%s: entry served past its expiry: %q", name, got.version)
		}
	}
}

// Clear 清空条目并作废正在进行的加载：等着的人照常拿到结果，但结果不写回；
// Clear 之后来的请求不搭旧车，自己重新加载。
func TestCacheClearVoidsInFlightLoads(t *testing.T) {
	c := newCache[string](time.Minute, 8, nil)
	ctx := context.Background()
	_, _ = c.Get(ctx, "a", "", always[string], nil, func(context.Context) (string, error) { return "a1", nil })

	started, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan string)
	go func() {
		v, _ := c.Get(ctx, "k", "", always[string], nil, func(context.Context) (string, error) {
			close(started)
			<-release
			return "before-clear", nil
		})
		oldDone <- v
	}()
	<-started
	c.Clear()
	if c.Len() != 0 {
		t.Fatalf("Clear left %d entries", c.Len())
	}
	// Clear 之后的请求自己加载，不等旧的那一趟
	if v, _ := c.Get(ctx, "k", "", always[string], nil, func(context.Context) (string, error) {
		return "after-clear", nil
	}); v != "after-clear" {
		t.Fatalf("request after Clear joined a pre-Clear load: %q", v)
	}
	close(release)
	if v := <-oldDone; v != "before-clear" {
		t.Fatalf("waiter of the voided load got %q", v)
	}
	if v, ok := c.Peek("k"); !ok || v != "after-clear" {
		t.Fatalf("voided load was written back over the fresh entry: %q %v", v, ok)
	}
}

func TestCacheDropAndPut(t *testing.T) {
	clock := newFakeClock()
	c := newCache[int](time.Minute, 8, clock.Now)
	c.Put("k", 1)
	if v, ok := c.Peek("k"); !ok || v != 1 {
		t.Fatalf("Put not visible: %d %v", v, ok)
	}
	if !c.Drop("k") || c.Drop("k") {
		t.Fatal("Drop must report whether the entry was present")
	}
	c.Put("k", 2)
	clock.Advance(time.Minute)
	if _, ok := c.Peek("k"); ok {
		t.Fatal("Peek returned an entry past its TTL")
	}
}

func TestNewRejectsUnboundedOptions(t *testing.T) {
	for _, opts := range []Options[int]{{TTL: 0, Max: 1}, {TTL: time.Second, Max: 0}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%+v) accepted an unbounded cache", opts)
				}
			}()
			New(opts)
		}()
	}
}
