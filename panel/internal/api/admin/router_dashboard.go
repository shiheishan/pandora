// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerDashboardRoutes
// [POS]: api/admin 路由表的「仪表盘概览、收入趋势与调账、流量排行、通知积压」段，由 NewRouter 按原注册顺序调用；处理器在 dashboard.go / revenue.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerDashboardRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 仪表盘 ---
	r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
		Get("/overview", h.overview)
	r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
		Get("/revenue/timeseries", h.revenueTimeseries)
	r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
		Get("/revenue/adjustments", h.revenueAdjustments)
	r.With(
		middleware.RequirePermission("metering.read", d.Log),
		middleware.RequirePermission("node.read", d.Log),
	).Get("/dashboard/traffic/nodes", h.dashboardNodeTraffic)
	r.With(
		middleware.RequirePermission("metering.read", d.Log),
		middleware.RequirePermission("iam.user.read", d.Log),
	).Get("/dashboard/traffic/users", h.dashboardUserTraffic)
	r.With(middleware.RequirePermission("ops.notification.read", d.Log)).
		Get("/dashboard/backlog/notifications", h.dashboardNotificationBacklog)
	r.With(
		middleware.RequirePermission("billing.adjustment.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "revenue_adjustment_create", d.Log),
	).Post("/revenue/adjustments", h.createRevenueAdjustment)
	r.With(
		middleware.RequirePermission("billing.adjustment.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "revenue_adjustment_reverse", d.Log),
	).Post("/revenue/adjustments/{id}/reverse", h.reverseRevenueAdjustment)
}
