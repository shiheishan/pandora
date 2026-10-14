// Package model 是 auth 模块的 GORM 结构体：后台管理员名单。
package model

import (
	"time"

	"github.com/google/uuid"
)

// Admin 是 admins 表的一行：在表里（且账号 active）就是管理员，后台不分角色。
type Admin struct {
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;primaryKey"`
	GrantedBy *uuid.UUID `gorm:"column:granted_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Admin) TableName() string { return "admins" }
