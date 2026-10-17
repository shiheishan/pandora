package model

import (
	"time"

	"github.com/google/uuid"
)

// DomainGroup 是 subscription_domain_groups 表的一行。
type DomainGroup struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Name      string    `gorm:"column:name"`
	IsDefault bool      `gorm:"column:is_default"`
	Position  int       `gorm:"column:position"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (DomainGroup) TableName() string { return "subscription_domain_groups" }

// Domain 是 subscription_domains 表的一行。
type Domain struct {
	GroupID  uuid.UUID `gorm:"column:group_id;type:uuid;primaryKey"`
	BaseURL  string    `gorm:"column:base_url;primaryKey"`
	Weight   int       `gorm:"column:weight"`
	Position int       `gorm:"column:position"`
}

// TableName 固定表名。
func (Domain) TableName() string { return "subscription_domains" }

// DomainGroupUserGroup 是 subscription_domain_group_user_groups 表的一行。
type DomainGroupUserGroup struct {
	UserGroupID uuid.UUID `gorm:"column:user_group_id;type:uuid;primaryKey"`
	GroupID     uuid.UUID `gorm:"column:group_id;type:uuid"`
}

// TableName 固定表名。
func (DomainGroupUserGroup) TableName() string { return "subscription_domain_group_user_groups" }
