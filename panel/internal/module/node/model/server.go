package model

import (
	"time"

	"github.com/google/uuid"
)

// Server 是 servers 表的一行（迁移 00018_node.sql）。
//
// inet 列（public_ipv4 等）在 Go 里是纯地址字符串：repo 读时用 host() 取，写时 CAST 成 inet。
type Server struct {
	ID            uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Name          string    `gorm:"column:name"`
	Status        string    `gorm:"column:status"`
	StatusReason  *string   `gorm:"column:status_reason"`
	RowVersion    int64     `gorm:"column:row_version;default:1"`
	Region        *string   `gorm:"column:region"`
	PublicIPv4    *string   `gorm:"column:public_ipv4;type:inet"`
	PublicIPv6    *string   `gorm:"column:public_ipv6;type:inet"`
	PrivateIPv4   *string   `gorm:"column:private_ipv4;type:inet"`
	CapacityNodes int       `gorm:"column:capacity_nodes"`
	Notes         *string   `gorm:"column:notes"`
	// 以下由节点通道写（节点自报）；后台建、改服务器时也可录入 hostname、os_name、architecture
	Hostname        *string    `gorm:"column:hostname"`
	OSName          *string    `gorm:"column:os_name"`
	Architecture    *string    `gorm:"column:architecture"`
	AgentVersion    *string    `gorm:"column:agent_version"`
	BinarySHA256    *string    `gorm:"column:binary_sha256"`
	CPUCores        *int       `gorm:"column:cpu_cores"`
	MemoryMB        *int       `gorm:"column:memory_mb"`
	DiskGB          *int       `gorm:"column:disk_gb"`
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at"`
	RetiredAt       *time.Time `gorm:"column:retired_at"`
	DeletedAt       *time.Time `gorm:"column:deleted_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Server) TableName() string { return "servers" }
