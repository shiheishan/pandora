// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerTelegramRoutes、registerMailSettingsRoutes、registerMailTemplateRoutes
// [POS]: api/admin 路由表的「Telegram 配置、邮件设置、通知模板及三个测试发送」段，由 NewRouter 按原注册顺序调用；处理器在 telegram.go / mail.go / mail_template.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerTelegramRoutes(r chi.Router, d Deps, h *handlers) {
	// --- Telegram ---
	// Bot Token 走信封加密，读接口只回「配没配」不回明文。
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/settings/telegram", h.getTelegramSettings)
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
	).Post("/settings/telegram", h.setTelegramSettings)
	// 测试发送（Telegram / SMTP / 模板）统一用通知写权限：它们是
	// 「往外真发一条」，与支付渠道无关，原先挂的 billing.provider.write 是错位。
	r.With(middleware.RequirePermission("ops.notification.write", d.Log)).
		Post("/settings/telegram/test", h.testTelegram)
}

func registerMailSettingsRoutes(r chi.Router, d Deps, h *handlers) {
	// 邮件设置
	r.With(middleware.RequirePermission("security.audit.read", d.Log)).
		Get("/settings/mail", h.getMailSettings)
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
	).Post("/settings/mail", h.setMailSettings)
	r.With(middleware.RequirePermission("ops.notification.write", d.Log)).
		Post("/settings/mail/test", h.testMailSettings)
}

func registerMailTemplateRoutes(r chi.Router, d Deps, h *handlers) {
	// 通知模板：内容可改，但 code 不能新建 ——
	// code 是代码里的常量，后台建一个没人调用的模板只会误导人。
	// 测试发送要求近期重认证：它会往任意地址真发信。
	r.With(middleware.RequirePermission("ops.notification.read", d.Log)).
		Get("/mail/templates", h.listMailTemplates)
	// 草稿预览是纯计算、不写库，与看模板同权
	r.With(middleware.RequirePermission("ops.notification.read", d.Log)).
		Post("/mail/templates/preview", h.previewMailTemplate)
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
	).Post("/mail/templates", h.saveMailTemplate)
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
	).Post("/mail/templates/reset", h.resetMailTemplate)
	r.With(
		middleware.RequirePermission("ops.notification.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/mail/templates/test", h.testMailTemplate)
}
