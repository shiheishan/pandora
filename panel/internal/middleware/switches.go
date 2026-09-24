// [INPUT]: 依赖 platform/db 的租户事务读 feature_switches，依赖 platform/httpx 的租户、错误模型，依赖 chi 的 RouteContext 取挂载点内的相对路径
// [OUTPUT]: 对外提供 FeatureSwitch、AdminWritesGate
// [POS]: middleware 的降级开关门（NFR-008，契约后台-09 POST v1/switches/{code}）：开关关闭时在网关层回 503，业务代码不用各自读开关
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// switchEnabled 读一个降级开关。enabled=true 表示功能可用。
//
// 缺行视为开启（fail open）：这几个开关是运维手里的「急停」，迁移只给当时已有的
// 租户插了行，之后新建的租户没有行；把缺行当关闭，新租户会一上来就不能下单。
// auth.registration 是反例（缺行即关闭），那是注册策略自己的规则，不走这里。
func switchEnabled(ctx context.Context, pool *db.Pool, tenantID, code string) (bool, error) {
	enabled := true
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT enabled FROM feature_switches WHERE tenant_id = $1 AND code = $2`,
			tenantID, code).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return enabled, err
}

// FeatureSwitch 在开关 code 关闭时拒绝请求，回 503 service_unavailable。
// 挂在 Idempotency 之前：被拒的请求不消耗幂等键，开关恢复后用原键重试即可。
func FeatureSwitch(pool *db.Pool, code, message string, log *slog.Logger) func(http.Handler) http.Handler {
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
func AdminWritesGate(pool *db.Pool, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if adminWriteExempt(r.Method, routePath(r)) {
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
	return (method == http.MethodPost && strings.HasPrefix(path, "/switches/")) ||
		strings.HasPrefix(path, "/auth/") || path == "/me/password"
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
