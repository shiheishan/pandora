package model

import (
	"time"

	"github.com/google/uuid"
)

// NodeRelay 是 node_relays 表的一行（迁移 00045_xb_node.sql）：中转节点，只出现在订阅里，
// 协议配置、倍率、标签、ECH、池与资格都跟父节点走，只换地址与端口（以及可选的 SNI / Host 覆盖）。
type NodeRelay struct {
	ID           uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ParentNodeID uuid.UUID `gorm:"column:parent_node_id;type:uuid"`
	Name         string    `gorm:"column:name"`
	ServerHost   string    `gorm:"column:server_host"`
	ServerPort   int       `gorm:"column:server_port"`
	SNI          *string   `gorm:"column:sni"`
	HostHeader   *string   `gorm:"column:host_header"`
	Enabled      bool      `gorm:"column:enabled"`
	SortOrder    int       `gorm:"column:sort_order"`
	RowVersion   int64     `gorm:"column:row_version;default:1"`
	CreatedAt    time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (NodeRelay) TableName() string { return "node_relays" }
