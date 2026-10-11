package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/cache"
	"github.com/aegispanel/aegis/internal/platform/featureswitch"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// switchQuerier 是开关读取对数据库的全部要求（*db.Pool 的 QueryRowScoped，一次往返）。
// 语句在 platform/featureswitch（中间件不写 SQL）。
type switchQuerier = featureswitch.Querier

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
// 产品又只有一个租户，正常只有个位数条；满了先清过期的，仍满挤掉最早过期的一条。
const switchCacheMax = 256

// switches 是本进程的降级开关缓存（platform/cache：有上限、单飞、TTL，没有纪元，靠 Clear 当场失效）。
var switches = newSwitchCache(time.Now)

type switchKey struct{ tenant, code string }

func newSwitchCache(now func() time.Time) *cache.Cache[switchKey, bool] {
	return cache.New[switchKey](cache.Options[bool]{TTL: switchCacheTTL, Max: switchCacheMax, Now: now})
}

func switchAlways(bool) bool { return true }

// switchEnabled 读一个降级开关。enabled=true 表示功能可用；缺行视为开启（见 featureswitch.Enabled）。
//
// 读到的值在进程内缓存 switchCacheTTL：管理端每个写请求、门户每次下单都要过一道开关，
// 以前每次一个完整事务（四次往返 + 归还清理）。同一个开关的并发未命中合成一次读库；
// 读库出错不缓存，照旧回 500。
func switchEnabled(ctx context.Context, pool switchQuerier, tenantID, code string) (bool, error) {
	key := switchKey{tenantID, code}
	// 先只探缓存：命中（绝大多数请求）不构造加载闭包，零分配
	if enabled, ok := switches.Lookup(key, switchAlways, nil); ok {
		return enabled, nil
	}
	return switches.Get(ctx, key, "", switchAlways, nil,
		func(ctx context.Context) (bool, error) { return featureswitch.Enabled(ctx, pool, tenantID, code) })
}

// InvalidateFeatureSwitches 清空本进程的降级开关缓存，下一次读取回库；此刻正在读库的那一趟
// 结果也作废，不会在失效之后写回旧值。切开关的请求经过 AdminWritesGate 时自动调用；
// 别处改了 feature_switches 想让本进程立即看到新值时也可以调用。
func InvalidateFeatureSwitches() { switches.Clear() }

// switchesChangedTopic 是后台切开关成功后向管理端频道广播的主题
// （api/admin 的 setSwitch 发出，守卫 switches_cache_test.go 对齐两边）。
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

// FeatureSwitch 在开关 code 关闭时拒绝请求，回 503 service_unavailable。
// 挂在 Idempotency 之前：被拒的请求不消耗幂等键，开关恢复后用原键重试即可。
func FeatureSwitch(pool switchQuerier, code, message string, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enabled, err := switchEnabled(r.Context(), pool, httpx.TenantIDFrom(r.Context()), code)
			if err != nil {
				httpx.Fail(w, r, log, httpx.Internal(err))
				return
			}
			if !enabled {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnavailable, message))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AdminWritesGate 是 admin.writes 开关：关闭时管理端进入只读模式，除了切开关
// 本身、登录与重认证、改自己密码以外的所有非 GET 请求一律 503。
//
// 挂在 /v1 子路由上，按挂载点内的相对路径判断豁免——网关前面是否还有路径前缀
// （AEGIS_ADMIN_PATH）不影响判断。
//
// 与 FeatureSwitch 共用同一份开关缓存。切开关（POST /switches/{code}）也从这里过，
// 处理完就清空本进程的缓存：管理员关掉 admin.writes 后，本网关的下一个写请求立刻
// 按新值判断。其它网关进程（门户的下单、礼品卡开关）最长滞后 switchCacheTTL。
func AdminWritesGate(pool switchQuerier, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := routePath(r)
			if isSwitchWrite(r.Method, path) {
				defer InvalidateFeatureSwitches()
			}
			if adminWriteExempt(r.Method, path) {
				next.ServeHTTP(w, r)
				return
			}
			enabled, err := switchEnabled(r.Context(), pool, httpx.TenantIDFrom(r.Context()), "admin.writes")
			if err != nil {
				httpx.Fail(w, r, log, httpx.Internal(err))
				return
			}
			if !enabled {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnavailable, "管理端只读模式"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// adminWriteExempt 列出只读模式下仍放行的请求。path 是 /v1 之内的相对路径。
// 切开关必须放行，否则关了就再也开不回来；登录、重认证与改密码是进门的钥匙。
func adminWriteExempt(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return isSwitchWrite(method, path) ||
		strings.HasPrefix(path, "/auth/") || path == "/me/password"
}

// isSwitchWrite 是切开关的请求（POST v1/switches/{code}）。
func isSwitchWrite(method, path string) bool {
	return method == http.MethodPost && strings.HasPrefix(path, "/switches/")
}

func routePath(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
		return rctx.RoutePath
	}
	path := r.URL.Path
	if i := strings.Index(path, "/v1/"); i >= 0 {
		path = path[i+len("/v1"):]
	}
	return path
}
