package repo

// 后台节点列表的读模型。

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// NodeListFilter 是后台节点列表的筛选（service 已校验）。
type NodeListFilter struct {
	IncludeRetired bool
	State          string // ''/all/online/offline/disabled/retired
	Search         string
	ID             *uuid.UUID
	Tag            string
	Limit          int
	Offset         int
	Detail         bool // true 时读 protocol_config，否则该列给 NULL
}

// NodeListRow 是后台节点列表的一行读模型：节点本身加所在服务器、节点池的展示字段。
type NodeListRow struct {
	ID                    uuid.UUID       `gorm:"column:id"`
	NodeNo                int64           `gorm:"column:node_no"`
	RowVersion            int64           `gorm:"column:row_version"`
	Name                  string          `gorm:"column:name"`
	ServingStatus         string          `gorm:"column:serving_status"`
	ServerID              *uuid.UUID      `gorm:"column:server_id"`
	ServerName            *string         `gorm:"column:server_name"`
	ServerStatus          *string         `gorm:"column:server_status"`
	ServerLastHeartbeatAt *time.Time      `gorm:"column:server_last_heartbeat_at"`
	PoolID                *uuid.UUID      `gorm:"column:pool_id"`
	PoolName              *string         `gorm:"column:pool_name"`
	PoolStatus            *string         `gorm:"column:pool_status"`
	AgentVersion          *string         `gorm:"column:agent_version"`
	Hostname              *string         `gorm:"column:hostname"`
	PublicIPv4            *string         `gorm:"column:public_ipv4"`
	CPUCores              *int            `gorm:"column:cpu_cores"`
	MemoryMB              *int            `gorm:"column:memory_mb"`
	DiskGB                *int            `gorm:"column:disk_gb"`
	NodeType              *string         `gorm:"column:node_type"`
	ServerHost            *string         `gorm:"column:server_host"`
	ServerPort            *int            `gorm:"column:server_port"`
	DisplayName           *string         `gorm:"column:display_name"`
	CountryCode           *string         `gorm:"column:country_code"`
	SortOrder             int             `gorm:"column:sort_order"`
	CreatedAt             time.Time       `gorm:"column:created_at"`
	RuntimeStatus         *string         `gorm:"column:runtime_status"`
	RuntimeReason         *string         `gorm:"column:runtime_reason"`
	RuntimeStateAt        *time.Time      `gorm:"column:runtime_state_at"`
	RuntimeReasonNode     *string         `gorm:"column:runtime_reason_node"`
	AppliedConfigSHA256   *string         `gorm:"column:applied_config_sha256"`
	ApplyFailedSHA256     *string         `gorm:"column:apply_failed_sha256"`
	ApplyFailedReason     *string         `gorm:"column:apply_failed_reason"`
	ApplyFailedDetail     *string         `gorm:"column:apply_failed_detail"`
	ApplyFailedAt         *time.Time      `gorm:"column:apply_failed_at"`
	TrafficRate           decimal.Decimal `gorm:"column:traffic_rate"`
	ProtocolConfig        *string         `gorm:"column:protocol_config"`
	ProtocolSchemaVersion int             `gorm:"column:protocol_schema_version"`
	ConfigValidatedAt     *time.Time      `gorm:"column:config_validated_at"`
	ServerReady           bool            `gorm:"column:server_ready"`
	Deliverable           bool            `gorm:"column:deliverable"`
	Tags                  string          `gorm:"column:tags"`
	TrafficLimitBytes     *int64          `gorm:"column:traffic_limit_bytes"`
	TrafficResetDay       *int            `gorm:"column:traffic_reset_day"`
	TrafficUsedBytes      int64           `gorm:"column:traffic_used_bytes"`
	TrafficResetAt        time.Time       `gorm:"column:traffic_reset_at"`
	TrafficExceededAt     *time.Time      `gorm:"column:traffic_exceeded_at"`
	RateSchedule          string          `gorm:"column:rate_schedule"`
	RelayCount            int             `gorm:"column:relay_count"`
	ECHEnabled            bool            `gorm:"column:ech_enabled"`
	Total                 int64           `gorm:"column:total"`
}

// nodeListFilterSQL 按筛选拼 WHERE（节点别名 n、服务器别名 s），只拼用到的条件。
func nodeListFilterSQL(f NodeListFilter) (string, []any) {
	conds := []string{"n.deleted_at IS NULL"}
	args := []any{}
	if !f.IncludeRetired {
		conds = append(conds, "n.serving_status <> 'retired'")
	}
	switch f.State {
	case "all":
		conds = append(conds, "n.serving_status <> 'retired'")
	case "online":
		conds = append(conds, "n.serving_status = 'active' AND s.last_heartbeat_at >= now() - interval '90 seconds'")
	case "offline":
		conds = append(conds, "n.serving_status = 'active' AND (s.last_heartbeat_at IS NULL OR s.last_heartbeat_at < now() - interval '90 seconds')")
	case "disabled":
		conds = append(conds, "n.serving_status IN ('draft', 'disabled')")
	case "retired":
		conds = append(conds, "n.serving_status = 'retired'")
	}
	if f.Search != "" {
		conds = append(conds, `strpos(lower(concat_ws(' ', n.name, n.display_name, s.name, n.country_code, n.node_type, n.server_host, n.node_no::text)), lower(?::text)) > 0`)
		args = append(args, f.Search)
	}
	if f.ID != nil {
		conds = append(conds, "n.id = ?")
		args = append(args, *f.ID)
	}
	if f.Tag != "" {
		conds = append(conds, "n.tags @> jsonb_build_array(?::text)")
		args = append(args, f.Tag)
	}
	return strings.Join(conds, " AND "), args
}

// nodeListCountRow 是另数总数时的一行。
type nodeListCountRow struct {
	N int64 `gorm:"column:n"`
}

// ListNodePage 取后台节点列表的一页与筛选后的总数：先在 nodes 上取一页 id，再只对这一页拼读模型。
func (r *Repo) ListNodePage(ctx context.Context, f NodeListFilter) ([]NodeListRow, int64, error) {
	where, args := nodeListFilterSQL(f)
	protoCol := "NULL::text"
	if f.Detail {
		protoCol = "n.protocol_config::text"
	}
	q := `WITH page AS MATERIALIZED (
  SELECT n.id, n.sort_order, n.node_no, count(*) OVER () AS total
    FROM nodes n LEFT JOIN servers s ON s.id = n.server_id
   WHERE ` + where + `
   ORDER BY n.sort_order, n.node_no, n.id
   LIMIT ? OFFSET ?)
SELECT n.id, n.node_no, n.row_version, n.name, n.serving_status, n.server_id,
       s.name AS server_name, s.status AS server_status, s.last_heartbeat_at AS server_last_heartbeat_at,
       n.pool_id, p.name AS pool_name, p.status AS pool_status,
       s.agent_version, s.hostname, host(s.public_ipv4) AS public_ipv4, s.cpu_cores, s.memory_mb, s.disk_gb,
       n.node_type, n.server_host, n.server_port, n.display_name, n.country_code, n.sort_order, n.created_at,
       n.runtime_status, n.runtime_reason, n.runtime_state_at, ro.name AS runtime_reason_node,
       n.applied_config_sha256, n.apply_failed_sha256, n.apply_failed_reason, n.apply_failed_detail, n.apply_failed_at,
       n.traffic_rate, ` + protoCol + ` AS protocol_config, n.protocol_schema_version, n.config_validated_at,
       COALESCE(s.status = 'ready' AND s.deleted_at IS NULL, false) AS server_ready,
       COALESCE((` + DeliverableNodeCond + `), false) AS deliverable,
       n.tags::text AS tags, n.traffic_limit_bytes, n.traffic_reset_day, n.traffic_used_bytes, n.traffic_reset_at,
       n.traffic_exceeded_at, n.traffic_rate_schedule::text AS rate_schedule,
       (SELECT count(*) FROM node_relays r WHERE r.parent_node_id = n.id)::int AS relay_count,
       EXISTS (SELECT 1 FROM node_ech_keys k WHERE k.node_id = n.id AND k.superseded_at IS NULL) AS ech_enabled,
       pg.total
  FROM page pg JOIN nodes n ON n.id = pg.id
  LEFT JOIN servers s ON s.id = n.server_id
  LEFT JOIN node_pools p ON p.id = n.pool_id
  LEFT JOIN nodes ro ON ro.id = n.runtime_reason_node_id
 ORDER BY pg.sort_order, pg.node_no, pg.id`
	pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	var rows []NodeListRow
	if err := r.d.Conn(ctx).Raw(q, pageArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	if len(rows) > 0 {
		return rows, rows[0].Total, nil
	}
	rows = []NodeListRow{}
	if f.Offset <= 0 {
		return rows, 0, nil
	}
	// 页越界：按同样的筛选另数一次总数，前端据此回到有效页
	var c nodeListCountRow
	err := r.d.Conn(ctx).Raw(
		"SELECT count(*) AS n FROM nodes n LEFT JOIN servers s ON s.id = n.server_id WHERE "+where, args...,
	).Scan(&c).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, c.N, nil
}
