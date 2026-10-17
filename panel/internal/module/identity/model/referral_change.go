package model

import (
	"time"

	"github.com/google/uuid"
)

// ReferralChange 是 referral_changes 表的一行（只追加）：后台改邀请人的记录。
type ReferralChange struct {
	ID                     uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	RefereeUserID          uuid.UUID  `gorm:"column:referee_user_id;type:uuid"`
	ReferrerUserID         *uuid.UUID `gorm:"column:referrer_user_id;type:uuid"`
	PreviousReferrerUserID *uuid.UUID `gorm:"column:previous_referrer_user_id;type:uuid"`
	ChangedBy              uuid.UUID  `gorm:"column:changed_by;type:uuid"`
	Reason                 string     `gorm:"column:reason"`
	CreatedAt              time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (ReferralChange) TableName() string { return "referral_changes" }
