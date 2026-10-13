// Package model 是 node 模块的 GORM 结构体；池相关表见迁移 00018_node.sql。
package model

import (
	"time"

	"github.com/google/uuid"
)

// NodePool 是 node_pools 表的一行。
type NodePool struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Code      string    `gorm:"column:code"`
	Name      string    `gorm:"column:name"`
	Region    string    `gorm:"column:region"`
	Status    string    `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;default:now()"`
}

// TableName 固定表名。
func (NodePool) TableName() string { return "node_pools" }

// NodePoolUserGroup 是 node_pool_user_groups 表的一行（池「仅限用户组」名单）。
type NodePoolUserGroup struct {
	PoolID      uuid.UUID `gorm:"column:pool_id;type:uuid;primaryKey"`
	UserGroupID uuid.UUID `gorm:"column:user_group_id;type:uuid;primaryKey"`
	CreatedAt   time.Time `gorm:"column:created_at;default:now()"`
}

// TableName 固定表名。
func (NodePoolUserGroup) TableName() string { return "node_pool_user_groups" }
