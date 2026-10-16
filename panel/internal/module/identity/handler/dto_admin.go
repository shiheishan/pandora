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
	SubState string `form:"sub_state" binding:"omitempty,oneof=active expired none"` // 订阅情况，经 subscription 端口圈人
}

// userRowResponse 是后台用户列表与详情共用的行形状。
type userRowResponse struct {
	ID                  string                 `json:"id"`
	Email               string                 `json:"email"`
	DisplayName         *string                `json:"display_name"`
	Status              string                 `json:"status"`
	RiskLevel           string                 `json:"risk_level"`
	GroupName           string                 `json:"group_name"`
	GroupID             *string                `json:"group_id"`
	CreatedAt           time.Time              `json:"created_at"`
	LastLoginAt         *time.Time             `json:"last_login_at"`
	SubscriptionCount   int                    `json:"subscription_count"`
	ActivePlan          *string                `json:"active_plan"`
	Balance             int64                  `json:"balance"`
	CurrentSubscription *admCurrentSubResponse `json:"current_subscription"`
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
	EmailVerified       bool                      `json:"email_verified"`
	IsAdmin             bool                      `json:"is_admin"`
	Referrer            *admUserRefResponse       `json:"referrer"`
	Stats               admUserStatsResponse      `json:"stats"`
	Subscriptions       []admSubscriptionResponse `json:"subscriptions"`
	RecentOrders        []admOrderRowResponse     `json:"recent_orders"`
	Telegram            *admTelegramResponse      `json:"telegram"`
	UnattachedPackBytes int64                     `json:"unattached_pack_bytes"`
	AdminNote           *string                   `json:"admin_note"` // 后台备注，只在后台详情出现
	DeletedAt           *time.Time                `json:"deleted_at"` // 注销时间；没注销为 null
}

// admUserStatsResponse 是详情里的统计（实收、订单数、邀请数）。
type admUserStatsResponse struct {
	PaidTotal     int64 `json:"paid_total"`
	OrderCount    int64 `json:"order_count"`
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
	ID             string               `json:"id"`
	Code           string               `json:"code"`
	Name           string               `json:"name"`
	Description    string               `json:"description"`
	Users          int64                `json:"users"`
	Status         string               `json:"status"`
	Plans          int64                `json:"plans"`
	Prices         int64                `json:"prices"`
	Coupons        int64                `json:"coupons"`
	ExclusivePools []admPoolRefResponse `json:"exclusive_pools"`
}

type admListUserGroupsResponse struct {
	Groups []admUserGroupRow `json:"groups"`
}

type admUserGroupBodyRequest struct {
	Code        string `json:"code" binding:"max=64"`
	Name        string `json:"name" binding:"max=200"`
	Description string `json:"description" binding:"max=2000"`
}

// admSetUserGroupStatusRequest 是 POST /v1/user-groups/:id/status 的请求体：disabled 为 true 停用、false 启用。
type admSetUserGroupStatusRequest struct {
	Disabled *bool `json:"disabled" binding:"required"`
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

func admUserRowFrom(cc *admCents, u service.UserRow) userRowResponse {
	var gid *string
	if u.GroupID != nil {
		s := u.GroupID.String()
		gid = &s
	}
	return userRowResponse{
		ID:                  u.ID.String(),
		Email:               u.Email,
		DisplayName:         u.DisplayName,
		Status:              u.Status,
		RiskLevel:           u.RiskLevel,
		GroupName:           u.GroupName,
		GroupID:             gid,
		CreatedAt:           u.CreatedAt,
		LastLoginAt:         u.LastLoginAt,
		SubscriptionCount:   u.SubscriptionCount,
		ActivePlan:          u.ActivePlan,
		Balance:             cc.of(u.Balance),
		CurrentSubscription: admCurrentSubFrom(u.Current),
	}
}

func admUserDetailFrom(d service.UserDetail) (userDetailResponse, error) {
	var cc admCents
	out := userDetailResponse{
		userRowResponse:     admUserRowFrom(&cc, d.UserRow),
		EmailVerified:       d.EmailVerified,
		IsAdmin:             d.IsAdmin,
		Subscriptions:       make([]admSubscriptionResponse, 0, len(d.Subscriptions)),
		RecentOrders:        make([]admOrderRowResponse, 0, len(d.RecentOrders)),
		UnattachedPackBytes: d.UnattachedPackBytes,
	}
	for _, s := range d.Subscriptions {
		out.Subscriptions = append(out.Subscriptions, admSubscriptionFrom(&cc, s))
	}
	for _, o := range d.RecentOrders {
		out.RecentOrders = append(out.RecentOrders, admOrderRowFrom(&cc, o))
	}
	out.Stats = admUserStatsResponse{
		PaidTotal:     cc.of(d.PaidTotal),
		OrderCount:    d.OrderCount,
		ReferralCount: d.ReferralCount,
	}
	if d.Referrer != nil {
		out.Referrer = &admUserRefResponse{
			ID:    d.Referrer.ID.String(),
			Email: d.Referrer.Email,
		}
	}
	if d.Telegram != nil {
		out.Telegram = &admTelegramResponse{
			Username: d.Telegram.Username,
			BoundAt:  d.Telegram.BoundAt,
		}
	}
	out.AdminNote = d.AdminNote
	if d.DeletedAt != nil {
		t := d.DeletedAt.UTC()
		out.DeletedAt = &t
	}
	return out, cc.err
}

func admGroupRowFrom(g service.Group) admUserGroupRow {
	pools := make([]admPoolRefResponse, 0, len(g.ExclusivePools))
	for _, p := range g.ExclusivePools {
		pools = append(pools, admPoolRefResponse{ID: p.ID.String(), Name: p.Name})
	}
	return admUserGroupRow{
		ID:             g.ID.String(),
		Code:           g.Code,
		Name:           g.Name,
		Description:    g.Description,
		Users:          g.Users,
		Status:         g.Status,
		Plans:          g.Plans,
		Prices:         g.Prices,
		Coupons:        g.Coupons,
		ExclusivePools: pools,
	}
}
