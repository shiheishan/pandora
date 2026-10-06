package nodefabric

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// AdminNodeListRow 是后台节点列表的一行。json 标签就是响应里的键，handler 嵌入它再补下发状态。
type AdminNodeListRow struct {
	ID string `json:"id"`
	// NodeNo 是给人看的短编号（101、102…）。UUID 仍然是主键和接口
	// 参数，但后台列表第一列、工单和群里说的都是这个号——
	// 「103 挂了」比念一遍 UUID 快得多，也不会念错。
	NodeNo        int        `json:"node_no"`
	RowVersion    int64      `json:"row_version"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	ServingStatus string     `json:"serving_status"`
	ServerID      *string    `json:"server_id"`
	ServerName    *string    `json:"server_name"`
	PoolID        *string    `json:"pool_id"`
	PoolName      *string    `json:"pool_name"`
	AgentVer      *string    `json:"agent_version"`
	Hostname      *string    `json:"hostname"`
	PublicIP      *string    `json:"public_ipv4"`
	CPUCores      *int       `json:"cpu_cores"`
	MemoryMB      *int       `json:"memory_mb"`
	DiskGB        *int       `json:"disk_gb"`
	HealthScore   *int       `json:"health_score"`
	AppliedVer    *int       `json:"applied_config_version"`
	DesiredVer    *int       `json:"desired_config_version"`
	LastBeat      *time.Time `json:"last_heartbeat_at"`
	// Stale 由数据库算：心跳超过 90 秒即视为失联（AGT-004 心跳超时停止新分配）
	Stale     bool      `json:"stale"`
	Serial    *int      `json:"identity_serial"`
	CreatedAt time.Time `json:"created_at"`

	// 以下是对外服务配置。表单保存时是全量覆盖，
	// 所以这里必须原样带回，缺一个字段就会在保存时被清掉。
	NodeType              *string         `json:"node_type"`
	ServerHost            *string         `json:"server_host"`
	ServerPort            *int            `json:"server_port"`
	TrafficRate           float64         `json:"traffic_rate"`
	DisplayName           *string         `json:"display_name"`
	CountryCode           *string         `json:"country_code"` // 只进管理端（保留规则 3）
	Kernel                string          `json:"kernel"`
	Protocol              json.RawMessage `json:"protocol_config"`
	ProtocolSchemaVersion int             `json:"protocol_schema_version"`
	ConfigValidatedAt     *time.Time      `json:"config_validated_at"`
	SortOrder             int             `json:"sort_order"`

	// 运营视角的三项。都是聚合出来的，不是 nodes 表上的列。
	//
	// OnlineUsers 是在线订阅数，OnlineIPs 是在线 IP 数：后者明显大于
	// 前者就说明有人在共享账号，这是两个不能合并的指标。
	OnlineUsers int `json:"online_users"`
	OnlineIPs   int `json:"online_ips"`
	// TrafficBytes24h 是近 24 小时上下行合计（原始量），设计表格的「24h 流量」列
	TrafficBytes24h int64 `json:"traffic_bytes_24h"`
	// 所在服务器控制节点最近一条探针（与服务器列表的 cpu_bp 同源）；无探针为 null
	CPUPercent *float64   `json:"cpu_percent"`
	MemPercent *float64   `json:"mem_percent"`
	MetricsAt  *time.Time `json:"metrics_at"`
	// TrafficBytes 是近 30 天上下行合计（原始量，未乘倍率）。
	// 不做全量累计：node_traffic_reports 每节点每分钟一条，
	// 全表 SUM 会随运行时长线性变慢，而列表页每次打开都要算。
	TrafficBytes int64 `json:"traffic_bytes"`
	// GrantedPlans 是通过所属分组授权到这个节点的套餐。
	// 对应 xboard 的「权限组」——回答「谁能用上这个节点」。
	GrantedPlans []string `json:"granted_plans"`
}

// ListAdminNodes 读后台节点列表的一页与筛选后的真实总数（缺陷 21）。
// 返回的切片非 nil：空页编成 []。
func (s *Service) ListAdminNodes(ctx context.Context, tenantID string, includeRetired bool, limit, offset int) ([]AdminNodeListRow, int64, error) {
	out := []AdminNodeListRow{}
	var total int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID},
		func(tx pgx.Tx) error {
			// total 用与列表相同的筛选单独数一次：翻过最后一页时列表为空，
			// 窗口函数就取不到总数了
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM nodes n
				 WHERE n.tenant_id = $1 AND n.status <> 'destroyed'
				   AND ($2::boolean OR n.serving_status <> 'retired')`,
				tenantID, includeRetired).Scan(&total); err != nil {
				return err
			}
			// 在线数与流量先各自聚合成一张小表再 JOIN，而不是给每个节点
			// 挂相关子查询：后者会把同一个时间窗扫 200 遍。
			rows, err := tx.Query(ctx, `
				WITH alive AS (
				  SELECT node_id,
				         count(DISTINCT subscription_id)::int AS users,
				         count(*)::int AS ips
				    FROM node_alive_ips
				   -- 与在线设备视图同一个窗口（按租户设置，R103），不另写字面量
				   WHERE tenant_id = $1
				     AND last_seen_at > now() - make_interval(mins => app.device_limit_window_minutes($1))
				   GROUP BY node_id
				), traffic AS (
				  SELECT node_id, sum(total_upload + total_download)::bigint AS bytes,
				         coalesce(sum(total_upload + total_download)
				           FILTER (WHERE received_at > now() - interval '24 hours'), 0)::bigint AS bytes_24h
				    FROM node_traffic_reports
				   WHERE tenant_id = $1 AND duplicate_of IS NULL
				     AND received_at > now() - interval '30 days'
				   GROUP BY node_id
				), grants AS (
				  SELECT pnp.pool_id, array_agg(DISTINCT pl.name) AS plans
				    FROM plan_node_pools pnp
				    JOIN plan_versions pv ON pv.tenant_id = pnp.tenant_id
				                         AND pv.id = pnp.plan_version_id
				    JOIN plans pl ON pl.tenant_id = pv.tenant_id AND pl.id = pv.plan_id
				   WHERE pnp.tenant_id = $1
				   GROUP BY pnp.pool_id
				)
				SELECT n.id, n.row_version, n.name, n.status, n.serving_status,
				       n.server_id, s.name, n.pool_id, p.name, n.agent_version, n.hostname,
				       host(n.public_ipv4), n.cpu_cores, n.memory_mb, n.disk_gb,
				       n.health_score, n.applied_config_version, n.desired_config_version,
				       n.last_heartbeat_at,
				       (n.last_heartbeat_at IS NULL
				        OR n.last_heartbeat_at < now() - interval '90 seconds') AS stale,
				       i.serial, n.created_at,
				       n.node_type, n.server_host, n.server_port,
				       n.traffic_rate, n.display_name, n.country_code,
				       coalesce(n.kernel,'auto'), n.protocol_config,
				       n.protocol_schema_version,n.config_validated_at,n.sort_order,n.node_no,
				       coalesce(a.users,0), coalesce(a.ips,0), coalesce(t.bytes,0),
				       coalesce(g.plans, '{}'), coalesce(t.bytes_24h,0),
				       m.cpu_bp / 100.0,
				       CASE WHEN m.mem_total_mb > 0 THEN round(m.mem_used_mb * 100.0 / m.mem_total_mb, 1) END,
				       m.recorded_at
				  FROM nodes n
				  LEFT JOIN node_pools p ON p.id = n.pool_id
				  LEFT JOIN servers s ON s.id=n.server_id AND s.tenant_id=n.tenant_id
				  LEFT JOIN node_identities i ON i.node_id = n.id AND i.status = 'active'
				  LEFT JOIN alive a ON a.node_id = n.id
				  LEFT JOIN traffic t ON t.node_id = n.id
				  LEFT JOIN grants g ON g.pool_id = n.pool_id
				  LEFT JOIN LATERAL (
				        SELECT nm.cpu_bp, nm.mem_used_mb, nm.mem_total_mb, nm.recorded_at
				          FROM node_metrics nm
				         WHERE nm.tenant_id = s.tenant_id AND nm.node_id = s.control_node_id
				         ORDER BY nm.recorded_at DESC LIMIT 1) m ON true
				 WHERE n.tenant_id = $1 AND n.status <> 'destroyed'
				   AND ($2::boolean OR n.serving_status <> 'retired')
				 -- 与「调整排序」同一个顺序（契约后台-07）
				 ORDER BY n.sort_order, n.node_no, n.id
				 LIMIT $3 OFFSET $4`,
				tenantID, includeRetired, limit, offset)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var x AdminNodeListRow
				if err := rows.Scan(&x.ID, &x.RowVersion, &x.Name, &x.Status, &x.ServingStatus,
					&x.ServerID, &x.ServerName, &x.PoolID, &x.PoolName, &x.AgentVer,
					&x.Hostname, &x.PublicIP, &x.CPUCores, &x.MemoryMB, &x.DiskGB,
					&x.HealthScore, &x.AppliedVer, &x.DesiredVer, &x.LastBeat,
					&x.Stale, &x.Serial, &x.CreatedAt,
					&x.NodeType, &x.ServerHost, &x.ServerPort,
					&x.TrafficRate, &x.DisplayName, &x.CountryCode, &x.Kernel, &x.Protocol,
					&x.ProtocolSchemaVersion, &x.ConfigValidatedAt, &x.SortOrder, &x.NodeNo,
					&x.OnlineUsers, &x.OnlineIPs, &x.TrafficBytes,
					&x.GrantedPlans, &x.TrafficBytes24h, &x.CPUPercent, &x.MemPercent, &x.MetricsAt); err != nil {
					return err
				}
				out = append(out, x)
			}
			return rows.Err()
		})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
