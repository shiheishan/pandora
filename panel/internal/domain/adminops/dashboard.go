package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const dashboardTimestampLayout = "2006-01-02T15:04:05.000000Z"

var dashboardRFC3339UTC = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+]00:00)$`)

type DashboardTrafficQuery struct {
	Range      string
	Limit      int
	SnapshotAt string
}

type DashboardTrafficTotals struct {
	ReportedBytes     string `json:"reported_bytes"`
	AttributedBytes   string `json:"attributed_bytes"`
	UnattributedBytes string `json:"unattributed_bytes"`
}

type DashboardTrafficQuality struct {
	DuplicateReportCount int64 `json:"duplicate_report_count"`
	InvalidReportCount   int64 `json:"invalid_report_count"`
	InvalidEntryCount    int64 `json:"invalid_entry_count"`
}

type DashboardNodeTrafficItem struct {
	NodeID                 string  `json:"node_id"`
	Name                   string  `json:"name"`
	DisplayName            *string `json:"display_name"`
	UploadBytes            string  `json:"upload_bytes"`
	DownloadBytes          string  `json:"download_bytes"`
	TotalBytes             string  `json:"total_bytes"`
	ContributingEntryCount int64   `json:"contributing_entry_count"`
	ReportCount            int64   `json:"report_count"`
	LastReportAt           string  `json:"last_report_at"`
}

type DashboardNodeTrafficRanking struct {
	ReturnedBytes  string `json:"returned_bytes"`
	OtherNodeBytes string `json:"other_node_bytes"`
}

type DashboardNodeTraffic struct {
	Range      string `json:"range"`
	SnapshotAt string `json:"snapshot_at"`
	// From 是实际计入的下界：snapshot_at 减区间后向上取整到整点（读的是小时汇总）
	From    string                      `json:"from"`
	To      string                      `json:"to"`
	Basis   string                      `json:"basis"`
	Items   []DashboardNodeTrafficItem  `json:"items"`
	Totals  DashboardTrafficTotals      `json:"totals"`
	Ranking DashboardNodeTrafficRanking `json:"ranking"`
	Quality DashboardTrafficQuality     `json:"quality"`
}

type DashboardUserTrafficItem struct {
	UserID                 string `json:"user_id"`
	EmailMasked            string `json:"email_masked"`
	UploadBytes            string `json:"upload_bytes"`
	DownloadBytes          string `json:"download_bytes"`
	TotalBytes             string `json:"total_bytes"`
	SubscriptionCount      int64  `json:"subscription_count"`
	ContributingEntryCount int64  `json:"contributing_entry_count"`
	LastReportAt           string `json:"last_report_at"`
}

type DashboardUserTrafficRanking struct {
	ReturnedBytes  string `json:"returned_bytes"`
	OtherUserBytes string `json:"other_user_bytes"`
}

type DashboardUserTraffic struct {
	Range      string `json:"range"`
	SnapshotAt string `json:"snapshot_at"`
	// From 同 DashboardNodeTraffic.From
	From    string                      `json:"from"`
	To      string                      `json:"to"`
	Basis   string                      `json:"basis"`
	Items   []DashboardUserTrafficItem  `json:"items"`
	Totals  DashboardTrafficTotals      `json:"totals"`
	Ranking DashboardUserTrafficRanking `json:"ranking"`
	Quality DashboardTrafficQuality     `json:"quality"`
}

type DashboardNotificationCounts struct {
	Ready               int64 `json:"ready"`
	ReadyRetry          int64 `json:"ready_retry"`
	Scheduled           int64 `json:"scheduled"`
	ScheduledRetry      int64 `json:"scheduled_retry"`
	SendingUnobservable int64 `json:"sending_unobservable"`
	FailedTotal         int64 `json:"failed_total"`
	SuppressedTotal     int64 `json:"suppressed_total"`
	BouncedTotal        int64 `json:"bounced_total"`
}

type DashboardNotificationAssessment struct {
	ThresholdSeconds int64  `json:"threshold_seconds"`
	Reason           string `json:"reason"`
}

type DashboardNotificationBacklog struct {
	AsOf                   string                          `json:"as_of"`
	BacklogState           string                          `json:"backlog_state"`
	ProcessorState         string                          `json:"processor_state"`
	ScannerIntervalSeconds int64                           `json:"scanner_interval_seconds"`
	Counts                 DashboardNotificationCounts     `json:"counts"`
	OldestReadyAt          *string                         `json:"oldest_ready_at"`
	MaxReadyLagSeconds     int64                           `json:"max_ready_lag_seconds"`
	LastSentAt             *string                         `json:"last_sent_at"`
	Assessment             DashboardNotificationAssessment `json:"assessment"`
}

type dashboardWindow struct {
	snapshot     time.Time
	from         time.Time
	to           time.Time
	snapshotText string
	fromText     string
	toText       string
}

func validateDashboardTrafficQuery(in DashboardTrafficQuery) (DashboardTrafficQuery, *time.Time, error) {
	if in.Range == "" {
		in.Range = "7d"
	}
	if in.Limit == 0 {
		in.Limit = 10
	}
	if in.Range != "24h" && in.Range != "7d" && in.Range != "30d" {
		return in, nil, httpx.New(httpx.CodeValidationFailed, "range 只能是 24h、7d 或 30d")
	}
	if in.Limit != 5 && in.Limit != 10 && in.Limit != 20 {
		return in, nil, httpx.New(httpx.CodeValidationFailed, "limit 只能是 5、10 或 20")
	}
	if in.SnapshotAt == "" {
		return in, nil, nil
	}
	if !dashboardRFC3339UTC.MatchString(in.SnapshotAt) {
		return in, nil, httpx.New(httpx.CodeValidationFailed, "snapshot_at 必须是 RFC3339 UTC 时间")
	}
	parsed, err := time.Parse(time.RFC3339Nano, in.SnapshotAt)
	if err != nil {
		return in, nil, httpx.New(httpx.CodeValidationFailed, "snapshot_at 必须是 RFC3339 UTC 时间")
	}
	return in, &parsed, nil
}

func dashboardRangeInterval(value string) string {
	switch value {
	case "24h":
		return "24 hours"
	case "30d":
		return "30 days"
	default:
		return "7 days"
	}
}

func resolveDashboardWindow(ctx context.Context, tx pgx.Tx, in DashboardTrafficQuery, parsed *time.Time) (dashboardWindow, error) {
	var requested any
	if parsed != nil {
		// 临时变异：忽略传入的 snapshot，总用 now。本提交随后 revert。
		requested = nil
	}
	var out dashboardWindow
	err := tx.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
		  SELECT statement_timestamp()::timestamptz(6) AS now_at
		), requested AS MATERIALIZED (
		  SELECT coalesce($1::text::timestamptz, now_at)::timestamptz(6) AS snapshot_at,
		         now_at
		    FROM clock
		), bounds AS (
		  SELECT snapshot_at,
		         (snapshot_at - $2::interval)::timestamptz(6) AS exact_from_at,
		         snapshot_at AS to_at
		    FROM requested
		   WHERE snapshot_at <= now_at
		     AND snapshot_at >= now_at - interval '31 days'
		), aligned AS (
		  -- 汇总按整点桶：起点向上取整到整点（UTC），报出来的 from 就是实际计入的下界
		  SELECT snapshot_at, to_at,
		         CASE WHEN date_trunc('hour', exact_from_at, 'UTC') = exact_from_at THEN exact_from_at
		              ELSE date_trunc('hour', exact_from_at, 'UTC') + interval '1 hour'
		         END AS from_at
		    FROM bounds
		)
		SELECT snapshot_at, from_at, to_at,
		       to_char(snapshot_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
		       to_char(from_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
		       to_char(to_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
		  FROM aligned`, requested, dashboardRangeInterval(in.Range)).Scan(
		&out.snapshot, &out.from, &out.to, &out.snapshotText, &out.fromText, &out.toText)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.New(httpx.CodeValidationFailed, "snapshot_at 超出允许范围")
	}
	return out, err
}

// dashboardTrafficWindowCTE 是节点排行与用户排行共用的窗口读数，只读小时汇总
// （迁移 00099），不再回到上报留档逐条展开 raw_payload。
//
// 校验口径没有变：汇总表是入库时按 app.node_traffic_payload_entries 分类累加的，
// 那个函数就是原来这里 strict_entries CTE 的原文（键 [+-]?数字、归一后不超过 19 位、
// 落在 int64 内；值为两个 JSON 整数、落在 [0, int64 最大值]）。重复上报只进
// duplicate_report_count；非法上报 = 根不是对象或含非法项的非重复上报。归属仍在读时
// 按 subscriptions.node_uid 连，订阅不在了就算未归属。PG18 测试拿原 SQL 对同一批
// 上报逐项比对（dashboard_traffic_pg18_test.go）。
//
// 窗口按整点桶取：桶起点 hour_start 落在 [$2, $3) 的桶整桶计入。$2 由
// resolveDashboardWindow 向上取整到整点，最旧不足一小时的那段不计；最新的桶是
// 当前小时，snapshot_at 取现在时它正好截到现在。
//
// 归属的读法两条排行各写各的（node_uid 全局唯一，一个 uid 至多一条订阅，两种读法都与
// 逐小时行连订阅的结果相同）：
//   - 节点排行只要「已归属字节」一个总数：节点×uid 行对订阅做半连接后直接求和，不分组；
//   - 用户排行先在窗口内按 uid 聚合，再每个 uid 连一次订阅（原来是每个 uid×小时×节点行
//     连一次），按用户分组时每行就是一条不同的订阅，订阅数用 count(*)（可走哈希聚合，
//     原来的 count(DISTINCT) 要排序、逐组建排序器）。
//
// traffic_totals 必须 MATERIALIZED：它被最终 SELECT 引用三次，内联后「已归属字节」那个
// 子查询会按引用次数重算。
const dashboardTrafficWindowCTE = `
node_hours AS MATERIALIZED (
  SELECT h.node_id,
         sum(h.upload_bytes) AS upload_bytes,
         sum(h.download_bytes) AS download_bytes,
         sum(h.positive_entry_count)::bigint AS contributing_entry_count,
         sum(h.positive_report_count)::bigint AS report_count,
         max(h.last_positive_report_at) AS last_report_at,
         sum(h.duplicate_report_count) AS duplicate_report_count,
         sum(h.invalid_report_count) AS invalid_report_count,
         sum(h.invalid_entry_count) AS invalid_entry_count
    FROM node_traffic_hourly h
   WHERE h.tenant_id=$1 AND h.hour_start >= $2 AND h.hour_start < $3
   GROUP BY h.node_id
),
quality AS MATERIALIZED (
  SELECT coalesce(sum(duplicate_report_count),0)::bigint AS duplicate_report_count,
         coalesce(sum(invalid_report_count),0)::bigint AS invalid_report_count,
         coalesce(sum(invalid_entry_count),0)::bigint AS invalid_entry_count,
         coalesce(sum(upload_bytes+download_bytes),0)::numeric AS reported_bytes
    FROM node_hours
)
`

const dashboardNodeTrafficSQL = `WITH ` + dashboardTrafficWindowCTE + `,
traffic_totals AS MATERIALIZED (
  SELECT q.reported_bytes,
         (SELECT coalesce(sum(t.upload_bytes+t.download_bytes),0)
            FROM node_user_traffic_hourly t
           WHERE t.tenant_id=$1 AND t.hour_start >= $2 AND t.hour_start < $3
             AND EXISTS (SELECT 1 FROM subscriptions sub
                          WHERE sub.tenant_id=$1 AND sub.node_uid=t.node_uid
                            AND sub.user_id IS NOT NULL))::numeric AS attributed_bytes
    FROM quality q
),
node_groups AS (
  SELECT g.node_id, n.name, n.display_name,
         g.upload_bytes::numeric AS upload_bytes,
         g.download_bytes::numeric AS download_bytes,
         (g.upload_bytes+g.download_bytes)::numeric AS total_bytes,
         g.contributing_entry_count, g.report_count,
         g.last_report_at::timestamptz(6) AS last_report_at
    FROM node_hours g
    JOIN nodes n ON n.tenant_id=$1 AND n.id=g.node_id
   WHERE g.upload_bytes+g.download_bytes > 0
),
ranked AS MATERIALIZED (
  SELECT * FROM node_groups ORDER BY total_bytes DESC,node_id ASC LIMIT $4
),
ranking AS (
  SELECT coalesce(sum(total_bytes),0)::numeric AS returned_bytes FROM ranked
)
SELECT coalesce((
         SELECT jsonb_agg(jsonb_build_object(
           'node_id',node_id::text,'name',name,'display_name',display_name,
           'upload_bytes',trim_scale(upload_bytes)::text,'download_bytes',trim_scale(download_bytes)::text,
           'total_bytes',trim_scale(total_bytes)::text,'contributing_entry_count',contributing_entry_count,
           'report_count',report_count,
           'last_report_at',to_char(last_report_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
         ) ORDER BY total_bytes DESC,node_id ASC) FROM ranked
       ),'[]'::jsonb),
       trim_scale(t.reported_bytes)::text,trim_scale(t.attributed_bytes)::text,
       trim_scale(t.reported_bytes-t.attributed_bytes)::text,
       trim_scale(r.returned_bytes)::text,trim_scale(t.reported_bytes-r.returned_bytes)::text,
       q.duplicate_report_count,q.invalid_report_count,q.invalid_entry_count
  FROM traffic_totals t CROSS JOIN ranking r CROSS JOIN quality q`

const dashboardUserTrafficSQL = `WITH ` + dashboardTrafficWindowCTE + `,
uid_hours AS MATERIALIZED (
  SELECT t.node_uid,
         sum(t.upload_bytes) AS upload_bytes,
         sum(t.download_bytes) AS download_bytes,
         sum(t.entry_count) AS entry_count,
         max(t.last_report_at) AS last_report_at
    FROM node_user_traffic_hourly t
   WHERE t.tenant_id=$1 AND t.hour_start >= $2 AND t.hour_start < $3
   GROUP BY t.node_uid
),
attributed AS MATERIALIZED (
  SELECT sub.user_id, g.upload_bytes, g.download_bytes,
         g.upload_bytes+g.download_bytes AS total_bytes, g.entry_count, g.last_report_at
    FROM uid_hours g
    JOIN subscriptions sub
      ON sub.tenant_id=$1
     AND sub.node_uid=g.node_uid
   WHERE sub.user_id IS NOT NULL
),
traffic_totals AS MATERIALIZED (
  SELECT q.reported_bytes,
         (SELECT coalesce(sum(total_bytes),0) FROM attributed)::numeric AS attributed_bytes
    FROM quality q
),
user_groups AS (
  SELECT a.user_id,u.email::text AS email,
         sum(a.upload_bytes)::numeric AS upload_bytes,
         sum(a.download_bytes)::numeric AS download_bytes,
         sum(a.total_bytes)::numeric AS total_bytes,
         -- 每行是一条不同的订阅（uid 唯一 → 订阅唯一），等于按订阅去重计数
         count(*)::bigint AS subscription_count,
         sum(a.entry_count)::bigint AS contributing_entry_count,
         max(a.last_report_at)::timestamptz(6) AS last_report_at
    FROM attributed a
    JOIN users u ON u.tenant_id=$1 AND u.id=a.user_id
   GROUP BY a.user_id,u.email
),
ranked AS MATERIALIZED (
  SELECT * FROM user_groups ORDER BY total_bytes DESC,user_id ASC LIMIT $4
),
ranking AS (
  SELECT coalesce(sum(total_bytes),0)::numeric AS returned_bytes FROM ranked
)
SELECT coalesce((
         SELECT jsonb_agg(jsonb_build_object(
           'user_id',user_id::text,
           'email_masked',CASE WHEN email ~ '^[^@]+@[^@]+$'
             THEN left(email,1)||'***@'||lower(split_part(email,'@',2)) ELSE '***' END,
           'upload_bytes',trim_scale(upload_bytes)::text,'download_bytes',trim_scale(download_bytes)::text,
           'total_bytes',trim_scale(total_bytes)::text,'subscription_count',subscription_count,
           'contributing_entry_count',contributing_entry_count,
           'last_report_at',to_char(last_report_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
         ) ORDER BY total_bytes DESC,user_id ASC) FROM ranked
       ),'[]'::jsonb),
       trim_scale(t.reported_bytes)::text,trim_scale(t.attributed_bytes)::text,
       trim_scale(t.reported_bytes-t.attributed_bytes)::text,
       trim_scale(r.returned_bytes)::text,trim_scale(t.attributed_bytes-r.returned_bytes)::text,
       q.duplicate_report_count,q.invalid_report_count,q.invalid_entry_count
  FROM traffic_totals t CROSS JOIN ranking r CROSS JOIN quality q`

// dashboardTrafficCacheKey 是「现在」视图的缓存键；带 snapshot_at 的历史查询不缓存（返回空串）。
func dashboardTrafficCacheKey(kind, tenantID string, in DashboardTrafficQuery) string {
	if in.SnapshotAt != "" {
		return ""
	}
	return kind + ":" + tenantID + ":" + in.Range + ":" + strconv.Itoa(in.Limit)
}

// DashboardNodeTraffic 读节点流量排行；「现在」视图按（租户, 区间, 条数）缓存 dashboardCacheTTL，
// 返回的 snapshot_at 就是那次计算的时刻，数据与它一致。
func (s *Service) DashboardNodeTraffic(ctx context.Context, tenantID string, input DashboardTrafficQuery) (*DashboardNodeTraffic, error) {
	in, parsed, err := validateDashboardTrafficQuery(input)
	if err != nil {
		return nil, err
	}
	key := dashboardTrafficCacheKey("traffic-nodes", tenantID, in)
	if key == "" {
		return s.readDashboardNodeTraffic(ctx, tenantID, in, parsed)
	}
	return cachedRead(s.dash, key, func() (*DashboardNodeTraffic, error) {
		return s.readDashboardNodeTraffic(ctx, tenantID, in, parsed)
	})
}

func (s *Service) readDashboardNodeTraffic(ctx context.Context, tenantID string, in DashboardTrafficQuery, parsed *time.Time) (*DashboardNodeTraffic, error) {
	out := &DashboardNodeTraffic{Range: in.Range, Basis: "strict_raw_report_entries", Items: []DashboardNodeTrafficItem{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		window, err := resolveDashboardWindow(ctx, tx, in, parsed)
		if err != nil {
			return err
		}
		out.SnapshotAt, out.From, out.To = window.snapshotText, window.fromText, window.toText
		return scanDashboardNodeTraffic(ctx, tx, dashboardNodeTrafficSQL, tenantID, window.from, window.to, in.Limit, out)
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// DashboardUserTraffic 读用户流量排行，缓存口径同 DashboardNodeTraffic。
func (s *Service) DashboardUserTraffic(ctx context.Context, tenantID string, input DashboardTrafficQuery) (*DashboardUserTraffic, error) {
	in, parsed, err := validateDashboardTrafficQuery(input)
	if err != nil {
		return nil, err
	}
	key := dashboardTrafficCacheKey("traffic-users", tenantID, in)
	if key == "" {
		return s.readDashboardUserTraffic(ctx, tenantID, in, parsed)
	}
	return cachedRead(s.dash, key, func() (*DashboardUserTraffic, error) {
		return s.readDashboardUserTraffic(ctx, tenantID, in, parsed)
	})
}

func (s *Service) readDashboardUserTraffic(ctx context.Context, tenantID string, in DashboardTrafficQuery, parsed *time.Time) (*DashboardUserTraffic, error) {
	out := &DashboardUserTraffic{Range: in.Range, Basis: "strict_raw_report_entries", Items: []DashboardUserTrafficItem{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		window, err := resolveDashboardWindow(ctx, tx, in, parsed)
		if err != nil {
			return err
		}
		out.SnapshotAt, out.From, out.To = window.snapshotText, window.fromText, window.toText
		return scanDashboardUserTraffic(ctx, tx, dashboardUserTrafficSQL, tenantID, window.from, window.to, in.Limit, out)
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// scanDashboardNodeTraffic 执行节点排行查询并填进 out（排行、合计、质量）。查询文本
// 由调用方给：服务用 dashboardNodeTrafficSQL，PG18 对照测试拿同样的列形状跑原 SQL。
func scanDashboardNodeTraffic(ctx context.Context, tx pgx.Tx, query, tenantID string, from, to time.Time, limit int, out *DashboardNodeTraffic) error {
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, query, tenantID, from, to, limit).Scan(
		&raw, &out.Totals.ReportedBytes, &out.Totals.AttributedBytes, &out.Totals.UnattributedBytes,
		&out.Ranking.ReturnedBytes, &out.Ranking.OtherNodeBytes,
		&out.Quality.DuplicateReportCount, &out.Quality.InvalidReportCount, &out.Quality.InvalidEntryCount); err != nil {
		return err
	}
	return json.Unmarshal(raw, &out.Items)
}

// scanDashboardUserTraffic 同 scanDashboardNodeTraffic，用户排行。
func scanDashboardUserTraffic(ctx context.Context, tx pgx.Tx, query, tenantID string, from, to time.Time, limit int, out *DashboardUserTraffic) error {
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, query, tenantID, from, to, limit).Scan(
		&raw, &out.Totals.ReportedBytes, &out.Totals.AttributedBytes, &out.Totals.UnattributedBytes,
		&out.Ranking.ReturnedBytes, &out.Ranking.OtherUserBytes,
		&out.Quality.DuplicateReportCount, &out.Quality.InvalidReportCount, &out.Quality.InvalidEntryCount); err != nil {
		return err
	}
	return json.Unmarshal(raw, &out.Items)
}

const dashboardNotificationBacklogSQL = `
WITH clock AS MATERIALIZED (
  SELECT statement_timestamp()::timestamptz(6) AS as_of
), facts AS MATERIALIZED (
  SELECT c.as_of,
         count(*) FILTER (WHERE d.status='queued' AND coalesce(d.next_retry_at,d.created_at)<=c.as_of)::bigint AS ready,
         count(*) FILTER (WHERE d.status='queued' AND coalesce(d.next_retry_at,d.created_at)<=c.as_of AND d.attempts>0)::bigint AS ready_retry,
         count(*) FILTER (WHERE d.status='queued' AND coalesce(d.next_retry_at,d.created_at)>c.as_of)::bigint AS scheduled,
         count(*) FILTER (WHERE d.status='queued' AND coalesce(d.next_retry_at,d.created_at)>c.as_of AND d.attempts>0)::bigint AS scheduled_retry,
         count(*) FILTER (WHERE d.status='sending')::bigint AS sending_unobservable,
         count(*) FILTER (WHERE d.status='failed')::bigint AS failed_total,
         count(*) FILTER (WHERE d.status='suppressed')::bigint AS suppressed_total,
         count(*) FILTER (WHERE d.status='bounced')::bigint AS bounced_total,
         min(coalesce(d.next_retry_at,d.created_at)) FILTER (
           WHERE d.status='queued' AND coalesce(d.next_retry_at,d.created_at)<=c.as_of
         )::timestamptz(6) AS oldest_ready_at,
         max(d.sent_at) FILTER (WHERE d.status='sent')::timestamptz(6) AS last_sent_at
    FROM clock c
    LEFT JOIN notification_deliveries d ON true AND $1::uuid IS NOT NULL
   GROUP BY c.as_of
), lag AS MATERIALIZED (
  SELECT f.*,CASE WHEN ready=0 THEN NULL::interval ELSE as_of-oldest_ready_at END AS lag_exact FROM facts f
)
SELECT to_char(as_of AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
       CASE WHEN lag_exact>=interval '600 seconds' THEN 'backlogged' ELSE 'clear' END,
       ready,ready_retry,scheduled,scheduled_retry,sending_unobservable,
       failed_total,suppressed_total,bounced_total,
       CASE WHEN oldest_ready_at IS NULL THEN NULL ELSE to_char(oldest_ready_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
       CASE WHEN ready=0 THEN 0::bigint ELSE ceil(extract(epoch FROM greatest(lag_exact,interval '0 seconds')))::bigint END,
       CASE WHEN last_sent_at IS NULL THEN NULL ELSE to_char(last_sent_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
       CASE WHEN ready=0 THEN 'no_due_backlog'
            WHEN lag_exact>=interval '600 seconds' THEN 'lag_exceeded'
            ELSE 'within_threshold' END
  FROM lag`

func (s *Service) DashboardNotificationBacklog(ctx context.Context, tenantID string) (*DashboardNotificationBacklog, error) {
	var out *DashboardNotificationBacklog
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) (err error) {
		out, err = scanNotificationBacklog(ctx, tx, tenantID)
		return err
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// scanNotificationBacklog 是通知积压的唯一口径，dashboard/backlog/notifications 与
// dashboard/tasks 的 notifications_backlog 项共用。
func scanNotificationBacklog(ctx context.Context, tx pgx.Tx, tenantID string) (*DashboardNotificationBacklog, error) {
	out := &DashboardNotificationBacklog{
		ProcessorState: "unobservable", ScannerIntervalSeconds: 300,
		Assessment: DashboardNotificationAssessment{ThresholdSeconds: 600},
	}
	err := tx.QueryRow(ctx, dashboardNotificationBacklogSQL, tenantID).Scan(
		&out.AsOf, &out.BacklogState,
		&out.Counts.Ready, &out.Counts.ReadyRetry, &out.Counts.Scheduled, &out.Counts.ScheduledRetry,
		&out.Counts.SendingUnobservable, &out.Counts.FailedTotal, &out.Counts.SuppressedTotal, &out.Counts.BouncedTotal,
		&out.OldestReadyAt, &out.MaxReadyLagSeconds, &out.LastSentAt, &out.Assessment.Reason)
	if err != nil {
		return nil, err
	}
	return out, nil
}
