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
	// 换 bot、改告警接收群组：要近期重认证（审计台账 2.3 第 2 条），写入与审计同一事务
	r.With(
		middleware.RequirePermission("platform.settings.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
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
