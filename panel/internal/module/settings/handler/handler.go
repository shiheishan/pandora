// Package handler 是 settings 模块的 HTTP 层：站点设置、降级开关与门户 site-config，以及开关中间件。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
)

// Settings 是 handler 用到的 settings service 方法；*service.Service 实现它。
type Settings interface {
	Site(ctx context.Context) (service.Site, error)
	SetSite(ctx context.Context, v service.Site) error
	RegistrationPolicy(ctx context.Context) (service.Registration, error)
	MailConfigured(ctx context.Context) (bool, error)
	Switches(ctx context.Context) ([]service.Switch, error)
	SetSwitch(ctx context.Context, code string, enabled bool, reason string) error
	SwitchEnabled(ctx context.Context, code string) (bool, error)
}

var _ Settings = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 settings 模块的 HTTP 入口。
type Handler struct{ svc Settings }

// New 构造 Handler。
func New(svc Settings) *Handler {
	return &Handler{svc: svc}
}

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/site-config", h.siteConfig)
}

// RegisterAdmin 注册管理端路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/settings/site", k.RequireAuth, k.Permission("security.audit.read"), h.getSite)
	v1.POST("/settings/site", k.RequireAuth, k.Permission("platform.settings.write"), k.RecentReauth, h.setSite)
	v1.GET("/switches", k.RequireAuth, k.Permission("security.audit.read"), h.listSwitches)
	v1.POST("/switches/:code", k.RequireAuth, k.Permission("platform.settings.write"), k.RecentReauth, h.setSwitch)
}
