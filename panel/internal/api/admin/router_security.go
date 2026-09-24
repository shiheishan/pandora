// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerAuditRoutes、registerRiskRoutes、registerSwitchRoutes
// [POS]: api/admin 路由表的「审计与导出、系统状态、风控画像与 IP 聚类处置、降级开关」段，由 NewRouter 按原注册顺序调用；处理器在 audit_log.go / system_status.go / profile.go / access_log.go / risk.go / handlers.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerAuditRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 审计 ---
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/audit", h.listAudit)
	// 导出带走含明文来源 IP 的全量记录：要导出权限，并且刚输过密码
	r.With(
		middleware.RequirePermission("security.audit.read", d.Log),
		middleware.RequirePermission("ops.export", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Get("/audit/export", h.exportAudit)
	// 系统状态：备份跑没跑、数据库多大。备份原先是彻底的盲区 ——
	// 每天都在正常跑，但面板上看不到；定时器哪天坏了同样没人发现。
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/system/status", h.systemStatus)
}

func registerRiskRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 风控画像 ---
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/users/{id}/profile", h.userProfile)
	// 全站访问明细。挂同一个权限：它展示的是解密后的明文 IP，
	// 和用户画像是同一级别的敏感数据。
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/access-log", h.accessLogList)
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/stats/timeseries", h.statsTimeseries)
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/ip-clusters", h.ipClusters)
	// 风控处置。标记正常可逆且只影响提示，不要重认证；批量停用会把
	// 一批人登出、订阅停止下发，要风控复核与用户写两个权限并重认证，
	// 幂等防止网络重试把「部分跳过」的结果算两遍
	r.With(middleware.RequirePermission("security.risk.review", d.Log)).
		Post("/ip-clusters/{key}/review", h.reviewIPCluster)
	r.With(
		middleware.RequirePermission("security.risk.review", d.Log),
		middleware.RequirePermission("iam.user.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "ip_cluster_disable", d.Log),
	).Post("/ip-clusters/{key}/disable-accounts", h.disableIPClusterAccounts)
}

func registerSwitchRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 降级开关 ---
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/switches", h.listSwitches)
	// 每次切换都要重认证：关掉的是整个站点的某项能力（契约后台-09）
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/switches/{code}", h.setSwitch)
}
