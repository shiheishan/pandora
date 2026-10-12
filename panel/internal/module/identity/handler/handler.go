// Package handler 是 identity 模块的 HTTP 层：注册、找回密码、个人资料与改密、邀请，以及后台用户与用户组管理。
package handler

import (
	"context"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 identity service 方法；*service.Service 实现它。
type Service interface {
	StartRegistration(ctx context.Context, email, inviteCode string) (service.RegistrationStart, error)
	CompleteRegistration(ctx context.Context, token, code, password string) (service.User, error)
	StartPasswordReset(ctx context.Context, email string) (service.PasswordResetStart, error)
	CompletePasswordReset(ctx context.Context, email, code, newPassword string) error
	Profile(ctx context.Context, id uuid.UUID) (service.Profile, error)
	ChangePassword(ctx context.Context, oldPassword, newPassword string) error
	MyInvite(ctx context.Context, userID uuid.UUID) (service.Invite, []service.Invitee, error)
	ListUsers(ctx context.Context, f service.UserFilter, limit, offset int) ([]service.UserRow, int64, error)
	UserDetail(ctx context.Context, id uuid.UUID) (service.UserDetail, error)
	SetStatus(ctx context.Context, userID uuid.UUID, status, reason string) error
	AdminResetPassword(ctx context.Context, userID uuid.UUID, newPassword, reason string) error
	AssignGroup(ctx context.Context, userID uuid.UUID, groupID *uuid.UUID) error
	ListGroups(ctx context.Context) ([]service.Group, error)
	CreateGroup(ctx context.Context, code, name, description string) (uuid.UUID, error)
	UpdateGroup(ctx context.Context, id uuid.UUID, name, description string) error
	DeleteGroup(ctx context.Context, id uuid.UUID) error
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 后台权限码（沿用旧代码，由 auth 模块登记）。
const (
	PermUserRead  = "iam.user.read"
	PermUserWrite = "iam.user.write"
)

// Handler 是 identity 模块的 HTTP 入口。
type Handler struct {
	svc        Service
	limiter    *ratelimit.Limiter
	authPerMin int
}

// New 构造 Handler。limiter 给注册、找回密码挂严格限流（Redis 故障回 503）；authPerMin 取 config.RateLimit.AuthPerMin。
func New(svc Service, limiter *ratelimit.Limiter, authPerMin int) *Handler {
	if authPerMin < 1 {
		authPerMin = 10
	}
	return &Handler{svc: svc, limiter: limiter, authPerMin: authPerMin}
}

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/auth/register/start", h.strict("reg_start", true), h.registerStart)
	v1.POST("/auth/register/complete", h.strict("reg_complete", true), h.registerComplete)
	v1.POST("/auth/password-reset/start", h.strict("pwreset_start", true), h.passwordResetStart)
	v1.POST("/auth/password-reset/complete", h.strict("pwreset_complete", false), h.passwordResetComplete)
	v1.GET("/me", k.RequireAuth, h.me)
	v1.POST("/me/password", k.RequireAuth, h.changePassword)
	v1.GET("/me/invite", k.RequireAuth, h.myInvite)
}

// RegisterAdmin 注册后台路由。Permission 自带登录检查，有权限码的路由不再挂 RequireAuth。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/users", k.Permission(PermUserRead), h.listUsers)
	v1.GET("/users/:id", k.Permission(PermUserRead), h.getUser)
	v1.POST("/users/:id/status", k.Permission(PermUserWrite), k.RecentReauth, h.setUserStatus)
	v1.POST("/users/:id/reset-password", k.Permission(PermUserWrite), k.RecentReauth, h.resetUserPassword)
	v1.POST("/users/:id/group", k.Permission(PermUserWrite), h.assignUserGroup)
	v1.GET("/user-groups", k.Permission(PermUserRead), h.listUserGroups)
	v1.POST("/user-groups", k.Permission(PermUserWrite), h.createUserGroup)
	v1.POST("/user-groups/:id", k.Permission(PermUserWrite), h.updateUserGroup)
	v1.DELETE("/user-groups/:id", k.Permission(PermUserWrite), h.deleteUserGroup)
	v1.POST("/me/password", k.RequireAuth, h.changePassword)
}

// strict 是注册与找回密码的严格限流：按路由加 IP、按网段，global 时再加整站一维；Redis 故障回 503。
func (h *Handler) strict(prefix string, global bool) gin.HandlerFunc {
	if h.limiter == nil {
		return func(c *gin.Context) { c.Next() }
	}
	dims := []ratelimit.Dimension{
		{Rule: ratelimit.PerMinute(prefix+"_ip", h.authPerMin).StrictMode(), Key: ratelimit.ByRoute},
		{Rule: ratelimit.PerWindow(prefix+"_net", h.authPerMin*6, 10*time.Minute).StrictMode(), Key: ratelimit.ByIPPrefix},
	}
	if global {
		name := "reg_global"
		if strings.HasPrefix(prefix, "pwreset") {
			name = "pwreset_global"
		}
		dims = append(dims, ratelimit.Dimension{
			Rule: ratelimit.PerWindow(name, h.authPerMin*20, time.Hour).StrictMode(),
			Key:  func(*gin.Context) (string, bool) { return "all", true },
		})
	}
	return h.limiter.Middleware(dims...)
}
