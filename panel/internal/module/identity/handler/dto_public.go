package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
)

// 注册第 1 步。
type registerStartRequest struct {
	Email        string `json:"email" binding:"max=320"`
	InviteCode   string `json:"invite_code" binding:"max=64"`
	CaptchaToken string `json:"captcha_token" binding:"max=4096"`
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
	Email        string `json:"email" binding:"max=320"`
	CaptchaToken string `json:"captcha_token" binding:"max=4096"`
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

// 我的一个邀请码（门户邀请页的列表）。
type inviteCodeResponse struct {
	Code      string    `json:"code"`
	Status    string    `json:"status"`
	Invited   int       `json:"invited"`
	Visits    int64     `json:"visits"`
	MaxUses   *int      `json:"max_uses"`
	CreatedAt time.Time `json:"created_at"`
}

func inviteCodeFrom(v service.InviteCodeView) inviteCodeResponse {
	return inviteCodeResponse{
		Code:      v.Code,
		Status:    v.Status,
		Invited:   v.Invited,
		Visits:    v.Visits,
		MaxUses:   v.MaxUses,
		CreatedAt: v.CreatedAt.UTC(),
	}
}

type myInviteResponse struct {
	Invite   inviteResponse       `json:"invite"`
	Invitees []inviteeResponse    `json:"invitees"`
	Codes    []inviteCodeResponse `json:"codes"`
	MaxCodes int                  `json:"max_codes"`
}

const (
	msgRegisterStart = "若该邮箱可用于注册，验证码已发送"
	msgResetStart    = "若该邮箱已注册，验证码已发送"
)

func rfc3339UTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func myInviteFrom(inv service.Invite, invitees []service.Invitee, codes service.MyInviteCodes) myInviteResponse {
	out := myInviteResponse{
		Invite: inviteResponse{
			Code:      inv.Code,
			Invited:   inv.Invited,
			MaxUses:   inv.MaxUses,
			CreatedAt: inv.CreatedAt,
		},
		Invitees: make([]inviteeResponse, len(invitees)),
		Codes:    make([]inviteCodeResponse, len(codes.Codes)),
		MaxCodes: codes.MaxCodes,
	}
	for i, v := range invitees {
		out.Invitees[i] = inviteeResponse{Email: v.Email, BoundAt: v.BoundAt, RiskFlag: v.RiskFlag}
	}
	for i, v := range codes.Codes {
		out.Codes[i] = inviteCodeFrom(v)
	}
	return out
}
