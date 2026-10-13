package model

import (
	"time"

	"github.com/google/uuid"
)

// 出站、分流规则与路由组（迁移 00021_node_routing.sql）。jsonb 列用 string 承载 JSON 文本，由 service 编解码。

// NodeOutbound 是 node_outbounds 表的一行。NodeID、GroupID 都为 nil 是全局出站。
type NodeOutbound struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	NodeID    *uuid.UUID `gorm:"column:node_id;type:uuid"`
	GroupID   *uuid.UUID `gorm:"column:group_id;type:uuid"`
	Tag       string     `gorm:"column:tag"`
	Type      string     `gorm:"column:type"`
	Settings  string     `gorm:"column:settings;type:jsonb"`
	SortOrder int        `gorm:"column:sort_order"`
	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (NodeOutbound) TableName() string { return "node_outbounds" }

// NodeRoute 是 node_routes 表的一行。NodeID、GroupID 都为 nil 是全局规则。
type NodeRoute struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	NodeID      *uuid.UUID `gorm:"column:node_id;type:uuid"`
	GroupID     *uuid.UUID `gorm:"column:group_id;type:uuid"`
	Priority    int        `gorm:"column:priority"`
	SortOrder   int        `gorm:"column:sort_order"`
	Matcher     string     `gorm:"column:matcher;type:jsonb"`
	OutboundTag string     `gorm:"column:outbound_tag"`
	Enabled     bool       `gorm:"column:enabled"`
	Note        string     `gorm:"column:note"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (NodeRoute) TableName() string { return "node_routes" }

// RouteGroup 是 route_groups 表的一行。
type RouteGroup struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Name        string    `gorm:"column:name"`
	Description string    `gorm:"column:description"`
	SortOrder   int       `gorm:"column:sort_order"`
	RowVersion  int64     `gorm:"column:row_version;default:1"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (RouteGroup) TableName() string { return "route_groups" }

// RouteGroupMember 是 route_group_members 表的一行。
type RouteGroupMember struct {
	GroupID   uuid.UUID `gorm:"column:group_id;type:uuid;primaryKey"`
	NodeID    uuid.UUID `gorm:"column:node_id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (RouteGroupMember) TableName() string { return "route_group_members" }
