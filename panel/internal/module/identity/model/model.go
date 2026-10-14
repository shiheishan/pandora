// Package model 是 identity 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00005_identity.sql。
package model

import (
	"time"

	"github.com/google/uuid"
)

// User 是 users 表的一行。
type User struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Email           string     `gorm:"column:email;type:citext"`
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at"`
	DisplayName     *string    `gorm:"column:display_name"`
	Status          string     `gorm:"column:status"`
	UserGroupID     *uuid.UUID `gorm:"column:user_group_id;type:uuid"`
	RiskLevel       string     `gorm:"column:risk_level;default:normal"`
	LastLoginAt     *time.Time `gorm:"column:last_login_at"`
	AdminNote       *string    `gorm:"column:admin_note"`
	DeletedAt       *time.Time `gorm:"column:deleted_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (User) TableName() string { return "users" }

// UserGroup 是 user_groups 表的一行。
type UserGroup struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code        string    `gorm:"column:code"`
	Name        string    `gorm:"column:name"`
	Description *string   `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (UserGroup) TableName() string { return "user_groups" }

// RegistrationSession 是 registration_sessions 表的一行。
type RegistrationSession struct {
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	TokenHash    []byte     `gorm:"column:token_hash"`
	Email        string     `gorm:"column:email;type:citext"`
	InviteCodeID *uuid.UUID `gorm:"column:invite_code_id;type:uuid"`
	IPHash       []byte     `gorm:"column:ip_hash"`
	UserAgent    *string    `gorm:"column:user_agent"`
	Status       string     `gorm:"column:status;default:pending"`
	Attempts     int16      `gorm:"column:attempts;default:0"`
	ExpiresAt    time.Time  `gorm:"column:expires_at"`
	ConsumedAt   *time.Time `gorm:"column:consumed_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (RegistrationSession) TableName() string { return "registration_sessions" }

// VerificationCode 是 verification_codes 表的一行。
type VerificationCode struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Purpose     string     `gorm:"column:purpose"`
	TargetHash  []byte     `gorm:"column:target_hash"`
	CodeHash    []byte     `gorm:"column:code_hash"`
	UserID      *uuid.UUID `gorm:"column:user_id;type:uuid"`
	Attempts    int16      `gorm:"column:attempts;default:0"`
	MaxAttempts int16      `gorm:"column:max_attempts;default:5"`
	ConsumedAt  *time.Time `gorm:"column:consumed_at"`
	ExpiresAt   time.Time  `gorm:"column:expires_at"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (VerificationCode) TableName() string { return "verification_codes" }

// InviteCode 是 invite_codes 表的一行。
type InviteCode struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code        string     `gorm:"column:code;type:citext"`
	OwnerUserID *uuid.UUID `gorm:"column:owner_user_id;type:uuid"`
	MaxUses     *int       `gorm:"column:max_uses"`
	UsedCount   int        `gorm:"column:used_count;default:0"`
	Channel     *string    `gorm:"column:channel"`
	Campaign    *string    `gorm:"column:campaign"`
	ExpiresAt   *time.Time `gorm:"column:expires_at"`
	Status      string     `gorm:"column:status;default:active"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (InviteCode) TableName() string { return "invite_codes" }

// Referral 是 referrals 表的一行（只追加）。
type Referral struct {
	RefereeUserID  uuid.UUID  `gorm:"column:referee_user_id;type:uuid;primaryKey"`
	ReferrerUserID uuid.UUID  `gorm:"column:referrer_user_id;type:uuid"`
	InviteCodeID   *uuid.UUID `gorm:"column:invite_code_id;type:uuid"`
	Channel        *string    `gorm:"column:channel"`
	Campaign       *string    `gorm:"column:campaign"`
	RiskFlag       string     `gorm:"column:risk_flag;default:none"`
	BoundAt        time.Time  `gorm:"column:bound_at;autoCreateTime"`
}

// TableName 固定表名。
func (Referral) TableName() string { return "referrals" }
