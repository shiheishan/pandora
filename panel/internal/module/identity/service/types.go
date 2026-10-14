package service

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 本文件是 service 对外方法用到的输入、输出类型（handler 与别的模块只看这些，不看 model）。

// 用户状态（与迁移的 CHECK 一致）。
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusBanned    = "banned"
)

// 验证码用途（与迁移的 CHECK 一致）。
const (
	PurposeEmailVerify   = "email_verify"
	PurposePasswordReset = "password_reset"
)

// RegistrationPending 是注册会话待验证的状态（消费后由 repo 的 SQL 改成 consumed）。
const RegistrationPending = "pending"

// 审计动作名（沿用旧代码）与资源类型。
const (
	ActionRegistered           = "user.registered"
	ActionCreated              = "user.created"
	ActionPasswordChanged      = "user.password_changed"
	ActionPasswordResetRequest = "user.password_reset_requested"
	ActionPasswordReset        = "user.password_reset"
	ActionPasswordResetByAdmin = "user.password_reset_by_admin"
	ActionStatusChange         = "user.status_change"
	ActionGroupChanged         = "user.group_changed"
	ActionUserGroupSaved       = "user_group.saved"
	ActionUserGroupDeleted     = "user_group.deleted"

	ResourceUser      = "user"
	ResourceUserGroup = "user_group"
)

// 验证码邮件模板（notify 模块种的模板码）。
const (
	TemplateEmailVerify   = "auth.email_verify"
	TemplatePasswordReset = "auth.password_reset"
)

// 流程参数。有效期同时决定库里的 expires_at 与邮件里写的分钟数。
const (
	registrationTTL          = 10 * time.Minute
	passwordResetTTL         = 10 * time.Minute
	passwordResetMaxAttempts = 5
	registrationMaxAttempts  = 5
	registrationTokenBytes   = 32
	verificationCodeDigits   = 6
	inviteesLimit            = 100
	directoryMaxLimit        = 1000
)

// Profile 是门户与后台 /me 要的账户资料。
type Profile struct {
	ID          uuid.UUID
	Email       string
	DisplayName *string
	Status      string
	CreatedAt   time.Time
}

// RegistrationStart 是注册第 1 步的结果。账号存不存在，除 DevCode 外完全一样（DevCode 只在非生产环境出现）。
type RegistrationStart struct {
	Token                string
	ExpiresAt            time.Time
	VerificationRequired bool
	DevCode              string
}

// PasswordResetStart 是找回密码第 1 步的结果；DevCode 只在非生产环境且确实发了码时非空。
type PasswordResetStart struct {
	ExpiresAt time.Time
	DevCode   string
}

// Invite 是用户自己的邀请码概况。
type Invite struct {
	Code      string
	Invited   int
	MaxUses   *int
	CreatedAt time.Time
}

// Invitee 是我邀请来的一个人；Email 已打码。
type Invitee struct {
	Email    string
	BoundAt  time.Time
	RiskFlag string
}

// UserFilter 是后台用户列表的筛选；零值不筛。
type UserFilter struct {
	// Query 按邮箱、显示名片段（不分大小写）或用户 id 精确匹配
	Query string
	// Statuses 等值匹配任一
	Statuses []string
	// GroupID 为 nil 不筛；NoGroup 为 true 时只要未分组的
	GroupID *uuid.UUID
	NoGroup bool
	// SubState 是 active / expired / none；空不筛
	SubState string
}

// UserRow 是后台用户列表的一行（只含 identity 自己的字段）。
type UserRow struct {
	ID          uuid.UUID
	Email       string
	DisplayName *string
	Status      string
	RiskLevel   string
	GroupID     *uuid.UUID
	GroupName   string
	CreatedAt   time.Time
	LastLoginAt *time.Time

	// 下面经端口由别的模块补（admin_views.go）
	// SubscriptionCount 是名下订阅份数（不分状态）
	SubscriptionCount int
	// ActivePlan 是当前订阅生效时的套餐名，否则 nil
	ActivePlan *string
	// Balance 是站点币种下的可用余额，Currency 是站点币种
	Balance  decimal.Decimal
	Currency string
	// Current 是当前订阅，没有订阅为 nil
	Current *AdminSub
}

// UserRef 是一个用户的 id 与邮箱。
type UserRef struct {
	ID    uuid.UUID
	Email string
}

// UserDetail 是后台用户详情里 identity 负责的部分。
type UserDetail struct {
	UserRow
	EmailVerified bool
	// IsAdmin 是否在管理员名单里（后台不分角色，只有是或不是）
	IsAdmin       bool
	Referrer      *UserRef
	ReferralCount int64

	// 下面经端口由别的模块补（admin_views.go）
	// Subscriptions 按创建时间倒序；永不为 nil
	Subscriptions []AdminSub
	// RecentOrders 是最近 20 单；永不为 nil
	RecentOrders []AdminOrderRow
	// PaidTotals 是按币种的实付（与 OpsOrders.UserOrderStats 同口径），币种升序、只列大于 0 的；永不为 nil
	PaidTotals []CurrencyAmount
	OrderCount int64
	Telegram   *TelegramRef
	// UnattachedPackBytes 是还没加到任何一份的流量包余量
	UnattachedPackBytes int64
}

// Group 是后台用户组列表的一行。
type Group struct {
	ID          uuid.UUID
	Code        string
	Name        string
	Description string
	Users       int64
	// 下面经 OpsGroupUsage 补：引用该组的套餐、价格、券数，与把它列入限定名单的节点池（永不为 nil）
	Plans          int64
	Prices         int64
	Coupons        int64
	ExclusivePools []PoolRef
}
