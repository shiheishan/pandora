package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

func (h *Handler) registerStart(c *gin.Context) {
	var req registerStartRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.StartRegistration(c.Request.Context(), req.Email, req.InviteCode)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, registerStartResponse{
		RegistrationToken:    out.Token,
		VerificationRequired: out.VerificationRequired,
		ExpiresAt:            rfc3339UTC(out.ExpiresAt),
		Message:              msgRegisterStart,
		DevCode:              out.DevCode,
	})
}

func (h *Handler) registerComplete(c *gin.Context) {
	var req registerCompleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	u, err := h.svc.CompleteRegistration(c.Request.Context(), req.RegistrationToken, req.Code, req.Password)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, registerCompleteResponse{UserID: u.ID.String(), Email: u.Email})
}

func (h *Handler) passwordResetStart(c *gin.Context) {
	var req passwordResetStartRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.StartPasswordReset(c.Request.Context(), req.Email)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, passwordResetStartResponse{
		ExpiresAt: rfc3339UTC(out.ExpiresAt),
		Message:   msgResetStart,
		DevCode:   out.DevCode,
	})
}

func (h *Handler) passwordResetComplete(c *gin.Context) {
	var req passwordResetCompleteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.CompletePasswordReset(c.Request.Context(), req.Email, req.Code, req.NewPassword); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, pubOKResponse{OK: true})
}

func (h *Handler) me(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	prof, err := h.svc.Profile(c.Request.Context(), p.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, meResponse{
		UserID:      prof.ID.String(),
		Email:       prof.Email,
		DisplayName: prof.DisplayName,
		Status:      prof.Status,
		CreatedAt:   rfc3339UTC(prof.CreatedAt),
		Permissions: []string{},
	})
}

// changePassword 门户与后台共用；后台改完连当前会话一起吊销，响应提示前端重新登录。
func (h *Handler) changePassword(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	var req changePasswordRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.ChangePassword(c.Request.Context(), req.OldPassword, req.NewPassword); err != nil {
		httpx.Fail(c, err)
		return
	}
	if p.Audience == authn.AudienceAdmin {
		httpx.OK(c, changePasswordAdminResponse{OK: true, Reauthenticate: true})
		return
	}
	httpx.OK(c, changePasswordPublicResponse{OK: true})
}

func (h *Handler) myInvite(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	inv, invitees, err := h.svc.MyInvite(c.Request.Context(), p.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, myInviteFrom(inv, invitees))
}
