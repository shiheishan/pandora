// Package handler 是 auth 模块的 HTTP 层：登录、刷新、登出、重认证、会话管理、快捷登录，以及全站用的鉴权中间件。
package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/auth/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Auth 是 handler 用到的 auth service 方法；*service.Service 实现它。
type Auth interface {
	Login(ctx context.Context, audience, email, password string) (service.Tokens, error)
	ConsumeQuickLogin(ctx context.Context, token string) (service.Tokens, error)
	Refresh(ctx context.Context, audience, raw string) (service.Tokens, error)
	Logout(ctx context.Context, audience, rawRefresh string) error
	Authenticate(ctx context.Context, audience, bearer string) (authn.Principal, error)
	Permissions(ctx context.Context, userID uuid.UUID) ([]string, error)
	AdminProfile(ctx context.Context) (service.AdminProfile, error)
	ListSessions(ctx context.Context) ([]service.SessionInfo, error)
	RevokeSession(ctx context.Context, sid string) error
	IssueQuickLogin(ctx context.Context) (service.QuickLoginLink, error)
	Reauth(ctx context.Context, password string) (service.Tokens, error)
}

var _ Auth = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Options 是 handler 的可调参数。
type Options struct {
	AuthPerMin   int  // config.RateLimit.AuthPerMin：认证入口每分钟次数
	SecureCookie bool // refresh cookie 带 Secure（生产必须为 true）
}

// Handler 是 auth 模块的 HTTP 入口。
type Handler struct {
	svc Auth
	lim *ratelimit.Limiter // nil 时不挂限流（单测用）
	opt Options
	cop *http.CrossOriginProtection
}

// New 构造 Handler。
func New(svc Auth, lim *ratelimit.Limiter, opt Options) *Handler {
	if opt.AuthPerMin < 1 {
		opt.AuthPerMin = 10
	}
	return &Handler{svc: svc, lim: lim, opt: opt, cop: http.NewCrossOriginProtection()}
}

// limit 挂限流；没有 Limiter 时放行。
func (h *Handler) limit(dims ...ratelimit.Dimension) gin.HandlerFunc {
	if h.lim == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return h.lim.Middleware(dims...)
}

// byAccount 以登录主体的用户 ID 为限流键。
func byAccount(c *gin.Context) (string, bool) {
	p, ok := authn.FromGin(c)
	if !ok {
		return "", false
	}
	return p.UserID.String(), true
}

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	n := h.opt.AuthPerMin
	authLimit := h.limit(
		ratelimit.Dimension{Rule: ratelimit.PerMinute("auth_route", n).StrictMode(), Key: ratelimit.ByRoute},
		ratelimit.Dimension{Rule: ratelimit.PerWindow("auth_net", n*6, 10*time.Minute).StrictMode(), Key: ratelimit.ByIPPrefix},
	)
	v1.POST("/auth/login", authLimit, h.login(authn.AudiencePublic))
	v1.POST("/auth/quick-login", authLimit, h.consumeQuickLogin)
	v1.POST("/auth/refresh", h.sameOrigin,
		h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("auth_refresh", n*6).StrictMode(), Key: ratelimit.ByIP}),
		h.refresh(authn.AudiencePublic))
	// 不挂 RequireAuth：access 过期时凭 cookie 也能登出
	v1.POST("/auth/logout", h.sameOrigin, h.logout(authn.AudiencePublic))
	v1.POST("/me/quick-login", k.RequireAuth, h.issueQuickLogin)
	v1.GET("/me/sessions", k.RequireAuth, h.listSessions)
	v1.DELETE("/me/sessions/:id", k.RequireAuth, h.revokeSession)
}

// RegisterAdmin 注册管理端路由。/auth/login、/auth/refresh、/auth/logout、/auth/reauth 不能改名：后台只读模式按这几个模板放行。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	n := h.opt.AuthPerMin
	v1.POST("/auth/login",
		h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("adm_auth", n).StrictMode(), Key: ratelimit.ByRoute}),
		h.login(authn.AudienceAdmin))
	v1.POST("/auth/refresh", h.sameOrigin,
		h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("adm_refresh", n*6).StrictMode(), Key: ratelimit.ByIP}),
		h.refresh(authn.AudienceAdmin))
	v1.POST("/auth/logout", h.sameOrigin, h.logout(authn.AudienceAdmin))
	v1.POST("/auth/reauth", k.RequireAuth,
		h.limit(
			ratelimit.Dimension{Rule: ratelimit.PerMinute("adm_reauth", n).StrictMode(), Key: byAccount},
			ratelimit.Dimension{Rule: ratelimit.PerMinute("adm_reauth_ip", n).StrictMode(), Key: ratelimit.ByIP},
		),
		h.reauth)
	v1.GET("/me", k.RequireAuth, h.adminMe)
}

func (h *Handler) login(aud string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req loginRequest
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}
		if strings.Contains(req.Email, "\x00") {
			httpx.Fail(c, errs.Invalid(map[string]string{"email": "格式不正确"}))
			return
		}
		if strings.Contains(req.Password, "\x00") {
			httpx.Fail(c, errs.Invalid(map[string]string{"password": "格式不正确"}))
			return
		}
		t, err := h.svc.Login(c.Request.Context(), aud, req.Email, req.Password)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		h.setRefreshCookie(c, aud, t.Refresh, t.RefreshExpires)
		if aud == authn.AudienceAdmin {
			perms := t.Permissions
			if perms == nil {
				perms = []string{}
			}
			httpx.OK(c, adminLoginResponse{
				AccessToken: t.AccessToken,
				ExpiresIn:   t.ExpiresIn,
				Permissions: perms,
				TokenType:   "Bearer",
				UserID:      t.UserID.String(),
			})
			return
		}
		httpx.OK(c, loginResponse{
			AccessToken: t.AccessToken,
			TokenType:   "Bearer",
			ExpiresIn:   t.ExpiresIn,
			UserID:      t.UserID.String(),
		})
	}
}

func (h *Handler) consumeQuickLogin(c *gin.Context) {
	var req quickLoginRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.ConsumeQuickLogin(c.Request.Context(), req.Token)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	h.setRefreshCookie(c, authn.AudiencePublic, t.Refresh, t.RefreshExpires)
	httpx.OK(c, tokenOut(t))
}

func (h *Handler) refresh(aud string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(cookieName(aud))
		if err != nil || raw == "" {
			httpx.Fail(c, errs.New(errs.Unauthorized))
			return
		}
		t, err := h.svc.Refresh(c.Request.Context(), aud, raw)
		if err != nil {
			if errs.As(err).Code == errs.Unauthorized {
				h.clearRefreshCookie(c, aud)
			}
			httpx.Fail(c, err)
			return
		}
		h.setRefreshCookie(c, aud, t.Refresh, t.RefreshExpires)
		httpx.OK(c, tokenOut(t))
	}
}

func (h *Handler) logout(aud string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, _ := c.Cookie(cookieName(aud))
		if err := h.svc.Logout(c.Request.Context(), aud, raw); err != nil {
			httpx.Fail(c, err)
			return
		}
		h.clearRefreshCookie(c, aud)
		httpx.NoContent(c)
	}
}

func (h *Handler) issueQuickLogin(c *gin.Context) {
	l, err := h.svc.IssueQuickLogin(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, quickLoginResponse{
		Token:     l.Token,
		ExpiresAt: l.ExpiresAt.UTC().Format(time.RFC3339),
		ExpiresIn: l.ExpiresIn,
	})
}

func (h *Handler) listSessions(c *gin.Context) {
	list, err := h.svc.ListSessions(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]sessionDTO, 0, len(list))
	for _, s := range list {
		out = append(out, sessionDTO{
			ID:         s.ID,
			Current:    s.Current,
			UserAgent:  s.UserAgent,
			CreatedAt:  s.CreatedAt.UTC().Format(time.RFC3339),
			LastSeenAt: s.LastSeenAt.UTC().Format(time.RFC3339),
			ExpiresAt:  s.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	httpx.OK(c, sessionsResponse{Sessions: out})
}

func (h *Handler) revokeSession(c *gin.Context) {
	if err := h.svc.RevokeSession(c.Request.Context(), c.Param("id")); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, revokeSessionResponse{Revoked: true})
}

func (h *Handler) reauth(c *gin.Context) {
	var req reauthRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.Reauth(c.Request.Context(), req.Password)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, tokenOut(t))
}

func (h *Handler) adminMe(c *gin.Context) {
	p, err := h.svc.AdminProfile(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	perms := p.Permissions
	if perms == nil {
		perms = []string{}
	}
	roles := make([]roleDTO, 0, len(p.Roles))
	for _, r := range p.Roles {
		roles = append(roles, roleDTO{Code: r.Code, Name: r.Name})
	}
	httpx.OK(c, adminMeResponse{
		UserID:      p.UserID.String(),
		Email:       p.Email,
		DisplayName: nil,
		Kind:        "user",
		Permissions: perms,
		Reauthed:    p.Reauthed,
		Roles:       roles,
	})
}

func tokenOut(t service.Tokens) tokenResponse {
	return tokenResponse{AccessToken: t.AccessToken, TokenType: "Bearer", ExpiresIn: t.ExpiresIn}
}
