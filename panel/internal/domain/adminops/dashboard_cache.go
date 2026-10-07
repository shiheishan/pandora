package adminops

import (
	"sync"
	"time"
)

// dashboardCacheTTL 是看板读模型在进程内缓存的时长。
//
// 看板（概览、流量排行、数据库体积）不要求秒级实时，而每次打开、每个标签页定时刷新都要
// 重算一遍：概览里有整本账的核对（app.verify_ledger_all），流量排行要扫区间内的小时汇总。
// 30 秒内同一租户、同一组参数的请求共用一次结果。只缓存「现在」的视图：带 snapshot_at 的
// 历史查询照旧直读。写操作不主动失效，最多滞后 30 秒（与前端的刷新节拍同一量级）。
const dashboardCacheTTL = 30 * time.Second

// dashboardCacheMax 是缓存条目上限：键是（租户, 读模型, 参数），产品单租户、参数取值有限，
// 正常只有十来条；超过说明键失控，直接清空重来。
const dashboardCacheMax = 64

type dashboardCacheEntry struct {
	value   any
	expires time.Time
}

// dashboardCache 是按键的短 TTL 缓存。零值不可用；nil 指针表示不缓存（直接构造 Service 的测试走这条）。
// 存进去的值调用方只读，不得修改。
type dashboardCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]dashboardCacheEntry
}

func newDashboardCache(ttl time.Duration, now func() time.Time) *dashboardCache {
	return &dashboardCache{ttl: ttl, now: now, entries: map[string]dashboardCacheEntry{}}
}

func (c *dashboardCache) get(key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (c *dashboardCache) put(key string, v any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= dashboardCacheMax {
		clear(c.entries)
	}
	c.entries[key] = dashboardCacheEntry{value: v, expires: c.now().Add(c.ttl)}
}

// cachedRead 先查缓存，未命中再读并写回；读出错不缓存。
func cachedRead[T any](c *dashboardCache, key string, read func() (T, error)) (T, error) {
	if v, ok := c.get(key); ok {
		if t, ok := v.(T); ok {
			return t, nil
		}
	}
	v, err := read()
	if err != nil {
		return v, err
	}
	c.put(key, v)
	return v, nil
}
