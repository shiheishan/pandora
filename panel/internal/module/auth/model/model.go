// Package model 是 auth 模块的 GORM 结构体：权限字典、角色、角色权限与用户角色绑定。
package model

import (
	"time"

	"github.com/google/uuid"
)

// Permission 是 permissions 表的一行（权限字典，由迁移种子维护）。
type Permission struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code        string    `gorm:"column:code"`
	Domain      string    `gorm:"column:domain"`
	Description string    `gorm:"column:description"`
	HighRisk    bool      `gorm:"column:high_risk"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Permission) TableName() string { return "permissions" }

// Role 是 roles 表的一行。
type Role struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code        string    `gorm:"column:code"`
	Name        string    `gorm:"column:name"`
	Description *string   `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (Role) TableName() string { return "roles" }

// RolePermission 是 role_permissions 表的一行。
type RolePermission struct {
	ID             uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	RoleID         uuid.UUID `gorm:"column:role_id;type:uuid"`
	PermissionCode string    `gorm:"column:permission_code"`
}

// TableName 固定表名。
func (RolePermission) TableName() string { return "role_permissions" }

// UserRole 是 user_roles 表的一行；ExpiresAt 为空是长期绑定。
type UserRole struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid"`
	RoleID    uuid.UUID  `gorm:"column:role_id;type:uuid"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
	GrantedBy *uuid.UUID `gorm:"column:granted_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (UserRole) TableName() string { return "user_roles" }
