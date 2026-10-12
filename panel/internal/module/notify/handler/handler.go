// Package handler 是 notify 模块的 HTTP 层：后台邮件设置与邮件模板。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
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
}

var _ Notify = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 notify 模块的 HTTP 入口。
type Handler struct{ svc Notify }

// New 构造 Handler。
func New(svc Notify) *Handler {
	return &Handler{svc: svc}
}

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {}

// RegisterAdmin 注册管理端路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/settings/mail", k.RequireAuth, h.getMailSettings)
	v1.POST("/settings/mail", k.RequireAuth, h.setMailSettings)
	v1.POST("/settings/mail/test", k.RequireAuth, h.testMailSettings)
	v1.GET("/mail/templates", k.RequireAuth, h.listTemplates)
	v1.POST("/mail/templates/preview", k.RequireAuth, h.previewTemplate)
	v1.POST("/mail/templates", k.RequireAuth, h.saveTemplate)
	v1.POST("/mail/templates/reset", k.RequireAuth, h.resetTemplate)
	v1.POST("/mail/templates/test", k.RequireAuth, k.RecentReauth, h.testTemplate)
}
