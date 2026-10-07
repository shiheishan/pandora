package nodefabric

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
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

func always[V any](V) bool { return true }

func TestTTLCacheServesUntilTTLThenReloads(t *testing.T) {
	clock := newFakeClock()
	c := newTTLCache[int](10*time.Second, 8, clock.Now)
	loads := 0
	load := func(context.Context) (int, error) { loads++; return loads, nil }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if v, err := c.get(ctx, "k", "", always[int], load); err != nil || v != 1 {
			t.Fatalf("get #%d = %d, %v; want cached 1", i, v, err)
		}
	}
	clock.Advance(10*time.Second - time.Nanosecond)
	if v, _ := c.get(ctx, "k", "", always[int], load); v != 1 || loads != 1 {
		t.Fatalf("entry reloaded before TTL: v=%d loads=%d", v, loads)
	}
	clock.Advance(time.Nanosecond)
	if v, _ := c.get(ctx, "k", "", always[int], load); v != 2 || loads != 2 {
		t.Fatalf("entry not reloaded at TTL: v=%d loads=%d", v, loads)
	}
}

// 纪元落后的条目不能用：调用方要求的纪元比条目新，就重算。
func TestTTLCacheReloadsWhenEntryIsOlderThanRequired(t *testing.T) {
	c := newTTLCache[nodeUserSet](time.Minute, 8, nil)
	epoch := int64(7)
	loads := 0
	load := func(context.Context) (nodeUserSet, error) {
		loads++
		return nodeUserSet{epoch: epoch, version: string(rune('a' + loads))}, nil
	}
	get := func(want int64) nodeUserSet {
		t.Helper()
		v, err := c.get(context.Background(), "pool", epochFlight(want),
			func(set nodeUserSet) bool { return set.epoch >= want }, load)
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

func TestTTLCacheDoesNotCacheErrors(t *testing.T) {
	c := newTTLCache[int](time.Minute, 8, nil)
	calls := 0
	fail := func(context.Context) (int, error) { calls++; return 0, errors.New("boom") }
	for i := 0; i < 2; i++ {
		if _, err := c.get(context.Background(), "k", "", always[int], fail); err == nil {
			t.Fatal("error swallowed")
		}
	}
	if calls != 2 || c.len() != 0 {
		t.Fatalf("failed load was cached: calls=%d entries=%d", calls, c.len())
	}
}

// 同一把钥匙、同一个单飞标签的并发请求只放一个加载，其余等它的结果。
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
			v, _ := c.get(context.Background(), "k", "7", always[int], load)
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

func TestTTLCacheStaysWithinBound(t *testing.T) {
	c := newTTLCache[int](time.Minute, 2, nil)
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c", "d"} {
		_, _ = c.get(ctx, k, "", always[int], func(context.Context) (int, error) { return 1, nil })
	}
	if n := c.len(); n > 2 {
		t.Fatalf("cache grew past its bound: %d", n)
	}
}

// 缓存路径：同池节点共用一份用户集，命中不碰库；版本是算好的那份；没进池的节点
// 直接得到空表。Service 没有连接池，任何一次回库都会 panic。
func TestListNodeUsersServesPoolCacheWithoutDatabase(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	pool := "pool-1"
	users := []ProxyUser{{ID: 1, UUID: "u-1"}, {ID: 2, UUID: "u-2"}}
	set := nodeUserSet{users: users, version: UserSetVersion(users), epoch: 12}
	_, _ = svc.caches.users.get(context.Background(), usersCacheKey("t1", pool), "12", always[nodeUserSet],
		func(context.Context) (nodeUserSet, error) { return set, nil })

	for _, nodeID := range []string{"n1", "n2"} {
		n := &ServingNode{ID: nodeID, PoolID: &pool, deliveryEpoch: 12, epochKnown: true}
		got, version, err := svc.NodeUserSet(context.Background(), "t1", n)
		if err != nil || len(got) != 2 || version != set.version {
			t.Fatalf("%s: users=%v version=%q err=%v", nodeID, got, version, err)
		}
	}
	got, err := svc.ListNodeUsers(context.Background(), "t1", &ServingNode{ID: "n3", epochKnown: true})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("pool-less node must get an empty list without a query: %v %v", got, err)
	}
}

// 吊销的读后写：认领时读到的纪元比缓存身份新，必须回库；没变就不回库。
func TestConfirmNodeIdentitySkipsOnlyWhenNothingChanged(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	if err := svc.ConfirmNodeIdentity(context.Background(), "t1", "n1",
		NodeSignatureCheck{epoch: 5, cached: true}, 5, nil, nil); err != nil {
		t.Fatalf("unchanged epoch must not re-check: %v", err)
	}
	if err := svc.ConfirmNodeIdentity(context.Background(), "t1", "n1",
		NodeSignatureCheck{epoch: 5}, 9, nil, nil); err != nil {
		t.Fatalf("an identity read fresh from the database needs no confirmation: %v", err)
	}
	// 纪元前进：会回库（这里没有连接池，用 panic 证明它确实去查了）
	defer func() {
		if recover() == nil {
			t.Fatal("advanced epoch did not trigger a fresh identity lookup")
		}
	}()
	_ = svc.ConfirmNodeIdentity(context.Background(), "t1", "n1",
		NodeSignatureCheck{epoch: 5, cached: true}, 6, nil, nil)
}

// 每个读缓存的入口都在同一条查询里读出纪元；UniProxy 认证不缓存（停用后下一次
// 请求必须 401）。
func TestDeliveryEpochIsReadAlongsideEveryCachedInput(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	for _, decl := range []string{"Service.AuthenticateNode", "Service.LookupIdentity",
		"Service.ClaimSignedRequestEpoch", "Service.loadServingNodeForPush",
		"Service.loadServingNodesForPush", "Service.ListNodeUsers"} {
		if !strings.Contains(pkg.Decl(decl), "deliveryEpochSQL") {
			t.Errorf("%s no longer reads the delivery epoch", decl)
		}
	}
	if strings.Contains(pkg.Decl("Service.AuthenticateNode"), "caches") {
		t.Error("UniProxy token authentication must not be cached")
	}
}
