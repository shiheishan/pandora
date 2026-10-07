package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// switchCacheTTL 是降级开关在进程内缓存的时长。
//
// 开关是急停：关掉之后几秒内全部生效就够，不需要每个请求都去库里确认一次。
// 3 秒取在 2–5 秒的中间，稳态下每个进程每个开关每 3 秒最多读一次库。失效靠两条路：
//   - 切开关所在的 admin 网关：请求经过 AdminWritesGate 时当场清空；
//   - 其它网关：订阅后台切开关时广播的 switches.changed（WatchFeatureSwitchChanges）。
//
// 广播走 Valkey、不保证送达，丢了也只是退回 TTL：最长滞后 3 秒。
const switchCacheTTL = 3 * time.Second

// switchCacheMax 是缓存条目上限。键是（租户, 开关码），开关码由路由声明、
// 产品又只有一个租户，正常只有个位数条；超过上限说明键失控，直接清空重来。
const switchCacheMax = 256

type switchKey struct{ tenant, code string }

type switchEntry struct {
	enabled bool
	expires time.Time
}

type switchCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[switchKey]switchEntry
}

var switches = newSwitchCache(switchCacheTTL, time.Now)

func newSwitchCache(ttl time.Duration, now func() time.Time) *switchCache {
	return &switchCache{ttl: ttl, now: now, entries: map[switchKey]switchEntry{}}
}

func (c *switchCache) get(tenant, code string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[switchKey{tenant, code}]
	if !ok || !c.now().Before(e.expires) {
		return false, false
	}
	return e.enabled, true
}

func (c *switchCache) put(tenant, code string, enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= switchCacheMax {
		clear(c.entries)
	}
	c.entries[switchKey{tenant, code}] = switchEntry{enabled: enabled, expires: c.now().Add(c.ttl)}
}

func (c *switchCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}

// InvalidateFeatureSwitches 清空本进程的降级开关缓存，下一次读取回库。
// 切开关的请求经过 AdminWritesGate 时自动调用；别处改了 feature_switches
// 想让本进程立即看到新值时也可以调用。
func InvalidateFeatureSwitches() { switches.reset() }

// switchesChangedTopic 是后台切开关成功后向管理端频道广播的主题
// （api/admin 的 setSwitch 发出，守卫 switch_cache_test.go 对齐两边）。
const switchesChangedTopic = "switches.changed"

// switchEventSource 是 *realtime.Hub 的订阅面。
type switchEventSource interface {
	Subscribe(channels []string) (<-chan realtime.Event, func())
}

// WatchFeatureSwitchChanges 订阅租户的管理端频道，收到 switches.changed 就清空
// 本进程的开关缓存。给不经过 AdminWritesGate 的网关（门户的下单、礼品卡开关）用。
// ctx 取消后退出；返回的函数等它退出（与其它后台循环一样在关资源之前 join）。
func WatchFeatureSwitchChanges(ctx context.Context, hub switchEventSource, tenantID string) (wait func()) {
	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelAdmin(tenantID)})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ev.Topic == switchesChangedTopic {
					InvalidateFeatureSwitches()
				}
			}
		}
	}()
	return func() { <-done }
}
