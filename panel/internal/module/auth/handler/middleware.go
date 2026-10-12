package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

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

// RequireAuth 要求已登录：主体由本引擎的 Authenticate 按本域校验过会话，DomainGuard 挡掉别的域。
// 后台不分权限：后台引擎上有效的会话就是管理员（取消管理员时其后台会话当场吊销）。
func (h *Handler) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authn.FromGin(c); !ok {
			httpx.Fail(c, errLoginRequired)
			return
		}
		c.Next()
	}
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
