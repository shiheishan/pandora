// Package handler 是 notify 模块的 HTTP 层：后台邮件 / Telegram 设置、后台 Telegram 扩展设置、邮件模板、公告、群发邮件、管理员自己的 Telegram 绑定；门户站内信、通知偏好、公告、Telegram 绑定；bot webhook。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Notify 是 handler 用到的 notify service 方法；*service.Service 实现它。
type Notify interface {
	MailSettings(ctx context.Context) (service.MailSettings, error)
	SaveMailSettings(ctx context.Context, in service.MailSettingsUpdate) error
	TestMailSettings(ctx context.Context, to string) error
	ListTemplates(ctx context.Context) ([]service.TemplateView, error)
	SaveTemplate(ctx context.Context, in service.TemplateEdit) (service.TemplateView, error)
	ResetTemplate(ctx context.Context, code, channel string) (service.TemplateView, error)
	PreviewDraft(ctx context.Context, in service.TemplateEdit) (service.DraftPreview, error)
	TestTemplate(ctx context.Context, in service.TemplateTest) (string, error)

	// 公告
	ListAnnouncements(ctx context.Context) (service.AdminAnnouncements, error)
	SaveAnnouncement(ctx context.Context, in service.AnnouncementInput) (service.AnnouncementSaved, error)
	WithdrawAnnouncement(ctx context.Context, id uuid.UUID, expectedVersion int) (int, error)
	MyAnnouncements(ctx context.Context, userID uuid.UUID) ([]service.PortalAnnouncement, error)

	// 群发邮件
	SendBulkMail(ctx context.Context, in service.BulkMailInput) (service.BulkMailResult, error)

	// 站内信与通知偏好
	Inbox(ctx context.Context, userID uuid.UUID, onlyUnread bool, limit int) ([]service.InboxEntry, int64, error)
	MarkInboxRead(ctx context.Context, userID, id uuid.UUID) error
	MarkAllInboxRead(ctx context.Context, userID uuid.UUID) error
	Preferences(ctx context.Context, userID uuid.UUID) ([]service.Preference, error)
	SetPreference(ctx context.Context, userID uuid.UUID, category, channel string, enabled bool) error

	// Telegram
	TelegramSettings(ctx context.Context) (service.TelegramSettingsView, error)
	SaveTelegramSettings(ctx context.Context, in service.TelegramSettingsInput) error
	TestTelegram(ctx context.Context, chatID int64) error
	TelegramExtras(ctx context.Context) (settingssvc.TelegramExtras, error)
	SaveTelegramExtras(ctx context.Context, in service.TelegramExtrasInput) error
	TelegramBinding(ctx context.Context, userID uuid.UUID) (service.TelegramBindingInfo, error)
	IssueBindCode(ctx context.Context, userID uuid.UUID) (service.BindCode, error)
	UnbindTelegram(ctx context.Context, userID uuid.UUID) error
	HandleTelegramUpdate(ctx context.Context, secret string, body []byte) error
}

var _ Notify = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 notify 模块的 HTTP 入口。
type Handler struct {
	svc Notify
	lim *ratelimit.Limiter
}

// New 构造 Handler。
func New(svc Notify) *Handler {
	return &Handler{svc: svc}
}

// UseLimiter 挂上限流器；不挂时（单测）webhook 不限流。
func (h *Handler) UseLimiter(lim *ratelimit.Limiter) { h.lim = lim }

// limit 挂限流；没有 Limiter 时放行。
func (h *Handler) limit(dims ...ratelimit.Dimension) gin.HandlerFunc {
	if h.lim == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return h.lim.Middleware(dims...)
}

// RegisterPublic 注册门户路由与 Telegram bot webhook（webhook 靠请求头里的 secret 认证，不挂登录）。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	// 按来源 IP 每分钟 300 次，挡伪造请求刷 secret 校验；Telegram 正常推送远低于此；非严格模式，Redis 不可用时放行。
	v1.POST("/webhooks/telegram", h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("tg_webhook", 300), Key: ratelimit.ByIP}), h.telegramWebhook)

	v1.GET("/me/announcements", k.RequireAuth, h.myAnnouncements)
	v1.GET("/me/notifications", k.RequireAuth, h.listNotifications)
	v1.POST("/me/notifications/read-all", k.RequireAuth, h.markAllNotificationsRead)
	v1.POST("/me/notifications/:id/read", k.RequireAuth, h.markNotificationRead)
	v1.GET("/me/notification-preferences", k.RequireAuth, h.getPreferences)
	v1.PUT("/me/notification-preferences", k.RequireAuth, h.setPreference)
	v1.GET("/me/telegram", k.RequireAuth, h.myTelegram)
	v1.POST("/me/telegram/bind-code", k.RequireAuth, h.telegramBindCode)
	v1.DELETE("/me/telegram", k.RequireAuth, h.telegramUnbind)
}

// RegisterAdmin 注册管理端路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/settings/mail", k.RequireAuth, h.getMailSettings)
	v1.POST("/settings/mail", k.RequireAuth, k.RecentReauth, h.setMailSettings)
	v1.POST("/settings/mail/test", k.RequireAuth, h.testMailSettings)
	v1.GET("/mail/templates", k.RequireAuth, h.listTemplates)
	v1.POST("/mail/templates/preview", k.RequireAuth, h.previewTemplate)
	v1.POST("/mail/templates", k.RequireAuth, h.saveTemplate)
	v1.POST("/mail/templates/reset", k.RequireAuth, h.resetTemplate)
	v1.POST("/mail/templates/test", k.RequireAuth, k.RecentReauth, h.testTemplate)

	v1.GET("/settings/telegram", k.RequireAuth, h.getTelegramSettings)
	v1.POST("/settings/telegram", k.RequireAuth, k.RecentReauth, h.setTelegramSettings)
	v1.POST("/settings/telegram/test", k.RequireAuth, h.testTelegram)
	v1.GET("/settings/telegram/extras", k.RequireAuth, h.getTelegramExtras)
	v1.POST("/settings/telegram/extras", k.RequireAuth, k.RecentReauth, h.setTelegramExtras)
	// 管理员自己的 Telegram 绑定（在管理员群组里回复工单提醒要用）：与门户同一组处理函数，从登录主体取 user id
	v1.GET("/me/telegram", k.RequireAuth, h.myTelegram)
	v1.POST("/me/telegram/bind-code", k.RequireAuth, k.RecentReauth, h.telegramBindCode)
	v1.DELETE("/me/telegram", k.RequireAuth, h.telegramUnbind)

	v1.GET("/announcements", k.RequireAuth, h.listAnnouncements)
	v1.POST("/announcements", k.RequireAuth, k.RecentReauth, k.Idempotency("announcement_save"), h.saveAnnouncement)
	v1.POST("/announcements/:id", k.RequireAuth, k.RecentReauth, k.Idempotency("announcement_save"), h.saveAnnouncement)
	v1.POST("/announcements/:id/withdraw", k.RequireAuth, k.RecentReauth, k.Idempotency("announcement_withdraw"), h.withdrawAnnouncement)

	v1.POST("/users/bulk/mail", k.RequireAuth, k.RecentReauth, k.Idempotency("user_bulk_mail"), h.sendBulkMail)
}
