package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerAnnouncementRoutes(r chi.Router, d Deps, h *handlers) {
	// 公告。运营角色不依赖套餐目录权限；写操作要求近期重认证与幂等键。
	r.With(middleware.RequirePermission("ops.announcement.write", d.Log)).
		Get("/announcements", h.listAnnouncements)
	r.With(
		middleware.RequirePermission("ops.announcement.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "announcement_save", d.Log),
	).
		Post("/announcements", h.saveAnnouncement)
	r.With(
		middleware.RequirePermission("ops.announcement.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "announcement_save", d.Log),
	).
		Post("/announcements/{id}", h.saveAnnouncement)
	r.With(
		middleware.RequirePermission("ops.announcement.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "announcement_withdraw", d.Log),
	).
		Post("/announcements/{id}/withdraw", h.withdrawAnnouncement)
}

func registerContentPageRoutes(r chi.Router, d Deps, h *handlers) {
	// 版本化知识库/自定义页面。所有写操作都要求近期重认证与幂等键，
	// 正文只作为纯文本源存储和传输，不进入可信 HTML 边界。
	r.With(middleware.RequirePermission("ops.content.write", d.Log)).
		Get("/content-pages", h.listContentPages)
	r.With(middleware.RequirePermission("ops.content.write", d.Log)).
		Get("/content-pages/{id}", h.getContentPage)
	r.With(
		middleware.RequirePermission("ops.content.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "content_page_version_create", d.Log),
	).Post("/content-pages", h.publishContentVersion)
	r.With(
		middleware.RequirePermission("ops.content.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "content_page_archive", d.Log),
	).Post("/content-pages/{id}/archive", h.archiveContentPage)
}
