package nodefabric

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/cache"
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

// 缓存路径：同池节点共用一份用户集，命中不碰库；版本是算好的那份；没进池的节点
// 直接得到空表。Service 没有连接池，任何一次回库都会 panic。
func TestListNodeUsersServesPoolCacheWithoutDatabase(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	pool := "pool-1"
	users := []ProxyUser{{ID: 1, UUID: "u-1"}, {ID: 2, UUID: "u-2"}}
	set := nodeUserSet{users: users, version: UserSetVersion(users), epoch: 12}
	_, _ = svc.caches.users.Get(context.Background(), usersCacheKey("t1", pool), "12", always[nodeUserSet], nil,
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

// 身份缓存的寿命不超过身份自己的 expires_at。
func TestIdentityCacheNeverOutlivesIdentityExpiry(t *testing.T) {
	clock := newFakeClock()
	caches := newNodeCaches(clock.Now)
	expires := clock.Now().Add(time.Minute)
	loads := 0
	load := func(context.Context) (Identity, error) {
		loads++
		return Identity{NodeID: "n", epoch: int64(loads), expiresAt: expires}, nil
	}
	_, _ = caches.identity.Get(context.Background(), "k", "0", always[Identity], nil, load)
	clock.Advance(59 * time.Second)
	_, _ = caches.identity.Get(context.Background(), "k", "0", always[Identity], nil, load)
	if loads != 1 {
		t.Fatalf("identity reloaded before its expiry: %d", loads)
	}
	clock.Advance(time.Second)
	_, _ = caches.identity.Get(context.Background(), "k", "0", always[Identity], nil, load)
	if loads != 2 {
		t.Fatal("identity served past its expires_at")
	}
	if nodeIdentityCacheTTL != 10*time.Minute {
		t.Fatal("identity cache: TTL is min(expires_at, 10 minutes)")
	}
	// 只到 TTL（身份本身还没到期）也同步重算：身份缓存不先回旧值
	expires = clock.Now().Add(time.Hour)
	_, _ = caches.identity.Get(context.Background(), "k", "0", always[Identity], nil, load)
	clock.Advance(nodeIdentityCacheTTL)
	if got, _ := caches.identity.Get(context.Background(), "k", "0", always[Identity], nil, load); loads != 4 || got.epoch != 4 {
		t.Fatalf("identity past its TTL served stale: loads=%d", loads)
	}
}

// 读纪元的常量与 platform/cache 的口径逐字相同。
func TestDeliveryEpochSQLMatchesPlatform(t *testing.T) {
	if deliveryEpochSQL != cache.EpochSQL(cache.NodeDeliveryEpoch) {
		t.Fatalf("deliveryEpochSQL = %s, platform = %s", deliveryEpochSQL, cache.EpochSQL(cache.NodeDeliveryEpoch))
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
		v, err := c.Get(context.Background(), "pool", epochFlight(1),
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

// 装配参数（newNodeCaches）钉在行为上：名单与身份都按纪元 rank，起得早、完成得晚的旧加载
// 不把已存的新条目换回去。删掉任一个 Rank，这条就红。
func TestNodeCachesRankKeepsNewerEntry(t *testing.T) {
	caches := newNodeCaches(newFakeClock().Now)
	ctx := context.Background()
	t.Run("users", func(t *testing.T) {
		raceOlderLoad(t, func(tag string, epoch int64, gate <-chan struct{}) {
			_, _ = caches.users.Get(ctx, "pool", tag, func(nodeUserSet) bool { return false }, nil,
				func(context.Context) (nodeUserSet, error) {
					<-gate
					return nodeUserSet{epoch: epoch}, nil
				})
		}, func() int64 { v, _ := caches.users.Peek("pool"); return v.epoch })
	})
	t.Run("identity", func(t *testing.T) {
		raceOlderLoad(t, func(tag string, epoch int64, gate <-chan struct{}) {
			_, _ = caches.identity.Get(ctx, "node", tag, func(Identity) bool { return false }, nil,
				func(context.Context) (Identity, error) {
					<-gate
					return Identity{epoch: epoch, expiresAt: time.Now().Add(time.Hour)}, nil
				})
		}, func() int64 { v, _ := caches.identity.Peek("node"); return v.epoch })
	})
}

// raceOlderLoad：纪元 5 的一趟先起、卡住；纪元 9 的一趟后起、先完成入库；再放行纪元 5 的那趟。
func raceOlderLoad(t *testing.T, get func(tag string, epoch int64, gate <-chan struct{}), stored func() int64) {
	t.Helper()
	oldGate, newGate := make(chan struct{}), make(chan struct{})
	oldDone := make(chan struct{})
	go func() { defer close(oldDone); get("old", 5, oldGate) }()
	time.Sleep(20 * time.Millisecond) // 旧的那趟已登记、卡在加载里
	close(newGate)
	get("new", 9, newGate)
	if got := stored(); got != 9 {
		t.Fatalf("newer load not stored: epoch %d", got)
	}
	close(oldGate)
	<-oldDone
	if got := stored(); got != 9 {
		t.Fatalf("an older load finishing late replaced the newer entry: epoch %d", got)
	}
}

// 名单：TTL 5 秒内直接命中；过了 TTL、在 TTL + 宽限 10 秒内先回旧值并在后台重算；过了宽限同步重算。
// 删掉 StaleGrace、或把 TTL / 宽限改大改小，这条就红。
func TestNodeUserSetTTLAndStaleGrace(t *testing.T) {
	clock := newFakeClock()
	caches := newNodeCaches(clock.Now)
	ctx := context.Background()
	var loads atomic.Int32
	gate := make(chan struct{}, 8)
	load := func(context.Context) (nodeUserSet, error) {
		n := loads.Add(1)
		if n > 1 {
			<-gate // 重算卡住：同步等它的请求会被看出来
		}
		return nodeUserSet{epoch: 1, version: "v" + string(rune('0'+n))}, nil
	}
	valid := func(set nodeUserSet) bool { return set.epoch >= 1 }
	get := func() nodeUserSet {
		t.Helper()
		v, err := caches.users.Get(ctx, "pool", "1", valid, nil, load)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	get()
	clock.Advance(nodeUsersCacheTTL - time.Millisecond)
	if v := get(); v.version != "v1" || loads.Load() != 1 {
		t.Fatalf("reloaded inside the TTL: %+v loads=%d", v, loads.Load())
	}
	if nodeUsersCacheTTL != 5*time.Second || nodeUsersStaleGrace != 10*time.Second {
		t.Fatalf("user set TTL/grace = %s/%s, want 5s/10s", nodeUsersCacheTTL, nodeUsersStaleGrace)
	}
	clock.Advance(2 * time.Millisecond) // 刚过 TTL：先回旧值，后台起一趟重算（卡在 gate 上）
	stale := make(chan nodeUserSet, 1)
	go func() { v, _ := caches.users.Get(ctx, "pool", "1", valid, nil, load); stale <- v }()
	select {
	case v := <-stale:
		if v.version != "v1" {
			t.Fatalf("past TTL inside the grace window must serve the old set: %+v", v)
		}
	case <-time.After(time.Second):
		gate <- struct{}{} // 放行那趟同步重算，免得卡住
		t.Fatal("past TTL inside the grace window the request waited for a synchronous reload")
	}
	for start := time.Now(); loads.Load() < 2; time.Sleep(time.Millisecond) {
		if time.Since(start) > 2*time.Second {
			t.Fatal("no background refresh after the TTL")
		}
	}
	gate <- struct{}{} // 放行后台那趟
	for start := time.Now(); ; time.Sleep(time.Millisecond) {
		if v, ok := caches.users.Peek("pool"); ok && v.version == "v2" {
			break
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("background refresh never replaced the set")
		}
	}
	// 过了 TTL + 宽限：同步重算，请求要等它（这里先放行一格，免得卡死）
	clock.Advance(nodeUsersCacheTTL + nodeUsersStaleGrace + time.Millisecond)
	gate <- struct{}{}
	if v := get(); v.version != "v3" {
		t.Fatalf("past TTL + grace must reload synchronously: %+v", v)
	}
}
