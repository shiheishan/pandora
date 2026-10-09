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
// TTL 只兜两类没有纪元的情况：不追踪的输入（在线设备记录），以及读方恰好卡在写方
// 「纪元已推进、数据还没提交可见」那道提交缝里算出的旧结果。订阅到期的时间流逝没有
// 写，用户集条目记着名单里最早的到期时刻（nextExpiry），到点硬过期、同步重算，不走
// 「先回旧值」的宽限。
//
// 只在 aegis-node 里开（EnableNodeCaches）；只缓存成功结果。

const (
	// nodeUsersCacheTTL 是用户集缓存的寿命上限。strict 模式下的在线设备变化没有纪元，
	// 最坏延迟 = 这个 TTL + nodeUsersStaleGrace + 节点拉取间隔。订阅到期按条目的
	// nextExpiry 硬过期，不受这一条约束。
	nodeUsersCacheTTL = 5 * time.Second
	// nodeUsersStaleGrace 是用户集过了 TTL 之后还能先回旧值的窗口：先回旧值、后台单飞
	// 重算（stale-while-revalidate），请求不再每 5 秒同步等一次 30 多毫秒的名单查询。
	// 只对「TTL 到了」生效；纪元前进（任何已提交的下发相关改动）照旧同步重算。
	// 超过这个窗口还没人来取的旧值不再回，下一次请求同步重算。
	nodeUsersStaleGrace = 10 * time.Second
	// nodeIdentityCacheTTL 是签名身份缓存的寿命上限，且不超过身份自己的 expires_at。
	// 吊销、重新接入、节点退役或改服务状态都会推进纪元、下一次请求就回库，这个 TTL
	// 只兜「纪元已推进、数据还没提交可见」那道提交缝。
	nodeIdentityCacheTTL = 10 * time.Minute

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
	// hard 表示 expires 来自条目自己的到期时刻（expiry），而不是 TTL：过了就不能
	// 再当旧值先回（staleGrace 只对 TTL 到期生效）。
	hard bool
	// hardAt 是条目自己的到期时刻（expiry），零值表示没有。pinned 的条目不看 TTL，
	// 但这个时刻照样作数（身份到期、名单里最早的订阅到期）。
	hardAt time.Time
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

	// expiry 给条目一个自己的到期时刻（如身份的 expires_at），与 ttl 取早者；可为 nil。
	expiry func(V) time.Time
	// staleGrace > 0 时，TTL 到期但 valid 仍认可的条目在这个窗口里先回旧值、后台重算；
	// 按 expiry 硬过期的条目不在此列。
	staleGrace time.Duration
	// rank 是条目的新旧（纪元）；晚完成的旧加载不覆盖已存的新条目。可为 nil。
	rank func(V) int64

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
//
// pinned 非空且认可条目时不看 TTL（纪元监听证明加载之后没有相关提交，见 epoch_watch.go），
// 只看条目自己的到期时刻（hardAt）。
func (c *ttlCache[V]) get(ctx context.Context, key, flight string, valid, pinned func(V) bool,
	load func(context.Context) (V, error)) (V, error) {
	flightKey := key + "\x00" + flight
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && valid(e.value) {
		now := c.now()
		if now.Before(e.expires) || (pinned != nil && pinned(e.value) && (e.hardAt.IsZero() || now.Before(e.hardAt))) {
			c.mu.Unlock()
			return e.value, nil
		}
		if c.staleGrace > 0 && !e.hard && now.Before(e.expires.Add(c.staleGrace)) {
			// 先回旧值；没有同标签的加载在跑就起一个后台加载，跑完替换条目。
			if _, busy := c.flights[flightKey]; !busy {
				f := &ttlFlight[V]{done: make(chan struct{})}
				c.flights[flightKey] = f
				go c.run(context.Background(), key, flightKey, f, load)
			}
			c.mu.Unlock()
			return e.value, nil
		}
	}
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
	c.run(ctx, key, flightKey, f, load)
	return f.value, f.err
}

// run 执行一趟已登记的加载，结束后放行排队的人并按需存下结果。
func (c *ttlCache[V]) run(ctx context.Context, key, flightKey string, f *ttlFlight[V],
	load func(context.Context) (V, error)) {
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
	if old, ok := c.entries[key]; ok && c.rank != nil && c.rank(old.value) > c.rank(value) {
		return // 一趟起得早、完成得晚的加载，不能把已存的较新条目换回旧的
	}
	expires, hard := now.Add(c.ttl), false
	var hardAt time.Time
	if c.expiry != nil {
		hardAt = c.expiry(value)
		if !hardAt.IsZero() && hardAt.Before(expires) {
			expires, hard = hardAt, true
		}
	}
	c.entries[key] = ttlEntry[V]{value: value, expires: expires, hard: hard, hardAt: hardAt}
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

// nodeUserSet 是一个池当前该放行的用户、它的版本（UniProxy ETag 同源）、算出它时
// 的下发纪元，以及名单里最早的订阅到期时刻（零值表示没有会到期的订阅）。users 被
// 多个请求共享，只读。version 只在缓存路径上算好，直查路径为空。
type nodeUserSet struct {
	users      []ProxyUser
	version    string
	epoch      int64
	nextExpiry time.Time
	// watch 是加载前的纪元监听戳（零值：加载时监听不健康）。戳覆盖请求时的戳就不必
	// 再比纪元，loose 模式下也不必按 TTL 重算（epoch_watch.go）。
	watch watchStamp
	// strict 是加载时的设备判定模式：strict 名单依赖在线设备记录，没有纪元，照旧按 TTL 重算。
	strict bool
	// body 是这一版名单的 UniProxy 响应正文（JSON 与 gzip 各编码一次，同池节点共享）。
	// 只在缓存路径上有，直查路径为 nil。
	body *userSetBody
}

// nextExpiryFloor 是 nextExpiry 离现在的最小距离。库与本进程的时钟有偏差：本进程
// 先到了到期时刻、库里 now() 还没到，重算出的名单仍含这个人、nextExpiry 不变；
// 不设下限的话，偏差窗口里每个请求都同步重算一次。
const nextExpiryFloor = time.Second

// earlierExpiry 取两者中较早的到期时刻；at 为空（不会到期）时原样返回 cur。
func earlierExpiry(cur time.Time, at *time.Time) time.Time {
	if at == nil || (!cur.IsZero() && !at.Before(cur)) {
		return cur
	}
	return *at
}

// clampNextExpiry 把已经过去或太近的到期时刻推到 now + nextExpiryFloor。
func clampNextExpiry(at, now time.Time) time.Time {
	if at.IsZero() {
		return at
	}
	if floor := now.Add(nextExpiryFloor); at.Before(floor) {
		return floor
	}
	return at
}

// nodeCaches 是 aegis-node 进程的用户集缓存、签名身份缓存与节点配置视图缓存。
type nodeCaches struct {
	users    *ttlCache[nodeUserSet]
	identity *ttlCache[Identity]
	// config 只在纪元监听健康时用（nodeConfigView，config_delivery_view.go）。
	config *ttlCache[*nodeConfigView]
	// watch 是纪元监听（StartEpochWatch 起；没起时为 nil，戳恒为零值）。
	watch *epochWatch
	// alive 是在线上报的刷新备忘（uniproxy_alive_memo.go）。
	alive *aliveMemo
}

func newNodeCaches(now func() time.Time) *nodeCaches {
	users := newTTLCache[nodeUserSet](nodeUsersCacheTTL, nodeUsersCacheMax, now)
	users.staleGrace = nodeUsersStaleGrace
	users.rank = func(set nodeUserSet) int64 { return set.epoch }
	users.expiry = func(set nodeUserSet) time.Time { return set.nextExpiry }
	identity := newTTLCache[Identity](nodeIdentityCacheTTL, nodeIdentityCacheMax, now)
	identity.expiry = func(id Identity) time.Time { return id.expiresAt }
	identity.rank = func(id Identity) int64 { return id.epoch }
	config := newTTLCache[*nodeConfigView](nodeConfigCacheTTL, nodeIdentityCacheMax, now)
	return &nodeCaches{users: users, identity: identity, config: config, alive: newAliveMemo()}
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
