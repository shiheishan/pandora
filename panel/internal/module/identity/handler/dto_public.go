package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
)

// 注册第 1 步。
type registerStartRequest struct {
	Email      string `json:"email" binding:"max=320"`
	InviteCode string `json:"invite_code" binding:"max=64"`
}

type registerStartResponse struct {
	RegistrationToken    string `json:"registration_token"`
	VerificationRequired bool   `json:"verification_required"`
	ExpiresAt            string `json:"expires_at"` // RFC3339 UTC
	Message              string `json:"message"`
	DevCode              string `json:"dev_code,omitempty"`
}

// 注册第 2 步。
type registerCompleteRequest struct {
	RegistrationToken string `json:"registration_token" binding:"max=256"`
	Code              string `json:"code" binding:"max=16"`
	Password          string `json:"password" binding:"max=1024"`
}

type registerCompleteResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// 找回密码第 1 步。
type passwordResetStartRequest struct {
	Email string `json:"email" binding:"max=320"`
}

type passwordResetStartResponse struct {
	ExpiresAt string `json:"expires_at"` // RFC3339 UTC
	Message   string `json:"message"`
	DevCode   string `json:"dev_code,omitempty"`
}

// 找回密码第 2 步。
type passwordResetCompleteRequest struct {
	Email       string `json:"email" binding:"max=320"`
	Code        string `json:"code" binding:"max=16"`
	NewPassword string `json:"new_password" binding:"max=1024"`
}

type pubOKResponse struct {
	OK bool `json:"ok"`
}

// meResponse 是门户 /me。
type meResponse struct {
	UserID      string  `json:"user_id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"` // RFC3339 UTC
}

// 改密（门户与后台共用请求，响应分开）。
type changePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"max=1024"`
	NewPassword string `json:"new_password" binding:"max=1024"`
}

type changePasswordPublicResponse struct {
	OK bool `json:"ok"`
}

type changePasswordAdminResponse struct {
	OK             bool `json:"ok"`
	Reauthenticate bool `json:"reauthenticate"`
}

// 我的邀请。
type inviteResponse struct {
	Code      string    `json:"code"`
	Invited   int       `json:"invited"`
	MaxUses   *int      `json:"max_uses"`
	CreatedAt time.Time `json:"created_at"`
}

type inviteeResponse struct {
	Email    string    `json:"email"`
	BoundAt  time.Time `json:"bound_at"`
	RiskFlag string    `json:"risk_flag"`
}

type myInviteResponse struct {
	Invite   inviteResponse    `json:"invite"`
	Invitees []inviteeResponse `json:"invitees"`
}

const (
	msgRegisterStart = "若该邮箱可用于注册，验证码已发送"
	msgResetStart    = "若该邮箱已注册，验证码已发送"
)

func rfc3339UTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func myInviteFrom(inv service.Invite, invitees []service.Invitee) myInviteResponse {
	out := myInviteResponse{
		Invite: inviteResponse{
			Code:      inv.Code,
			Invited:   inv.Invited,
			MaxUses:   inv.MaxUses,
			CreatedAt: inv.CreatedAt,
		},
		Invitees: make([]inviteeResponse, len(invitees)),
	}
	for i, v := range invitees {
		out.Invitees[i] = inviteeResponse{Email: v.Email, BoundAt: v.BoundAt, RiskFlag: v.RiskFlag}
	}
	return out
}
