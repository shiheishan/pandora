// Package handler 是 identity 模块的 HTTP 层：注册、找回密码、个人资料与改密、邀请，以及后台用户与用户组管理、批量运营、用户画像、访问明细与同源 IP 风控。
package handler

import (
	"context"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 identity service 方法；*service.Service 实现它。
type Service interface {
	StartRegistration(ctx context.Context, email, inviteCode, captchaToken string) (service.RegistrationStart, error)
	CompleteRegistration(ctx context.Context, token, code, password string) (service.User, error)
	StartPasswordReset(ctx context.Context, email, captchaToken string) (service.PasswordResetStart, error)
	CompletePasswordReset(ctx context.Context, email, code, newPassword string) error
	Profile(ctx context.Context, id uuid.UUID) (service.Profile, error)
	ChangePassword(ctx context.Context, oldPassword, newPassword string) error
	MyInvite(ctx context.Context, userID uuid.UUID) (service.Invite, []service.Invitee, error)
	ListUsers(ctx context.Context, f service.UserFilter, limit, offset int) ([]service.UserRow, int64, error)
	UserDetail(ctx context.Context, id uuid.UUID) (service.UserDetail, error)
	SetStatus(ctx context.Context, userID uuid.UUID, status, reason string) error
	AdminResetPassword(ctx context.Context, userID uuid.UUID, newPassword, reason string) error
	AssignGroup(ctx context.Context, userID uuid.UUID, groupID *uuid.UUID) error
	AdminListGroups(ctx context.Context) ([]service.Group, error)
	CreateGroup(ctx context.Context, code, name, description string) (uuid.UUID, error)
	UpdateGroup(ctx context.Context, id uuid.UUID, name, description string) error
	DeleteGroup(ctx context.Context, id uuid.UUID) error
	SetGroupStatus(ctx context.Context, id uuid.UUID, disabled bool) error

	PreviewBulk(ctx context.Context, f service.BulkFilter) (service.BulkPreview, error)
	ExportUsers(ctx context.Context, f service.BulkFilter, limit int) ([]service.ExportRow, error)
	SubmitGeneration(ctx context.Context, in service.GenerateInput) (service.GenerationJob, error)
	ListGenerationJobs(ctx context.Context) ([]service.GenerationJob, error)
	GetGenerationJob(ctx context.Context, id uuid.UUID) (service.GenerationJob, error)
	GenerationResult(ctx context.Context, id uuid.UUID) ([]service.GeneratedUser, error)
	UserProfile(ctx context.Context, id uuid.UUID) (service.UserProfile, error)
	AccessLog(ctx context.Context, q service.AccessLogQuery) ([]service.AccessItem, error)
	IPClusters(ctx context.Context, includeReviewed bool) ([]service.IPCluster, error)
	ReviewIPCluster(ctx context.Context, key, note string) (time.Time, error)
	DisableIPClusterAccounts(ctx context.Context, key string, ids []uuid.UUID, reason string) (service.DisableClusterResult, error)

	// 注册与邀请、后台用户操作（xb-reg）
	MyInviteCodes(ctx context.Context, userID uuid.UUID) (service.MyInviteCodes, error)
	CreateInviteCode(ctx context.Context, userID uuid.UUID) (service.InviteCodeView, error)
	RecordInviteVisit(ctx context.Context, code string) error
	AdminSetReferrer(ctx context.Context, refereeID uuid.UUID, referrerID *uuid.UUID, reason string) error
	AdminSetEmail(ctx context.Context, userID uuid.UUID, email, reason string) error
	AdminSetNote(ctx context.Context, userID uuid.UUID, displayName, adminNote *string) error
	DeleteUser(ctx context.Context, userID uuid.UUID, reason string) (service.DeleteUserResult, error)
	BulkBan(ctx context.Context, f service.BulkFilter, expectedTotal int64, reason string) (service.BulkBanResult, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

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
	v1.POST("/auth/register/start", h.strict("reg_start", true, false), h.registerStart)
	v1.POST("/auth/register/complete", h.strict("reg_complete", true, true), h.registerComplete)
	v1.POST("/auth/password-reset/start", h.strict("pwreset_start", true, true), h.passwordResetStart)
	v1.POST("/auth/password-reset/complete", h.strict("pwreset_complete", false, true), h.passwordResetComplete)
	v1.GET("/me", k.RequireAuth, h.me)
	v1.POST("/me/password", k.RequireAuth, h.mePasswordLimit(), h.changePassword)
	v1.GET("/me/invite", k.RequireAuth, h.myInvite)
	v1.POST("/me/invite/codes", k.RequireAuth, h.createInviteCode)
	// 邀请链接访问量：公开、按 IP 限频（Redis 故障放行），后端按 IP 去重计数、不存 IP
	v1.POST("/invite/visit", h.inviteVisitLimit(), h.inviteVisit)
}

// RegisterAdmin 注册后台路由。后台不分权限：已登录的后台主体（管理员）就能访问。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/users/bulk/preview", k.RequireAuth, h.bulkPreview)
	v1.GET("/users/bulk/export", k.RequireAuth, k.RecentReauth, h.bulkExport)
	v1.POST("/users/bulk/generate", k.RequireAuth, k.RecentReauth, k.Idempotency("user_bulk_generate"), h.bulkGenerate)
	v1.GET("/users/bulk/generate/jobs", k.RequireAuth, h.listGenerationJobs)
	v1.GET("/users/bulk/generate/jobs/:id", k.RequireAuth, h.getGenerationJob)
	v1.GET("/users/bulk/generate/jobs/:id/result", k.RequireAuth, k.RecentReauth, h.generationResult)
	v1.GET("/users/:id/profile", k.RequireAuth, h.userProfile)
	v1.GET("/access-log", k.RequireAuth, h.accessLog)
	v1.GET("/ip-clusters", k.RequireAuth, h.ipClusters)
	v1.POST("/ip-clusters/:key/review", k.RequireAuth, h.reviewIPCluster)
	v1.POST("/ip-clusters/:key/disable-accounts", k.RequireAuth, k.RecentReauth, k.Idempotency("ip_cluster_disable"), h.disableIPCluster)

	v1.GET("/users", k.RequireAuth, h.listUsers)
	v1.GET("/users/:id", k.RequireAuth, h.getUser)
	v1.POST("/users/:id/status", k.RequireAuth, k.RecentReauth, h.setUserStatus)
	v1.POST("/users/:id/reset-password", k.RequireAuth, k.RecentReauth, h.resetUserPassword)
	v1.POST("/users/:id/group", k.RequireAuth, h.assignUserGroup)
	v1.GET("/user-groups", k.RequireAuth, h.listUserGroups)
	v1.POST("/user-groups", k.RequireAuth, h.createUserGroup)
	v1.POST("/user-groups/:id", k.RequireAuth, h.updateUserGroup)
	v1.DELETE("/user-groups/:id", k.RequireAuth, h.deleteUserGroup)
	v1.POST("/user-groups/:id/status", k.RequireAuth, h.setUserGroupStatus)
	// 后台改用户字段、注销、改邀请人、按筛选批量封禁（xb-reg）：改邮箱、注销、改邀请人、批量封禁要近期重认证，批量封禁另挂幂等
	v1.POST("/users/bulk/ban", k.RequireAuth, k.RecentReauth, k.Idempotency("user_bulk_ban"), h.bulkBan)
	v1.POST("/users/:id/email", k.RequireAuth, k.RecentReauth, h.setUserEmail)
	v1.POST("/users/:id/note", k.RequireAuth, h.setUserNote)
	v1.POST("/users/:id/delete", k.RequireAuth, k.RecentReauth, h.deleteUser)
	v1.POST("/users/:id/referrer", k.RequireAuth, k.RecentReauth, h.setUserReferrer)
	v1.POST("/me/password", k.RequireAuth, h.mePasswordLimit(), h.changePassword)
}

// strict 是注册与找回密码的严格限流：perIP 为 true 时加按路由加 IP 一维；按网段，global 时再加整站一维；Redis 故障回 503。
// 注册的按 IP 限额改由后台设置 signup.ip_limit_* 在 service 里管，reg_start 传 perIP=false。
func (h *Handler) strict(prefix string, global, perIP bool) gin.HandlerFunc {
	if h.limiter == nil {
		return func(c *gin.Context) { c.Next() }
	}
	dims := []ratelimit.Dimension{
		{Rule: ratelimit.PerWindow(prefix+"_net", h.authPerMin*6, 10*time.Minute).StrictMode(), Key: ratelimit.ByIPPrefix},
	}
	if perIP {
		dims = append([]ratelimit.Dimension{
			{Rule: ratelimit.PerMinute(prefix+"_ip", h.authPerMin).StrictMode(), Key: ratelimit.ByRoute},
		}, dims...)
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

// mePasswordLimit 是改密接口的严格限流：按账号、按 IP 两维，每分钟 authPerMin 次。
// 挂在 RequireAuth 之后，按账号那一维取登录主体；没有主体时跳过该维（RequireAuth 已经拦下）。
func (h *Handler) mePasswordLimit() gin.HandlerFunc {
	if h.limiter == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return h.limiter.Middleware(
		ratelimit.Dimension{
			Rule: ratelimit.PerMinute("me_password", h.authPerMin).StrictMode(),
			Key: func(c *gin.Context) (string, bool) {
				p, ok := authn.FromGin(c)
				if !ok {
					return "", false
				}
				return p.UserID.String(), true
			},
		},
		ratelimit.Dimension{Rule: ratelimit.PerMinute("me_password_ip", h.authPerMin).StrictMode(), Key: ratelimit.ByIP},
	)
}
