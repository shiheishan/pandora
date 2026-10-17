package model

import (
	"time"

	"github.com/google/uuid"
)

// SubscriptionTrial 是 subscription_trials 表的一行（只追加）。
type SubscriptionTrial struct {
	UserID         uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id;type:uuid"`
	PlanID         uuid.UUID `gorm:"column:plan_id;type:uuid"`
	Hours          int       `gorm:"column:hours"`
	GrantedAt      time.Time `gorm:"column:granted_at;autoCreateTime"`
}

// TableName 固定表名。
func (SubscriptionTrial) TableName() string { return "subscription_trials" }
