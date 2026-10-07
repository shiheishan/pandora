package nodefabric

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// 节点链路的进程内缓存。
//
// 5k 实测（200 节点、每节点 15 秒拉一次用户）：ListNodeUsers 每秒 13 次、纯节点
// 负载占库时 80%；签名请求每次都回库查身份，UniProxy 请求每次都回库验令牌。这三样
// 在两次变更之间都不变——同一个池的 200 个节点拉到的是同一份用户表，同一个节点
// 半分钟内的身份也不会变。所以按变更事件作废、再加 TTL 兜底，换掉「每请求一查」。
//
// 边界：
//   - 只在 aegis-node 里开（EnableNodeCaches），且只对作废订阅已建立的租户生效
//     （RunNodeCacheInvalidation 运行期间）。没有事件源就不缓存：宁可多查库，也不
//     让一份收不到作废通知的缓存只靠 TTL 撑着。
//   - 作废信号来自 platform/realtime：表级变更（aegis-public 的库监听经 Valkey 抄送
//     到管理频道）与节点频道上的显式通知。Pub/Sub 不保证送达，所以每样都有 TTL
//     上限——这就是「变更后最坏多久生效」：用户集 5 秒，认证 30 秒。
//   - 只缓存成功结果；认证失败、查库出错一律不进缓存，下一次照常回库。

const (
	// nodeUsersCacheTTL 是用户集缓存的寿命。配额用尽、到期这类没有事件的变化，
	// 最坏延迟 = 这个 TTL + 节点拉取间隔。5 秒让 200 个同池节点合成每 5 秒一查。
	nodeUsersCacheTTL = 5 * time.Second
	// nodeAuthCacheTTL 是令牌认证与签名身份的缓存寿命，也是「吊销后最多还能访问
	// 多久」的上限（收不到作废事件时）。
	nodeAuthCacheTTL = 30 * time.Second

	nodeUsersCacheMax    = 1024 // 条目按（租户, 池）计
	nodeAuthCacheMax     = 8192 // 条目按（租户, 节点, 令牌）计
	nodeIdentityCacheMax = 8192 // 条目按（租户, 节点）计

	// nodeCacheLoadTimeout 是单飞加载的上限。加载不跟随发起者的请求取消：
	// 同一把钥匙上排着队的其他请求不该因为第一个人断开而一起失败。
	nodeCacheLoadTimeout = 15 * time.Second
)

type ttlEntry[V any] struct {
	value   V
	group   string
	expires time.Time
}

type ttlFlight[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// ttlCache 是带上限、TTL、按组作废、单飞合并的进程内缓存。
//
// 作废与加载的竞态靠 epoch 解决：每次作废把 epoch 加一；加载开始时记下 epoch，
// 结束时 epoch 变过就只把结果交给这一批调用方、不写进缓存——否则一次在作废之前
// 读到旧数据的加载，会在作废之后把旧数据重新塞回去，一直留到 TTL。单飞的键也带
// epoch，作废之后进来的请求不会去搭作废之前那趟车。
type ttlCache[V any] struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
	epoch   uint64
	entries map[string]ttlEntry[V]
	flights map[string]*ttlFlight[V]
}

func newTTLCache[V any](ttl time.Duration, max int, now func() time.Time) *ttlCache[V] {
	if now == nil {
		now = time.Now
	}
	return &ttlCache[V]{ttl: ttl, max: max, now: now,
		entries: make(map[string]ttlEntry[V]), flights: make(map[string]*ttlFlight[V])}
}

// peek 只读缓存，不触发加载。
func (c *ttlCache[V]) peek(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// get 命中就返回；否则同一把钥匙只放一个加载，其余等它的结果。
func (c *ttlCache[V]) get(ctx context.Context, key, group string, load func(context.Context) (V, error)) (V, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.value, nil
	}
	epoch := c.epoch
	flightKey := key + "\x00" + strconv.FormatUint(epoch, 10)
	if f, ok := c.flights[flightKey]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.value, f.err
		case <-ctx.Done():
			var zero V
			return zero, ctx.Err()
		}
	}
	f := &ttlFlight[V]{done: make(chan struct{})}
	c.flights[flightKey] = f
	c.mu.Unlock()

	// 加载 panic 也要放行排队的人，不能让他们挂到各自的超时。
	defer func() {
		c.mu.Lock()
		delete(c.flights, flightKey)
		if f.err == nil && c.epoch == epoch {
			c.storeLocked(key, group, f.value)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.err = errNodeCacheLoadPanicked
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeCacheLoadTimeout)
	defer cancel()
	f.value, f.err = load(loadCtx)
	return f.value, f.err
}

func (c *ttlCache[V]) storeLocked(key, group string, value V) {
	now := c.now()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		// 还满就挤掉最早过期的一条。只在满载时走这条 O(n)，上限是几千条。
		if len(c.entries) >= c.max {
			var oldestKey string
			var oldest time.Time
			for k, e := range c.entries {
				if oldestKey == "" || e.expires.Before(oldest) {
					oldestKey, oldest = k, e.expires
				}
			}
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = ttlEntry[V]{value: value, group: group, expires: now.Add(c.ttl)}
}

// invalidateGroup 丢掉一组条目，并让正在进行的加载结果不再入缓存。
func (c *ttlCache[V]) invalidateGroup(group string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if e.group == group {
			delete(c.entries, k)
		}
	}
	c.epoch++
}

// drop 丢掉一条，返回它原先是否在缓存里。
func (c *ttlCache[V]) drop(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	delete(c.entries, key)
	c.epoch++
	return ok
}

func (c *ttlCache[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

type nodeCacheError string

func (e nodeCacheError) Error() string { return string(e) }

const errNodeCacheLoadPanicked = nodeCacheError("node cache load did not complete")

// nodeUserSet 是一个池当前该放行的用户与它的版本（UniProxy ETag 同源）。
// users 被多个请求共享，只读。
type nodeUserSet struct {
	users   []ProxyUser
	version string
}

// nodeCaches 是 aegis-node 进程的三份缓存与「哪些租户的作废订阅在跑」。
type nodeCaches struct {
	users    *ttlCache[nodeUserSet]
	auth     *ttlCache[ServingNode]
	identity *ttlCache[Identity]

	mu      sync.Mutex
	watched map[string]int
}

func newNodeCaches(now func() time.Time) *nodeCaches {
	return &nodeCaches{
		users:    newTTLCache[nodeUserSet](nodeUsersCacheTTL, nodeUsersCacheMax, now),
		auth:     newTTLCache[ServingNode](nodeAuthCacheTTL, nodeAuthCacheMax, now),
		identity: newTTLCache[Identity](nodeAuthCacheTTL, nodeIdentityCacheMax, now),
		watched:  make(map[string]int),
	}
}

// EnableNodeCaches 打开节点链路缓存。装配时调用一次（aegis-node），之后还要有
// RunNodeCacheInvalidation 在跑，对应租户的缓存才真正生效。
func (s *Service) EnableNodeCaches() {
	if s.caches == nil {
		s.caches = newNodeCaches(nil)
	}
}

// nodeCachesFor 返回该租户可用的缓存；没开或作废订阅不在跑时返回 nil，调用方直查库。
func (s *Service) nodeCachesFor(tenantID string) *nodeCaches {
	c := s.caches
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.watched[tenantID] == 0 {
		return nil
	}
	return c
}

func (c *nodeCaches) watch(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watched[tenantID]++
}

// unwatch 撤掉一个作废订阅；最后一个撤掉时清空该租户的缓存，免得下次开启时
// 捡到订阅中断期间没被作废的旧条目。
func (c *nodeCaches) unwatch(tenantID string) {
	c.mu.Lock()
	c.watched[tenantID]--
	last := c.watched[tenantID] <= 0
	if last {
		delete(c.watched, tenantID)
	}
	c.mu.Unlock()
	if last {
		c.invalidateTenant(tenantID)
	}
}

func usersCacheKey(tenantID, poolID string) string { return tenantID + "\x00" + poolID }

func nodeCacheGroup(tenantID, nodeID string) string { return tenantID + "\x00" + nodeID }

func authCacheKey(tenantID, nodeID string, tokenHash []byte) string {
	return tenantID + "\x00" + nodeID + "\x00" + string(tokenHash)
}

// invalidateUsers 作废租户下全部池的用户集（组 = 租户）。
func (c *nodeCaches) invalidateUsers(tenantID string) { c.users.invalidateGroup(tenantID) }

// invalidateNode 作废某个节点的令牌认证与签名身份。
func (c *nodeCaches) invalidateNode(tenantID, nodeID string) {
	group := nodeCacheGroup(tenantID, nodeID)
	c.auth.invalidateGroup(group)
	c.identity.invalidateGroup(group)
}

// invalidateTenant 清空租户的全部缓存。
func (c *nodeCaches) invalidateTenant(tenantID string) {
	c.users.invalidateGroup(tenantID)
	prefix := tenantID + "\x00"
	c.auth.invalidatePrefix(prefix)
	c.identity.invalidatePrefix(prefix)
}

// invalidatePrefix 丢掉组名以 prefix 开头的条目（按租户整体清空用）。
func (c *ttlCache[V]) invalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if len(e.group) >= len(prefix) && e.group[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
	c.epoch++
}

// invalidateNodeUsers 供推送路径在重算之前先作废：两个订阅者谁先处理同一条
// 事件没有保证，推送方自己作废一次，就不会拿到作废之前的旧列表。
func (s *Service) invalidateNodeUsers(tenantID string) {
	if c := s.caches; c != nil {
		c.invalidateUsers(tenantID)
	}
}

// userSetVersionOf 取一份用户列表的版本：列表正是缓存里那一份时直接用算好的，
// 否则现算。按底层数组判断「同一份」，不比内容——比内容就等于又算一遍。
func (s *Service) userSetVersionOf(tenantID string, n *ServingNode, users []ProxyUser) string {
	if c := s.nodeCachesFor(tenantID); c != nil && n != nil && n.PoolID != nil && len(users) > 0 {
		if set, ok := c.users.peek(usersCacheKey(tenantID, *n.PoolID)); ok &&
			len(set.users) == len(users) && &set.users[0] == &users[0] {
			return set.version
		}
	}
	return UserSetVersion(users)
}
