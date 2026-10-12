package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
)

// 后台用户列表查询参数。
type listUsersQuery struct {
	Q        string `form:"q" binding:"max=200"`
	Status   string `form:"status" binding:"max=100"`
	GroupID  string `form:"group_id" binding:"max=64"`
	SubState string `form:"sub_state" binding:"omitempty,oneof=active expired none"` // 订阅模块接入前收下但不生效
}

// userRowResponse 是后台用户列表与详情共用的行形状。
type userRowResponse struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName *string    `json:"display_name"`
	Status      string     `json:"status"`
	RiskLevel   string     `json:"risk_level"`
	GroupName   string     `json:"group_name"`
	GroupID     *string    `json:"group_id"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}

type listUsersResponse struct {
	Users []userRowResponse `json:"users"`
	Total int64             `json:"total"`
}

type admUserRefResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// userDetailResponse 是后台用户详情（identity 字段）。
type userDetailResponse struct {
	userRowResponse
	EmailVerified bool                 `json:"email_verified"`
	IsAdmin       bool                 `json:"is_admin"`
	Referrer      *admUserRefResponse  `json:"referrer"`
	Stats         admUserStatsResponse `json:"stats"`
}

// admUserStatsResponse 是详情里的统计；identity 只填 referral_count，其余字段（如 paid_totals、order_count）由别的模块接入时补。
type admUserStatsResponse struct {
	ReferralCount int64 `json:"referral_count"`
}

type admSetUserStatusRequest struct {
	Status string `json:"status" binding:"required,max=32"`
	Reason string `json:"reason" binding:"max=2000"`
}

type admSetUserStatusResponse struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
}

type admResetUserPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"max=1024"`
	Reason      string `json:"reason" binding:"max=2000"`
}

type admResetUserPasswordResponse struct {
	OK              bool `json:"ok"`
	SessionsRevoked bool `json:"sessions_revoked"`
}

type admAssignUserGroupRequest struct {
	GroupID string `json:"group_id" binding:"max=64"`
}

type admOKResponse struct {
	OK bool `json:"ok"`
}

type admUserGroupRow struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Users       int64  `json:"users"`
}

type admListUserGroupsResponse struct {
	Groups []admUserGroupRow `json:"groups"`
}

type admUserGroupBodyRequest struct {
	Code        string `json:"code" binding:"max=64"`
	Name        string `json:"name" binding:"max=200"`
	Description string `json:"description" binding:"max=2000"`
}

type admCreateUserGroupResponse struct {
	ID string `json:"id"`
}

type admUpdateUserGroupResponse struct {
	ID string `json:"id"`
}

func admSplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func admUserRowFrom(u service.UserRow) userRowResponse {
	var gid *string
	if u.GroupID != nil {
		s := u.GroupID.String()
		gid = &s
	}
	return userRowResponse{
		ID:          u.ID.String(),
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Status:      u.Status,
		RiskLevel:   u.RiskLevel,
		GroupName:   u.GroupName,
		GroupID:     gid,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

func admUserDetailFrom(d service.UserDetail) userDetailResponse {
	out := userDetailResponse{
		userRowResponse: admUserRowFrom(d.UserRow),
		EmailVerified:   d.EmailVerified,
		IsAdmin:         d.IsAdmin,
		Stats:           admUserStatsResponse{ReferralCount: d.ReferralCount},
	}
	if d.Referrer != nil {
		out.Referrer = &admUserRefResponse{
			ID:    d.Referrer.ID.String(),
			Email: d.Referrer.Email,
		}
	}
	return out
}

func admGroupRowFrom(g service.Group) admUserGroupRow {
	return admUserGroupRow{
		ID:          g.ID.String(),
		Code:        g.Code,
		Name:        g.Name,
		Description: g.Description,
		Users:       g.Users,
	}
}
