package nodefabric

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
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

	// 以下是列表行要显示或搜索的对外服务配置。编辑表单才用的字段在 AdminNodeDetail，
	// 只有按 id 单取（AdminNodeQuery.ID）时才查、才回。
	NodeType    *string `json:"node_type"`
	ServerHost  *string `json:"server_host"`
	ServerPort  *int    `json:"server_port"`
	DisplayName *string `json:"display_name"`
	CountryCode *string `json:"country_code"` // 只进管理端（保留规则 3）
	SortOrder   int     `json:"sort_order"`

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
	// GrantedPlans 是通过所属分组授权到这个节点的套餐。
	// 对应 xboard 的「权限组」——回答「谁能用上这个节点」。
	GrantedPlans []string `json:"granted_plans"`

	// 下发与运行的真实状态（node_runtime_view.go）
	NodeRuntimeView

	// AdminNodeDetail 只在单取时填；列表响应不编出它（json:"-"），由 handler 在单取时另外嵌入
	AdminNodeDetail `json:"-"`
}

// AdminNodeDetail 是只有编辑表单与单节点视图才用的字段。
//
// 表单保存时 protocol_config 是全量覆盖，所以单取时必须原样带回（读出时由 handler 抹掉敏感键）。
// 列表里不带：一个节点的 protocol_config 动辄几百字节到上 KB，1000 个节点的列表每次
// 刷新都要多编、多传、多解析一遍，而列表上一个字也不显示它。
type AdminNodeDetail struct {
	TrafficRate           float64         `json:"traffic_rate"`
	Kernel                string          `json:"kernel"`
	Protocol              json.RawMessage `json:"protocol_config"`
	ProtocolSchemaVersion int             `json:"protocol_schema_version"`
	ConfigValidatedAt     *time.Time      `json:"config_validated_at"`
	// TrafficBytes 是近 30 天上下行合计（原始量，未乘倍率）。
	// 不做全量累计：node_traffic_reports 每节点每分钟一条，全表 SUM 会随运行时长线性变慢。
	// 列表只要 24 小时那一列，30 天的只在单取时算。
	TrafficBytes int64 `json:"traffic_bytes"`
	// Warnings 是存量协议配置不满足现行规则的提示（ProtocolConfigWarnings，如证书路径不在约定目录）；
	// 只在单取时算，没有提示时不出现
	Warnings []string `json:"warnings,omitempty"`
}

// AdminNodeQuery 是后台节点列表的筛选与分页。
type AdminNodeQuery struct {
	// IncludeRetired 只在 State 为空时生效：State 一旦给出，就由它决定含不含已退役
	IncludeRetired bool
	// State 是后台列表的状态筛选（与前端 logic.ts 的 nodeState 同一映射）：
	// all（不含已退役）/ online / offline / disabled / retired；空串不筛
	State string
	// Search 在名称、展示名、服务器名、国家、协议、地址、编号里按子串找（不分大小写）
	Search string
	// ID 非空时只取这一个节点，并带上 AdminNodeDetail（编辑表单、抽屉单取）
	ID     string
	Limit  int
	Offset int
}

// AdminNodeStates 是 AdminNodeQuery.State 认得的值。
var AdminNodeStates = []string{"all", "online", "offline", "disabled", "retired"}

// adminNodeSearchMax 是搜索词的长度上限（按字符）：节点名不超过 120 字，再长的词不可能命中
const adminNodeSearchMax = 120

// adminNodeFilterSQL 拼列表页与「翻过最后一页」计数共用的筛选（别名 n 是 nodes、s 是 LEFT JOIN
// 的 servers），参数追加进 args（args[0] 已是租户，即 $1）。
//
// 只拼实际用到的条件，不写「$4 为空串或命中」「$5 IS NULL OR n.id = $5」这类万能条件：pgx 缓存语句后
// PostgreSQL 会改用通用计划，万能条件的选择率按缺省值估（几个条件连乘下来只剩约 1 行），
// 下游读模型全被排成嵌套循环——5k-r4 节点列表 p50 从 104ms 回退到 886ms 就是这么来的。
// 每种筛选组合是一条单独的语句文本，各自缓存、各自的通用计划都按真实条件估行数。
// 「离线」与列表的 stale 列同一个口径（心跳超过 NodeStaleAfter 即离线）。
func adminNodeFilterSQL(q AdminNodeQuery, args *[]any) string {
	arg := func(v any) string {
		*args = append(*args, v)
		return "$" + strconv.Itoa(len(*args))
	}
	var b strings.Builder
	b.WriteString("n.tenant_id = $1 AND n.status <> 'destroyed'")
	if !q.IncludeRetired {
		b.WriteString(" AND n.serving_status <> 'retired'")
	}
	switch q.State {
	case "all":
		b.WriteString(" AND n.serving_status <> 'retired'")
	case "online":
		b.WriteString(" AND (n.serving_status = 'draining'" +
			" OR (n.serving_status = 'active' AND n.last_heartbeat_at >= now() - interval '90 seconds'))")
	case "offline":
		b.WriteString(" AND n.serving_status = 'active'" +
			" AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < now() - interval '90 seconds')")
	case "disabled":
		b.WriteString(" AND n.serving_status IN ('draft', 'disabled')")
	case "retired":
		b.WriteString(" AND n.serving_status = 'retired'")
	case "":
	default:
		// 不认得的状态（QueryAdminNodes 已回 422，这里只防未校验的调用方）：什么也不匹配
		b.WriteString(" AND false")
	}
	if q.Search != "" {
		b.WriteString(" AND strpos(lower(concat_ws(' ', n.name, n.display_name, s.name," +
			" n.country_code, n.node_type, n.server_host, n.node_no::text)), lower(" + arg(q.Search) + "::text)) > 0")
	}
	if q.ID != "" {
		b.WriteString(" AND n.id = " + arg(q.ID) + "::uuid")
	}
	return b.String()
}

// validateAdminNodeQuery 校验并归一筛选参数；不认得的状态与过长的搜索词回 422。
func validateAdminNodeQuery(q *AdminNodeQuery) error {
	q.Search = strings.TrimSpace(q.Search)
	q.ID = strings.TrimSpace(q.ID)
	if q.State != "" && !slices.Contains(AdminNodeStates, q.State) {
		return httpx.Invalid(map[string]string{"state": "只能是 all、online、offline、disabled、retired 之一"})
	}
	if utf8.RuneCountInString(q.Search) > adminNodeSearchMax {
		return httpx.Invalid(map[string]string{"q": "搜索词太长"})
	}
	if err := validateAdminUUID("id", q.ID, false); err != nil {
		return err
	}
	if q.State != "" {
		q.IncludeRetired = true
	}
	return nil
}

// ListAdminNodes 读后台节点列表的一页（含编辑字段与 30 天流量），保留给只按分页取全量字段的调用方。
func (s *Service) ListAdminNodes(ctx context.Context, tenantID string, includeRetired bool, limit, offset int) ([]AdminNodeListRow, int64, error) {
	return s.queryAdminNodes(ctx, tenantID, AdminNodeQuery{IncludeRetired: includeRetired, Limit: limit, Offset: offset}, true)
}

// QueryAdminNodes 读后台节点列表的一页与筛选后的真实总数（缺陷 21）。
// 给了 ID 时只取那一个节点，并带上 AdminNodeDetail；否则只查列表要显示的字段。
// 返回的切片非 nil：空页编成 []。
func (s *Service) QueryAdminNodes(ctx context.Context, tenantID string, q AdminNodeQuery) ([]AdminNodeListRow, int64, error) {
	if err := validateAdminNodeQuery(&q); err != nil {
		return nil, 0, err
	}
	return s.queryAdminNodes(ctx, tenantID, q, q.ID != "")
}

// adminNodeListStmt 是一次列表读要发的语句：主查询与「翻过最后一页」时的计数各一条，参数各自一份。
type adminNodeListStmt struct {
	sql, countSQL   string
	args, countArgs []any
}

// adminNodeListSQL 按筛选与是否单取拼出列表语句（PG18 用例在通用计划下对它做 EXPLAIN）。
func adminNodeListSQL(tenantID string, q AdminNodeQuery, detail bool) adminNodeListStmt {
	args := []any{tenantID}
	filter := adminNodeFilterSQL(q, &args)
	countArgs := slices.Clone(args)
	limitArg, offsetArg := "$"+strconv.Itoa(len(args)+1), "$"+strconv.Itoa(len(args)+2)
	args = append(args, q.Limit, q.Offset)
	// 流量窗口与编辑字段按单取 / 列表写成两条语句文本，不靠参数在 CASE 里切换（理由同 adminNodeFilterSQL）
	window, edit := "interval '24 hours'", "false"
	if detail {
		window, edit = "interval '30 days'", "true"
	}

	// 先在 nodes 上按筛选与排序键取一页 id（总数随页用窗口函数带回），再只对这一页
	// 拼读模型：在线数、流量、探针都只聚合本页节点，不再对全租户的节点各算一遍。
	//
	// 每个 CTE 都标 MATERIALIZED，各自只算一次：不标时通用计划会把流量聚合内联进嵌套循环，
	// 对本页每个节点把整个聚合重算一遍（O(n²)，5k-r4 的 2 万个缓冲块、410–509ms）。
	// alive / traffic 按「本页 id 数组」过滤（= ANY(ARRAY(…))）而不是与 page 做半连接：
	// 那是单表扫描上的过滤条件，计划器没法再把它排成「每个节点一次索引查找」。
	//
	// 在线窗口在 win 里只算一次再代入（按租户设置，R103，与在线设备视图同一个窗口）；
	// 流量读小时汇总（00099），列表只算 24 小时，30 天合计只在单取（detail）时算。
	//
	// 只读，经 QueryScoped 一次往返（注入租户与查询同批），不再开 BEGIN / COMMIT。
	sql := `
		WITH page AS MATERIALIZED (
		  SELECT n.id, n.sort_order, n.node_no, count(*) OVER () AS total
		    FROM nodes n
		    LEFT JOIN servers s ON s.id = n.server_id AND s.tenant_id = n.tenant_id
		   WHERE ` + filter + `
		   -- 与「调整排序」同一个顺序（契约后台-07）
		   ORDER BY n.sort_order, n.node_no, n.id
		   LIMIT ` + limitArg + ` OFFSET ` + offsetArg + `
		), ids AS MATERIALIZED (
		  SELECT ARRAY(SELECT id FROM page) AS ids
		), win AS MATERIALIZED (
		  SELECT now() - make_interval(mins => app.device_limit_window_minutes($1)) AS since
		), alive AS MATERIALIZED (
		  SELECT node_id,
		         count(DISTINCT subscription_id)::int AS users,
		         count(*)::int AS ips
		    FROM node_alive_ips
		   WHERE tenant_id = $1
		     AND node_id = ANY ((SELECT ids FROM ids)::uuid[])
		     AND last_seen_at > (SELECT since FROM win)
		   GROUP BY node_id
		), traffic AS MATERIALIZED (
		  -- 原始量（Go 端合计、未乘倍率、不含重复上报），按整点桶：桶起点落在
		  -- 近 30 天 / 近 24 小时内的才算，窗口最旧的不足一小时不计。
		  -- 按主键 (tenant_id, hour_start, node_id) 扫窗口内的区间，节点 id 在索引项上过滤
		  SELECT node_id,
		         sum(raw_bytes)::bigint AS bytes,
		         coalesce(sum(raw_bytes)
		           FILTER (WHERE hour_start >= now() - interval '24 hours'), 0)::bigint AS bytes_24h
		    FROM node_traffic_hourly
		   WHERE tenant_id = $1
		     AND hour_start >= now() - ` + window + `
		     AND node_id = ANY ((SELECT ids FROM ids)::uuid[])
		   GROUP BY node_id
		), grants AS MATERIALIZED (
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
		       n.display_name, n.country_code, n.sort_order, n.node_no,
		       coalesce(a.users,0), coalesce(a.ips,0),
		       coalesce(g.plans, '{}'), coalesce(t.bytes_24h,0),
		       m.cpu_bp / 100.0,
		       CASE WHEN m.mem_total_mb > 0 THEN round(m.mem_used_mb * 100.0 / m.mem_total_mb, 1) END,
		       m.recorded_at,
		       pg.total,
		       -- 编辑字段：只有单取时读，列表不碰 protocol_config（jsonb，常常够大到要去 TOAST 取）
		       CASE WHEN ` + edit + ` THEN n.traffic_rate END,
		       CASE WHEN ` + edit + ` THEN coalesce(n.kernel,'auto') END,
		       CASE WHEN ` + edit + ` THEN n.protocol_config END,
		       CASE WHEN ` + edit + ` THEN n.protocol_schema_version END,
		       CASE WHEN ` + edit + ` THEN n.config_validated_at END,
		       CASE WHEN ` + edit + ` THEN coalesce(t.bytes,0) ELSE 0 END,
		       ` + nodeRuntimeViewColumns + `
		  FROM page pg
		  JOIN nodes n ON n.tenant_id = $1 AND n.id = pg.id
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
		  ` + nodeRuntimeViewJoins + `
		 ORDER BY pg.sort_order, pg.node_no, pg.id`
	return adminNodeListStmt{
		sql:  sql,
		args: args,
		countSQL: `
		SELECT count(*) FROM nodes n
		  LEFT JOIN servers s ON s.id = n.server_id AND s.tenant_id = n.tenant_id
		 WHERE ` + filter,
		countArgs: countArgs,
	}
}

func (s *Service) queryAdminNodes(ctx context.Context, tenantID string, q AdminNodeQuery, detail bool) ([]AdminNodeListRow, int64, error) {
	stmt := adminNodeListSQL(tenantID, q, detail)
	out := []AdminNodeListRow{}
	var total int64
	scope := db.Scope{TenantID: tenantID}
	err := s.pool.QueryScoped(ctx, scope, stmt.sql, stmt.args, func(rows pgx.Rows) error {
		var x AdminNodeListRow
		var rate *float64
		var kernel *string
		var schemaVer *int
		var rv nodeRuntimeViewScan
		dest := []any{&x.ID, &x.RowVersion, &x.Name, &x.Status, &x.ServingStatus,
			&x.ServerID, &x.ServerName, &x.PoolID, &x.PoolName, &x.AgentVer,
			&x.Hostname, &x.PublicIP, &x.CPUCores, &x.MemoryMB, &x.DiskGB,
			&x.HealthScore, &x.AppliedVer, &x.DesiredVer, &x.LastBeat,
			&x.Stale, &x.Serial, &x.CreatedAt,
			&x.NodeType, &x.ServerHost, &x.ServerPort,
			&x.DisplayName, &x.CountryCode, &x.SortOrder, &x.NodeNo,
			&x.OnlineUsers, &x.OnlineIPs,
			&x.GrantedPlans, &x.TrafficBytes24h, &x.CPUPercent, &x.MemPercent, &x.MetricsAt,
			&total,
			&rate, &kernel, &x.Protocol, &schemaVer, &x.ConfigValidatedAt,
			&x.TrafficBytes}
		if err := rows.Scan(append(dest, rv.dest()...)...); err != nil {
			return err
		}
		x.NodeRuntimeView = rv.view()
		if rate != nil {
			x.TrafficRate = *rate
		}
		if kernel != nil {
			x.Kernel = *kernel
		}
		if schemaVer != nil {
			x.ProtocolSchemaVersion = *schemaVer
		}
		if detail {
			x.Warnings = ProtocolConfigWarnings(value(x.NodeType), x.Protocol)
		}
		out = append(out, x)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if len(out) > 0 || q.Offset <= 0 {
		return out, total, nil
	}
	// 翻过最后一页：窗口函数没有行可带，按同样的筛选单独数一次（少见路径，另一次往返）
	err = s.pool.QueryRowScoped(ctx, scope, stmt.countSQL, stmt.countArgs, &total)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
