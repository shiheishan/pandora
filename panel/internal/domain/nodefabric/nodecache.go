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
// 负载占库时 80%。同一个池的 200 个节点拉到的是同一份用户表，两次变更之间它也
// 不变——缓存它，同池节点合成一次查询。签名请求的节点身份同理。
//
// 「改完立刻生效」靠下发纪元（迁移 00101 的 node_delivery_epoch 序列）：订阅、配额
// 用尽与否、流量包、套餐版本、池授权、用户组、系统设置、节点身份与节点状态一提交
// 就推进它。aegis-node 在本来就要跑的查询里顺手读出当前纪元（UniProxy 认证、拉生效
// 配置前的 nonce 认领、推送前读节点），缓存条目记着自己算出时的纪元，纪元前进了
// 就重算。所以无论改动来自后台、门户、定时任务还是直接执行的 SQL，下一次请求都
// 看得到，不用等 TTL，也不依赖 Pub/Sub 送达。
//
// TTL 只兜两类没有纪元的情况：不追踪的输入（在线设备记录、订阅到期的时间流逝），
// 以及读方恰好卡在写方「纪元已推进、数据还没提交可见」那道提交缝里算出的旧结果。
//
// 只在 aegis-node 里开（EnableNodeCaches）；只缓存成功结果。

const (
	// nodeUsersCacheTTL 是用户集缓存的寿命上限。到期与 strict 模式下的在线设备变化
	// 没有纪元，最坏延迟 = 这个 TTL + 节点拉取间隔。
	nodeUsersCacheTTL = 5 * time.Second
	// nodeIdentityCacheTTL 是签名身份缓存的寿命上限。吊销会推进纪元、下一次请求就
	// 回库，这个 TTL 只兜提交缝。
	nodeIdentityCacheTTL = 30 * time.Second

	nodeUsersCacheMax    = 1024 // 条目按（租户, 池）计
	nodeIdentityCacheMax = 8192 // 条目按（租户, 节点）计

	// nodeCacheLoadTimeout 是单飞加载的上限。加载不跟随发起者的请求取消：
	// 同一把钥匙上排着队的其他请求不该因为第一个人断开而一起失败。
	nodeCacheLoadTimeout = 15 * time.Second
)

// deliveryEpochSQL 读出当前下发纪元，嵌进各处本来就要跑的查询里，不单独多一次往返。
//
// 要加上 is_called：新建的序列 last_value=1、is_called=false，第一次 nextval 返回 1、
// last_value 还是 1，只把 is_called 翻成 true。只读 last_value 会漏掉全库的第一次推进。
const deliveryEpochSQL = `(SELECT last_value + is_called::int FROM node_delivery_epoch)`

type ttlEntry[V any] struct {
	value   V
	expires time.Time
}

type ttlFlight[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// ttlCache 是带上限、TTL、单飞合并的进程内缓存。条目是否仍可用由调用方的 valid
// 判断（比纪元）；不可用就重算，同一把钥匙、同一个单飞标签的并发请求只算一次。
type ttlCache[V any] struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
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

// get 命中且 valid 认可就返回；否则同一（key, flight）只放一个加载，其余等它的结果。
// flight 区分「要多新」：要求更新的请求不去搭为较旧要求发起的那趟车。
func (c *ttlCache[V]) get(ctx context.Context, key, flight string, valid func(V) bool,
	load func(context.Context) (V, error)) (V, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) && valid(e.value) {
		c.mu.Unlock()
		return e.value, nil
	}
	flightKey := key + "\x00" + flight
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
		if f.err == nil {
			c.storeLocked(key, f.value)
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

func (c *ttlCache[V]) storeLocked(key string, value V) {
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
	c.entries[key] = ttlEntry[V]{value: value, expires: now.Add(c.ttl)}
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

// drop 丢掉一条，返回它原先是否在缓存里。
func (c *ttlCache[V]) drop(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	delete(c.entries, key)
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

// nodeUserSet 是一个池当前该放行的用户、它的版本（UniProxy ETag 同源）与算出它时
// 的下发纪元。users 被多个请求共享，只读。
type nodeUserSet struct {
	users   []ProxyUser
	version string
	epoch   int64
}

// nodeCaches 是 aegis-node 进程的用户集缓存与签名身份缓存。
type nodeCaches struct {
	users    *ttlCache[nodeUserSet]
	identity *ttlCache[Identity]
}

func newNodeCaches(now func() time.Time) *nodeCaches {
	return &nodeCaches{
		users:    newTTLCache[nodeUserSet](nodeUsersCacheTTL, nodeUsersCacheMax, now),
		identity: newTTLCache[Identity](nodeIdentityCacheTTL, nodeIdentityCacheMax, now),
	}
}

// EnableNodeCaches 打开节点链路缓存。装配时调用一次（aegis-node）；其他进程不开，
// 照旧直查库。
func (s *Service) EnableNodeCaches() {
	if s.caches == nil {
		s.caches = newNodeCaches(nil)
	}
}

func usersCacheKey(tenantID, poolID string) string { return tenantID + "\x00" + poolID }

func identityCacheKey(tenantID, nodeID string) string { return tenantID + "\x00" + nodeID }

func epochFlight(epoch int64) string { return strconv.FormatInt(epoch, 10) }

// userSetVersionOf 取一份用户列表的版本：列表正是缓存里那一份时直接用算好的，
// 否则现算。按底层数组判断「同一份」，不比内容——比内容就等于又算一遍。
func (s *Service) userSetVersionOf(tenantID string, n *ServingNode, users []ProxyUser) string {
	if c := s.caches; c != nil && n != nil && n.PoolID != nil && len(users) > 0 {
		if set, ok := c.users.peek(usersCacheKey(tenantID, *n.PoolID)); ok &&
			len(set.users) == len(users) && &set.users[0] == &users[0] {
			return set.version
		}
	}
	return UserSetVersion(users)
}
