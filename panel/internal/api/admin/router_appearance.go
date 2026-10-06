package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerThemeRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 外观：主题与插槽 ---
	// 自定义 CSS 和插槽 HTML 会渲染进用户端，写权限按高危对待。
	r.With(middleware.RequirePermission("platform.appearance.read", d.Log)).
		Get("/themes", h.listThemes)
	r.With(
		middleware.RequirePermission("platform.appearance.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "appearance_theme_save", d.Log),
	).Post("/themes", h.saveTheme)
	r.With(
		middleware.RequirePermission("platform.appearance.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/themes/{code}/activate", h.activateTheme)
	r.With(
		middleware.RequirePermission("platform.appearance.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Delete("/themes/{code}", h.deleteTheme)
	r.With(middleware.RequirePermission("platform.appearance.read", d.Log)).
		Get("/slots", h.listSlots)
	r.With(
		middleware.RequirePermission("platform.appearance.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "appearance_slot_save", d.Log),
	).Post("/slots/{key}", h.saveSlot)

	// 站点时区（R49）：读与邮件设置同权；改它会改变之后所有按日统计的切日，
	// 写要重认证。整份覆盖、没有副作用可重放，不带幂等（同 settings/mail）
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/settings/site", h.getSiteSettings)
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/settings/site", h.setSiteSettings)
}

func registerPluginHookRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 插件钩子 ---
	// 钩子地址决定面板会去连什么，改它等同于改一条出站规则，
	// 所以写操作要重认证。
	r.With(middleware.RequirePermission("platform.plugin.read", d.Log)).
		Get("/plugin-hooks", h.listHooks)
	r.With(
		middleware.RequirePermission("platform.plugin.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "plugin_hook_save", d.Log),
	).Post("/plugin-hooks", h.saveHook)
	r.With(
		middleware.RequirePermission("platform.plugin.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Delete("/plugin-hooks/{code}", h.deleteHook)
	r.With(middleware.RequirePermission("platform.plugin.read", d.Log)).
		Get("/plugin-hooks/{code}/deliveries", h.hookDeliveries)
	r.With(
		middleware.RequirePermission("platform.plugin.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/plugin-hooks/{code}/test", h.testHook)
}
