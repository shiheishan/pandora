package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/featureswitch"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// switchQuerier 是开关读取对数据库的全部要求（*db.Pool 的 QueryRowScoped，一次往返）。
// 语句在 platform/featureswitch（中间件不写 SQL）。
type switchQuerier = featureswitch.Querier

// switchEnabled 读一个降级开关。enabled=true 表示功能可用；缺行视为开启（见 featureswitch.Enabled）。
//
// 读到的值在进程内缓存 switchCacheTTL（见 switch_cache.go）：管理端每个写请求、
// 门户每次下单都要过一道开关，以前每次一个完整事务（四次往返 + 归还清理）。
// 读库出错不缓存，照旧回 500。
func switchEnabled(ctx context.Context, pool switchQuerier, tenantID, code string) (bool, error) {
	if enabled, ok := switches.get(tenantID, code); ok {
		return enabled, nil
	}
	enabled, err := featureswitch.Enabled(ctx, pool, tenantID, code)
	if err != nil {
		return false, err
	}
	switches.put(tenantID, code, enabled)
	return enabled, nil
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
