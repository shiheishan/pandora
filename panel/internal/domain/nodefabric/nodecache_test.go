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
		if v, err := c.get(ctx, "k", "", always[int], nil, load); err != nil || v != 1 {
			t.Fatalf("get #%d = %d, %v; want cached 1", i, v, err)
		}
	}
	clock.Advance(10*time.Second - time.Nanosecond)
	if v, _ := c.get(ctx, "k", "", always[int], nil, load); v != 1 || loads != 1 {
		t.Fatalf("entry reloaded before TTL: v=%d loads=%d", v, loads)
	}
	clock.Advance(time.Nanosecond)
	if v, _ := c.get(ctx, "k", "", always[int], nil, load); v != 2 || loads != 2 {
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
			func(set nodeUserSet) bool { return set.epoch >= want }, nil, load)
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
		if _, err := c.get(context.Background(), "k", "", always[int], nil, fail); err == nil {
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
			v, _ := c.get(context.Background(), "k", "7", always[int], nil, load)
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
		_, _ = c.get(ctx, k, "", always[int], nil, func(context.Context) (int, error) { return 1, nil })
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
	_, _ = svc.caches.users.get(context.Background(), usersCacheKey("t1", pool), "12", always[nodeUserSet], nil,
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
		"Service.claimNonceInDatabase", "Service.EffectiveConfigUnchangedAt", "Service.CurrentDeliveryEpoch", "Service.loadServingNodeForPush",
		"Service.loadServingNodesForPush", "Service.nodeUsers"} {
		if !strings.Contains(pkg.Decl(decl), "deliveryEpochSQL") {
			t.Errorf("%s no longer reads the delivery epoch", decl)
		}
	}
	if strings.Contains(pkg.Decl("Service.AuthenticateNode"), "caches") {
		t.Error("UniProxy token authentication must not be cached")
	}
}

// 用户集过了 TTL：宽限期内先回旧值、后台重算一次；过了宽限期同步重算；纪元落后照旧同步。
func TestTTLCacheServesStaleWhileRevalidating(t *testing.T) {
	clock := newFakeClock()
	c := newTTLCache[nodeUserSet](5*time.Second, 8, clock.Now)
	c.staleGrace = 10 * time.Second
	c.rank = func(s nodeUserSet) int64 { return s.epoch }
	var loads atomic.Int64
	release := make(chan struct{})
	load := func(context.Context) (nodeUserSet, error) {
		n := loads.Add(1)
		if n == 2 {
			<-release // 后台那一趟卡住，证明前台没在等它
		}
		return nodeUserSet{version: "v" + string(rune('0'+n)), epoch: 7}, nil
	}
	ctx := context.Background()
	valid := func(s nodeUserSet) bool { return s.epoch >= 7 }
	if v, _ := c.get(ctx, "k", "7", valid, nil, load); v.version != "v1" {
		t.Fatalf("first load = %+v", v)
	}
	clock.Advance(6 * time.Second)
	if v, err := c.get(ctx, "k", "7", valid, nil, load); err != nil || v.version != "v1" {
		t.Fatalf("stale entry not served while revalidating: %+v %v", v, err)
	}
	for start := time.Now(); loads.Load() < 2; time.Sleep(time.Millisecond) {
		if time.Since(start) > 2*time.Second {
			t.Fatal("no background refresh started")
		}
	}
	if v, _ := c.get(ctx, "k", "7", valid, nil, load); v.version != "v1" || loads.Load() != 2 {
		t.Fatalf("second stale read started another refresh: loads=%d", loads.Load())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if e, ok := c.peek("k"); ok && e.version == "v2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never replaced the entry")
		}
		time.Sleep(time.Millisecond)
	}
	// 过了宽限期：同步重算
	clock.Advance(20 * time.Second)
	if v, _ := c.get(ctx, "k", "7", valid, nil, load); v.version != "v3" {
		t.Fatalf("entry past the grace window was served: %+v", v)
	}
	// 纪元前进：即使在 TTL 内也同步重算，不回旧值
	if v, _ := c.get(ctx, "k", "9", func(s nodeUserSet) bool { return s.epoch >= 9 }, nil, func(context.Context) (nodeUserSet, error) {
		return nodeUserSet{version: "v9", epoch: 9}, nil
	}); v.version != "v9" {
		t.Fatalf("advanced epoch served a stale entry: %+v", v)
	}
	// 晚完成的旧纪元加载不把新条目换回去
	c.mu.Lock()
	c.storeLocked("k", nodeUserSet{version: "old", epoch: 3})
	c.mu.Unlock()
	if e, _ := c.peek("k"); e.version != "v9" {
		t.Fatalf("older load overwrote a newer entry: %+v", e)
	}
}

// 身份缓存的寿命不超过身份自己的 expires_at。
func TestIdentityCacheNeverOutlivesIdentityExpiry(t *testing.T) {
	clock := newFakeClock()
	caches := newNodeCaches(clock.Now)
	expires := clock.Now().Add(time.Minute)
	loads := 0
	load := func(context.Context) (Identity, error) {
		loads++
		return Identity{NodeID: "n", epoch: 1, expiresAt: expires}, nil
	}
	_, _ = caches.identity.get(context.Background(), "k", "0", always[Identity], nil, load)
	clock.Advance(59 * time.Second)
	_, _ = caches.identity.get(context.Background(), "k", "0", always[Identity], nil, load)
	if loads != 1 {
		t.Fatalf("identity reloaded before its expiry: %d", loads)
	}
	clock.Advance(time.Second)
	_, _ = caches.identity.get(context.Background(), "k", "0", always[Identity], nil, load)
	if loads != 2 {
		t.Fatal("identity served past its expires_at")
	}
	if nodeIdentityCacheTTL != 10*time.Minute || caches.identity.staleGrace != 0 {
		t.Fatal("identity cache: TTL is min(expires_at, 10 minutes) and never serves stale entries")
	}
}

// 用户集按名单里最早的订阅到期时刻硬过期：到点同步重算，不走「先回旧值」的宽限
// （到期没有写、不推进纪元，宽限会让刚到期的人多留 10 秒）。只到 TTL 的条目照旧宽限。
func TestNodeUserSetHardExpiresAtNextSubscriptionExpiry(t *testing.T) {
	clock := newFakeClock()
	caches := newNodeCaches(clock.Now)
	c := caches.users
	expiry := clock.Now().Add(2 * time.Second)
	loads := 0
	load := func(context.Context) (nodeUserSet, error) {
		loads++
		if loads == 1 {
			return nodeUserSet{epoch: 1, version: "with-expiring", nextExpiry: expiry}, nil
		}
		return nodeUserSet{epoch: 1, version: "after-expiry"}, nil
	}
	get := func() nodeUserSet {
		t.Helper()
		v, err := c.get(context.Background(), "pool", epochFlight(1),
			func(set nodeUserSet) bool { return set.epoch >= 1 }, nil, load)
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
		t.Fatalf("entry reloaded before the subscription expired: %+v loads=%d", v, loads)
	}
	clock.Advance(time.Second)
	if v := get(); v.version != "after-expiry" || loads != 2 {
		t.Fatalf("expired subscription served from the stale grace window: %+v loads=%d", v, loads)
	}
}

func TestNextExpiryHelpers(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	a, b := now.Add(time.Hour), now.Add(time.Minute)
	if got := earlierExpiry(time.Time{}, &a); !got.Equal(a) {
		t.Fatalf("first expiry = %v", got)
	}
	if got := earlierExpiry(a, &b); !got.Equal(b) {
		t.Fatalf("earlier expiry = %v", got)
	}
	if got := earlierExpiry(b, &a); !got.Equal(b) {
		t.Fatalf("later expiry replaced earlier: %v", got)
	}
	if got := earlierExpiry(b, nil); !got.Equal(b) {
		t.Fatalf("nil expiry changed result: %v", got)
	}
	if got := clampNextExpiry(time.Time{}, now); !got.IsZero() {
		t.Fatalf("no expiry must stay zero: %v", got)
	}
	if got := clampNextExpiry(now.Add(-time.Second), now); !got.Equal(now.Add(nextExpiryFloor)) {
		t.Fatalf("past expiry (clock skew) not floored: %v", got)
	}
	if got := clampNextExpiry(a, now); !got.Equal(a) {
		t.Fatalf("future expiry changed: %v", got)
	}
}

// 节点名单只收 active 账号的订阅（封禁即断、解封恢复），并带出到期时刻。
func TestNodeUsersRequireActiveOwner(t *testing.T) {
	list := sourcetest.Load(t, ".").Decl("Service.nodeUsers")
	for _, want := range []string{"AND s.user_id NOT IN (SELECT u.id FROM users u",
		"WHERE u.tenant_id = $1 AND u.status <> 'active')", "s.current_period_end", "earlierExpiry(set.nextExpiry, periodEnd)"} {
		if !strings.Contains(list, want) {
			t.Fatalf("node user list missing %q", want)
		}
	}
}
