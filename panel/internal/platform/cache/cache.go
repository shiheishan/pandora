// Package cache 是面板唯一的进程内缓存：纪元判有效、单飞、有上限、TTL 兜底。
//
// 由 nodefabric 的节点缓存（w10quiet 定稿）抽出，形状不变：
//
//   - 条目是否仍可用由调用方的 valid 判断（通常是比纪元：条目算出时的纪元不落后于请求时读到的）；
//     不可用就重算，同一把钥匙、同一个单飞标签（flight）的并发请求只算一次。
//   - pinned 认可的条目不看 TTL：纪元监听（Watch）证明加载之后没有相关提交，条目原样可用；
//     只看条目自己的到期时刻（Options.Expiry）。
//   - TTL 只兜没有纪元的输入和「纪元已推进、数据还没提交可见」那道提交缝；StaleGrace 让过了 TTL
//     的条目先回旧值、后台单飞重算（只对 TTL 到期生效，按 Expiry 硬过期的不在此列）。
//   - 只缓存成功结果；加载出错原样返回给这一趟的全部等待者，不存。
//   - 条目数有上限：满了先清过期的，仍满挤掉最早过期的一条。
//
// 没有纪元可比的缓存（降级开关这类急停）用 valid 恒真加 TTL，配 Clear 当场失效：Clear 同时作废
// 正在进行的加载，免得一份在失效之前读出来的旧值在失效之后被写回去。
//
// 纪元的来源有两种，都由迁移里的触发器推进（序列，不加锁、不进事务）：
//   - 在本来就要跑的查询里顺手读出（EpochSQL），零额外往返；
//   - 纪元监听（Watch）：LISTEN 一条通道，触发器推进纪元时同时 pg_notify，通知在提交之后才送达，
//     所以「收到通知」一定晚于数据可见。监听健康时请求不读纪元，不健康时戳为零值、退回查库。
package cache

import (
	"context"
	"sync"
	"time"
)

// DefaultLoadTimeout 是单飞加载的缺省上限。加载不跟随发起者的请求取消：
// 同一把钥匙上排着队的其他请求不该因为第一个人断开而一起失败。
const DefaultLoadTimeout = 15 * time.Second

// Options 配置一个 Cache。TTL 与 Max 必填。
type Options[V any] struct {
	// TTL 是条目的寿命上限（pinned 认可的条目不看它）。
	TTL time.Duration
	// Max 是条目数上限。
	Max int
	// Now 是时钟，测试里换成可拨的；nil 用 time.Now。
	Now func() time.Time
	// Expiry 给条目一个自己的到期时刻（如身份的 expires_at、名单里最早的订阅到期），与 TTL 取早者；
	// 零值表示没有。过了它一律同步重算：不走 pinned，也不走 StaleGrace。可为 nil。
	Expiry func(V) time.Time
	// StaleGrace > 0 时，TTL 到期但 valid 仍认可的条目在这个窗口里先回旧值、后台重算；
	// 按 Expiry 硬过期的条目不在此列。
	StaleGrace time.Duration
	// Rank 是条目的新旧（纪元）；晚完成的旧加载不覆盖已存的新条目。可为 nil。
	Rank func(V) int64
	// LoadTimeout 是单飞加载的上限；0 用 DefaultLoadTimeout。
	LoadTimeout time.Duration
}

type entry[V any] struct {
	value   V
	expires time.Time
	// hard 表示 expires 来自条目自己的到期时刻（Expiry），而不是 TTL：过了就不能
	// 再当旧值先回（StaleGrace 只对 TTL 到期生效）。
	hard bool
	// hardAt 是条目自己的到期时刻（Expiry），零值表示没有。pinned 的条目不看 TTL，
	// 但这个时刻照样作数。
	hardAt time.Time
}

type flight[V any] struct {
	done  chan struct{}
	value V
	err   error
	// gen 是发起时的失效代数；Clear 之后完成的旧加载不写回。
	gen uint64
}

// Cache 是带上限、TTL、单飞合并的进程内缓存，见包注释。零值不可用，用 New 构造。
// 存进去的值由所有命中者共享，调用方只读、不得修改。
type Cache[V any] struct {
	ttl         time.Duration
	max         int
	now         func() time.Time
	expiry      func(V) time.Time
	staleGrace  time.Duration
	rank        func(V) int64
	loadTimeout time.Duration

	mu      sync.Mutex
	entries map[string]entry[V]
	flights map[string]*flight[V]
	gen     uint64
}

// New 按 opts 构造一个缓存。
func New[V any](opts Options[V]) *Cache[V] {
	if opts.TTL <= 0 || opts.Max <= 0 {
		panic("cache: TTL and Max must be positive")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	timeout := opts.LoadTimeout
	if timeout <= 0 {
		timeout = DefaultLoadTimeout
	}
	return &Cache[V]{ttl: opts.TTL, max: opts.Max, now: now, expiry: opts.Expiry,
		staleGrace: opts.StaleGrace, rank: opts.Rank, loadTimeout: timeout,
		entries: make(map[string]entry[V]), flights: make(map[string]*flight[V])}
}

// Get 命中且 valid 认可就返回；否则同一（key, flightTag）只放一个加载，其余等它的结果。
// flightTag 区分「要多新」：要求更新的请求不去搭为较旧要求发起的那趟车。
//
// pinned 非空且认可条目时不看 TTL（纪元监听证明加载之后没有相关提交），
// 只看条目自己的到期时刻（Options.Expiry）。
func (c *Cache[V]) Get(ctx context.Context, key, flightTag string, valid, pinned func(V) bool,
	load func(context.Context) (V, error)) (V, error) {
	flightKey := key + "\x00" + flightTag
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && valid(e.value) {
		now := c.now()
		// 条目自己的到期时刻过了就一律同步重算：不走 pinned，也不走 StaleGrace
		// （入库时 TTL 早于它、hard=false 的条目同样如此）
		pastHard := !e.hardAt.IsZero() && !now.Before(e.hardAt)
		if now.Before(e.expires) || (pinned != nil && !pastHard && pinned(e.value)) {
			c.mu.Unlock()
			return e.value, nil
		}
		if c.staleGrace > 0 && !e.hard && !pastHard && now.Before(e.expires.Add(c.staleGrace)) {
			// 先回旧值；没有同标签的加载在跑就起一个后台加载，跑完替换条目。
			if _, busy := c.flights[flightKey]; !busy {
				f := &flight[V]{done: make(chan struct{}), gen: c.gen}
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
	f := &flight[V]{done: make(chan struct{}), gen: c.gen}
	c.flights[flightKey] = f
	c.mu.Unlock()
	c.run(ctx, key, flightKey, f, load)
	return f.value, f.err
}

// run 执行一趟已登记的加载，结束后放行排队的人并按需存下结果。
func (c *Cache[V]) run(ctx context.Context, key, flightKey string, f *flight[V],
	load func(context.Context) (V, error)) {
	// 加载 panic 也要放行排队的人，不能让他们挂到各自的超时。
	defer func() {
		c.mu.Lock()
		if c.flights[flightKey] == f {
			delete(c.flights, flightKey)
		}
		if f.err == nil && f.gen == c.gen {
			c.storeLocked(key, f.value)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.err = errLoadPanicked
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
	defer cancel()
	f.value, f.err = load(loadCtx)
}

func (c *Cache[V]) storeLocked(key string, value V) {
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
	c.entries[key] = entry[V]{value: value, expires: expires, hard: hard, hardAt: hardAt}
}

// Put 直接存一条（同加载完成时的写回：受上限、Rank 与 Expiry 约束）。
func (c *Cache[V]) Put(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.storeLocked(key, value)
}

// Peek 只读缓存，不触发加载；过了 TTL（或 Expiry）的条目不算。
func (c *Cache[V]) Peek(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Drop 丢掉一条，返回它原先是否在缓存里。
func (c *Cache[V]) Drop(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	delete(c.entries, key)
	return ok
}

// Clear 清空全部条目，并作废此刻正在进行的加载：它们照常把结果交给已经在等的人，
// 但不写回；之后来的请求不搭这些车，各自重新加载。
func (c *Cache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.entries)
	clear(c.flights)
}

// Len 是当前条目数（含已过期、还没被挤掉的）。
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

type cacheError string

func (e cacheError) Error() string { return string(e) }

const errLoadPanicked = cacheError("cache load did not complete")
