package handler

import (
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

const (
	publicRefreshCookie = "pandora_rt"
	adminRefreshCookie  = "pandora_admin_rt"
	// refresh cookie 只随 /v1/auth/* 发出（refresh、logout）
	refreshCookiePath = "/v1/auth"
)

// cookieName 返回该域的 refresh cookie 名。
func cookieName(aud string) string {
	if aud == authn.AudienceAdmin {
		return adminRefreshCookie
	}
	return publicRefreshCookie
}

// setRefreshCookie 写入 refresh cookie；value 为空表示不改。
func (h *Handler) setRefreshCookie(c *gin.Context, aud, value string, expires time.Time) {
	if value == "" {
		return
	}
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cookieName(aud),
		Value:    value,
		Path:     refreshCookiePath,
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   h.opt.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearRefreshCookie 让浏览器删掉 refresh cookie。
func (h *Handler) clearRefreshCookie(c *gin.Context, aud string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cookieName(aud),
		Value:    "",
		Path:     refreshCookiePath,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.opt.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

// sameOrigin 挡跨站请求（凭 cookie 的 refresh、logout）：用标准库 CrossOriginProtection 看 Sec-Fetch-Site / Origin。
func (h *Handler) sameOrigin(c *gin.Context) {
	if err := h.cop.Check(c.Request); err != nil {
		httpx.Fail(c, errs.New(errs.Forbidden))
		return
	}
	c.Next()
}
