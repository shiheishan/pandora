package handler

import (
	"strings"

	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

var switchMessages = map[string]string{
	service.SwitchBillingCheckout: "下单与支付暂停中，请稍后再试",
	service.SwitchGiftCardRedeem:  "礼品卡兑换暂停中，请稍后再试",
}

// Switch 在开关关闭时回 503 service_unavailable；装进 route.Kit.Switch，排在 Idempotency 之前（被拒的请求不消耗幂等键）。
func (h *Handler) Switch(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		enabled, err := h.svc.SwitchEnabled(ctx, code)
		if err != nil {
			httpx.Fail(c, errs.Wrap(errs.Internal, err))
			return
		}
		if !enabled {
			msg := switchMessages[code]
			if msg == "" {
				msg = "该功能暂停中，请稍后再试"
			}
			httpx.Fail(c, errs.Newf(errs.Unavailable, msg))
			return
		}
		c.Next()
	}
}

// AdminWritesGate 在 admin.writes 关闭时后台只读：除 GET/HEAD/OPTIONS 与 adminWriteAllowlist 外一律 503。装在 admin 引擎上（按 FullPath 路由模板判断）。
func (h *Handler) AdminWritesGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if adminWriteExempt(c.Request.Method, v1Path(c)) {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		enabled, err := h.svc.SwitchEnabled(ctx, service.SwitchAdminWrites)
		if err != nil {
			httpx.Fail(c, errs.Wrap(errs.Internal, err))
			return
		}
		if !enabled {
			httpx.Fail(c, errs.Newf(errs.Unavailable, "管理端只读模式"))
			return
		}
		c.Next()
	}
}

func v1Path(c *gin.Context) string {
	p := c.FullPath()
	if p == "" {
		p = c.Request.URL.Path
	}
	if i := strings.Index(p, "/v1/"); i >= 0 {
		return p[i+len("/v1"):]
	}
	return p
}

// adminWriteAllowlist 是只读模式下仍放行的写接口（路由模板，去掉 /v1 前缀）：
// 登录、刷新、登出、重认证、改自己密码、切开关本身。逐条列出，新挂的写接口默认受只读模式约束。
var adminWriteAllowlist = map[string]bool{
	"POST /auth/login":     true,
	"POST /auth/refresh":   true,
	"POST /auth/logout":    true,
	"POST /auth/reauth":    true,
	"POST /me/password":    true,
	"POST /switches/:code": true,
}

func adminWriteExempt(method, path string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return adminWriteAllowlist[method+" "+path]
}
