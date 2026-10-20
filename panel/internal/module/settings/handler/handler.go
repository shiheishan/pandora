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
	SetSite(ctx context.Context, u service.SiteUpdate) error
	RegistrationPolicy(ctx context.Context) (service.Registration, error)
	MailConfigured(ctx context.Context) (bool, error)
	Switches(ctx context.Context) ([]service.Switch, error)
	SetSwitch(ctx context.Context, code string, enabled bool, reason string) error
	SwitchEnabled(ctx context.Context, code string) (bool, error)
	// SignupWithoutSecret 不解封人机验证 secret：本 handler 只回 has_captcha_secret
	SignupWithoutSecret(ctx context.Context) (service.Signup, error)
	SetSignup(ctx context.Context, u service.SignupUpdate) error
	SubscriptionExtrasSettings
	SupportRules(ctx context.Context) (service.SupportRules, error)
	SetSupportRules(ctx context.Context, v service.SupportRules) error
	PurchaseLimits(ctx context.Context) (service.PurchaseLimits, error)
	SetPurchaseLimits(ctx context.Context, v service.PurchaseLimits) error
	Home(ctx context.Context) (service.Home, error)
	SetHome(ctx context.Context, u service.HomeUpdate) error
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
	v1.GET("/signup-config", h.signupConfig)
}

// RegisterAdmin 注册管理端路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/settings/site", k.RequireAuth, h.getSite)
	v1.POST("/settings/site", k.RequireAuth, k.RecentReauth, h.setSite)
	v1.GET("/switches", k.RequireAuth, h.listSwitches)
	v1.POST("/switches/:code", k.RequireAuth, k.RecentReauth, h.setSwitch)
	v1.GET("/settings/signup", k.RequireAuth, h.getSignup)
	v1.POST("/settings/signup", k.RequireAuth, k.RecentReauth, h.setSignup)
	v1.GET("/settings/subscription", k.RequireAuth, h.getSubscriptionExtras)
	v1.POST("/settings/subscription", k.RequireAuth, k.RecentReauth, h.setSubscriptionExtras)
	v1.GET("/settings/support", k.RequireAuth, h.getSupportRules)
	v1.POST("/settings/support", k.RequireAuth, k.RecentReauth, h.setSupportRules)
	v1.GET("/settings/purchase", k.RequireAuth, h.getPurchaseLimits)
	v1.POST("/settings/purchase", k.RequireAuth, k.RecentReauth, h.setPurchaseLimits)
	v1.GET("/settings/home", k.RequireAuth, h.getHome)
	v1.POST("/settings/home", k.RequireAuth, k.RecentReauth, h.setHome)
}
