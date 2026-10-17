package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Node 是 nodes 表的一行（迁移 00018_node.sql）。
//
// protocol_config 是 jsonb，用 string 承载 JSON 文本，由 service 编解码。
type Node struct {
	ID                    uuid.UUID       `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	NodeNo                int64           `gorm:"column:node_no;->"`
	ServerID              *uuid.UUID      `gorm:"column:server_id;type:uuid"`
	PoolID                *uuid.UUID      `gorm:"column:pool_id;type:uuid"`
	Name                  string          `gorm:"column:name"`
	DisplayName           *string         `gorm:"column:display_name"`
	CountryCode           *string         `gorm:"column:country_code"`
	ServingStatus         string          `gorm:"column:serving_status"`
	NodeType              *string         `gorm:"column:node_type"`
	ServerHost            *string         `gorm:"column:server_host"`
	ServerPort            *int            `gorm:"column:server_port"`
	ListenL4              *string         `gorm:"column:listen_l4"`
	ProtocolConfig        string          `gorm:"column:protocol_config;type:jsonb"`
	ProtocolSchemaVersion int             `gorm:"column:protocol_schema_version"`
	ConfigValidatedAt     *time.Time      `gorm:"column:config_validated_at"`
	TrafficRate           decimal.Decimal `gorm:"column:traffic_rate;type:numeric(6,2)"`
	SortOrder             int             `gorm:"column:sort_order"`
	RowVersion            int64           `gorm:"column:row_version;default:1"`
	// 以下是 Xboard 补齐（迁移 00045）：标签、每周期流量上限与用量、时段倍率。jsonb 用 string 承载 JSON 文本
	Tags                string     `gorm:"column:tags;type:jsonb"`
	TrafficLimitBytes   *int64     `gorm:"column:traffic_limit_bytes"`
	TrafficResetDay     *int       `gorm:"column:traffic_reset_day"`
	TrafficUsedBytes    int64      `gorm:"column:traffic_used_bytes"`
	TrafficResetAt      time.Time  `gorm:"column:traffic_reset_at"`
	TrafficExceededAt   *time.Time `gorm:"column:traffic_exceeded_at"`
	TrafficRateSchedule string     `gorm:"column:traffic_rate_schedule;type:jsonb"`
	// 以下由节点通道写：本机运行状态与配置回执
	RuntimeStatus       *string    `gorm:"column:runtime_status"`
	RuntimeReason       *string    `gorm:"column:runtime_reason"`
	RuntimeReasonNodeID *uuid.UUID `gorm:"column:runtime_reason_node_id;type:uuid"`
	RuntimeStateAt      *time.Time `gorm:"column:runtime_state_at"`
	AppliedConfigSHA256 *string    `gorm:"column:applied_config_sha256"`
	AppliedConfigAt     *time.Time `gorm:"column:applied_config_at"`
	ApplyFailedSHA256   *string    `gorm:"column:apply_failed_sha256"`
	ApplyFailedReason   *string    `gorm:"column:apply_failed_reason"`
	ApplyFailedDetail   *string    `gorm:"column:apply_failed_detail"`
	ApplyFailedAt       *time.Time `gorm:"column:apply_failed_at"`
	RetiredAt           *time.Time `gorm:"column:retired_at"`
	DeletedAt           *time.Time `gorm:"column:deleted_at"`
	CreatedAt           time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Node) TableName() string { return "nodes" }
