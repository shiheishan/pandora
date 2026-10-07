package public

import (
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 自助找回密码的两步（契约 POST v1/auth/password-reset/start、/complete）。
// 规则在 identity/password_reset.go；这里只解请求、带上来源信息、写响应。

type passwordResetStartReq struct {
	Email string `json:"email"`
}

type passwordResetStartResponse struct {
	ExpiresAt string `json:"expires_at"`
	Message   string `json:"message"`
	DevCode   string `json:"dev_code,omitempty"`
}

func (h *handlers) passwordResetStart(w http.ResponseWriter, r *http.Request) {
	var req passwordResetStartReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Identity.StartPasswordReset(r.Context(), httpx.TenantIDFrom(r.Context()),
		identity.StartPasswordResetInput{
			Email: req.Email, IP: httpx.ClientIP(r), UserAgent: r.UserAgent(),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 邮箱存不存在，响应结构与内容都一样（IAM-006）
	httpx.OK(w, passwordResetStartResponse{
		ExpiresAt: out.ExpiresAt.UTC().Format(time.RFC3339),
		Message:   "若该邮箱已注册，验证码已发送",
		DevCode:   out.DevCode,
	})
}

type passwordResetCompleteReq struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

type passwordResetCompleteResponse struct {
	OK bool `json:"ok"`
}

func (h *handlers) passwordResetComplete(w http.ResponseWriter, r *http.Request) {
	var req passwordResetCompleteReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if err := h.d.Identity.CompletePasswordReset(r.Context(), httpx.TenantIDFrom(r.Context()),
		identity.CompletePasswordResetInput{
			Email: req.Email, Code: req.Code, NewPassword: req.NewPassword,
			IP: httpx.ClientIP(r), UserAgent: r.UserAgent(),
		}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, passwordResetCompleteResponse{OK: true})
}
