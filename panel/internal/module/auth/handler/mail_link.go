package handler

import (
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type mailLinkStartRequest struct {
	Email string `json:"email" binding:"required,max=320"`
	// 开了人机验证时必填（与注册、找回第 1 步同一字段）
	CaptchaToken string `json:"captcha_token" binding:"max=4096"`
}

// mailLinkStartResponse 是发登录链接的响应：不论邮箱是否注册都一样，不泄露账号是否存在。
type mailLinkStartResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type mailLinkLoginRequest struct {
	Token string `json:"token" binding:"required,max=128"`
}

func (h *Handler) startMailLink(c *gin.Context) {
	var req mailLinkStartRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if strings.Contains(req.Email, "\x00") {
		httpx.Fail(c, errs.Invalid(map[string]string{"email": "邮箱格式不正确"}))
		return
	}
	if err := h.svc.StartMailLogin(c.Request.Context(), req.Email, req.CaptchaToken); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, mailLinkStartResponse{
		OK:      true,
		Message: "如果这个邮箱已注册，登录链接已发出，15 分钟内有效",
	})
}

func (h *Handler) consumeMailLink(c *gin.Context) {
	var req mailLinkLoginRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.ConsumeMailLogin(c.Request.Context(), req.Token)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	h.setRefreshCookie(c, authn.AudiencePublic, t.Refresh, t.RefreshExpires)
	httpx.OK(c, tokenOut(t))
}
