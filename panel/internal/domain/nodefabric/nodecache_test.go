package nodefabric

import (
	"context"
	"strings"
	"sync"
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
