// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerPlanRoutes、registerTrafficPackRoutes、registerCatalogPlanUpdate、catalogIdempotencyFactory
// [POS]: api/admin 路由表的「套餐目录、向导、版本与价格，以及流量包目录；PUT /plans/{id} 经 registerCatalogPlanUpdate 注册，便于测试注入幂等中间件」段，由 NewRouter 按原注册顺序调用；处理器在 catalog.go 与 traffic_packs.go；套餐绑节点分组的两条路由在 router_nodes.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/db"
)

type catalogIdempotencyFactory func(*db.Pool, string, *slog.Logger) func(http.Handler) http.Handler

func registerCatalogPlanUpdate(r chi.Router, d Deps, handler http.HandlerFunc, idempotency catalogIdempotencyFactory) {
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		idempotency(d.Pool, "catalog_plan_update", d.Log),
	).Put("/plans/{id}", handler)
}

func registerPlanRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 套餐 ---
	r.With(middleware.RequirePermission("catalog.read", d.Log)).
		Get("/plans", h.listPlans)
	r.With(middleware.RequirePermission("catalog.read", d.Log)).
		Get("/plans/{id}", h.getPlan)
	r.With(
		middleware.RequirePermission("catalog.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_plan_create", d.Log),
	).Post("/plans", h.createPlan)
	// 向导一次就能建价格、绑线路、发布版本（D-C-2），门槛与单独的
	// 发布 / 改价接口相同：catalog.publish + 近期重认证。
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		// 独立的幂等域：和 /plans 共用一个 scope 的话，两个接口的
		// 幂等键会互相撞 —— 同一个 key 在这边建过整套餐，在那边
		// 就会被当成重放。
		middleware.Idempotency(d.Pool, "catalog_plan_create_complete", d.Log),
	).Post("/plans/complete", h.createPlanComplete)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_plan_update_complete", d.Log),
	).Put("/plans/{id}/complete", h.updatePlanComplete)
	registerCatalogPlanUpdate(r, d, h.updatePlan, middleware.Idempotency)
	r.With(
		middleware.RequirePermission("catalog.write", d.Log),
		middleware.Idempotency(d.Pool, "catalog_plan_version_create", d.Log),
	).Post("/plans/{id}/versions", h.createPlanVersion)
	r.With(middleware.RequirePermission("catalog.write", d.Log)).
		Put("/plans/{id}/versions/{versionID}", h.updatePlanVersion)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_plan_version_publish", d.Log),
	).Post("/plans/{id}/versions/{versionID}/publish", h.publishPlanVersion)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_price_create", d.Log),
	).Post("/plans/{id}/prices", h.createPlanPrice)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_price_archive", d.Log),
	).Post("/plans/{id}/prices/{priceID}/archive", h.archivePlanPrice)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_plan_archive", d.Log),
	).Post("/plans/{id}/archive", h.archivePlan)
}

func registerTrafficPackRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 流量包 ---
	// 流量包是在售商品：新建、改价、上下架与套餐改价同门槛（D-C-2），
	// catalog.publish + 近期重认证 + 幂等键，四个写接口各用一个幂等域。
	r.With(middleware.RequirePermission("catalog.read", d.Log)).
		Get("/traffic-packs", h.listTrafficPacks)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_traffic_pack_create", d.Log),
	).Post("/traffic-packs", h.createTrafficPack)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_traffic_pack_update", d.Log),
	).Put("/traffic-packs/{id}", h.updateTrafficPack)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_traffic_pack_status", d.Log),
	).Post("/traffic-packs/{id}/status", h.setTrafficPackStatus)
}
