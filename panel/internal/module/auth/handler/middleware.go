package handler

import (
	"slices"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/auth/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// permissionsKey 是同一请求里权限列表的缓存键。
const permissionsKey = "auth.permissions"

var errLoginRequired = errs.Newf(errs.Unauthorized, "需要登录")

// bearerToken 取 Authorization: Bearer 后的令牌；没有返回空串。
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// Authenticate 是引擎级中间件：带了令牌就校验并把主体放进请求；没带放行，由 RequireAuth 拦。
func (h *Handler) Authenticate(audience string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := bearerToken(c)
		if raw == "" {
			c.Next()
			return
		}
		p, err := h.svc.Authenticate(c.Request.Context(), audience, raw)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		authn.Set(c, p)
		c.Next()
	}
}

// DomainGuard 是引擎级纵深防御：主体所属域与本引擎不符时拒绝。
func (h *Handler) DomainGuard(audience string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if p, ok := authn.FromGin(c); ok && p.Audience != audience {
			plog.From(c.Request.Context()).Warn("检测到跨域令牌使用",
				zap.String("token_audience", p.Audience), zap.String("gateway", audience))
			httpx.Fail(c, errs.Newf(errs.Unauthorized, "凭据无效"))
			return
		}
		c.Next()
	}
}

// RequireAuth 要求已登录。
func (h *Handler) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authn.FromGin(c); !ok {
			httpx.Fail(c, errLoginRequired)
			return
		}
		c.Next()
	}
}

// Permission 要求后台权限码：未登录或不是后台主体 401，缺权限 404（不暴露接口存在）。code 未登记时注册路由即 panic。
func (h *Handler) Permission(code string) gin.HandlerFunc {
	if !service.IsPermission(code) {
		panic("auth: 未登记的权限码 " + code)
	}
	return func(c *gin.Context) {
		p, ok := authn.FromGin(c)
		if !ok {
			httpx.Fail(c, errLoginRequired)
			return
		}
		if p.Audience != authn.AudienceAdmin {
			httpx.Fail(c, errs.New(errs.Unauthorized))
			return
		}
		perms, err := h.permissions(c, p)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if !slices.Contains(perms, code) {
			plog.From(c.Request.Context()).Info("权限不足", zap.String("required", code))
			httpx.Fail(c, errs.New(errs.NotFound))
			return
		}
		c.Next()
	}
}

// permissions 取主体的权限；同一请求只查一次。
func (h *Handler) permissions(c *gin.Context, p authn.Principal) ([]string, error) {
	if v, ok := c.Get(permissionsKey); ok {
		if perms, ok := v.([]string); ok {
			return perms, nil
		}
	}
	perms, err := h.svc.Permissions(c.Request.Context(), p.UserID)
	if err != nil {
		return nil, err
	}
	c.Set(permissionsKey, perms)
	return perms, nil
}

// RecentReauth 要求 15 分钟内重认证过。
func (h *Handler) RecentReauth() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := authn.FromGin(c)
		if !ok {
			httpx.Fail(c, errLoginRequired)
			return
		}
		if !p.RecentlyReauthed(time.Now()) {
			httpx.Fail(c, errs.New(errs.ReauthRequired))
			return
		}
		c.Next()
	}
}
