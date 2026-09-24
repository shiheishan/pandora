// [INPUT]: 依赖 platform/db 的租户事务与 platform/httpx 的错误模型，读 node_traffic_reports / subscription 用量 / notification_deliveries
// [OUTPUT]: 对外提供 DashboardTrafficQuery、DashboardNodeTraffic / DashboardUserTraffic 排行、DashboardNotificationBacklog 与对应 Service 方法
// [POS]: domain/adminops 的仪表盘读模型：流量排行与通知投递积压；积压口径 scanNotificationBacklog 也被 dashboard_tasks.go 复用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
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
	Range      string                      `json:"range"`
	SnapshotAt string                      `json:"snapshot_at"`
	From       string                      `json:"from"`
	To         string                      `json:"to"`
	Basis      string                      `json:"basis"`
	Items      []DashboardNodeTrafficItem  `json:"items"`
	Totals     DashboardTrafficTotals      `json:"totals"`
	Ranking    DashboardNodeTrafficRanking `json:"ranking"`
	Quality    DashboardTrafficQuality     `json:"quality"`
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
	Range      string                      `json:"range"`
	SnapshotAt string                      `json:"snapshot_at"`
	From       string                      `json:"from"`
	To         string                      `json:"to"`
	Basis      string                      `json:"basis"`
	Items      []DashboardUserTrafficItem  `json:"items"`
	Totals     DashboardTrafficTotals      `json:"totals"`
	Ranking    DashboardUserTrafficRanking `json:"ranking"`
	Quality    DashboardTrafficQuality     `json:"quality"`
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
		// Preserve the original RFC3339 token so PostgreSQL, not the Go driver,
		// performs the frozen timestamptz(6) rounding for sub-microsecond input.
		requested = in.SnapshotAt
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
		         (snapshot_at - $2::interval)::timestamptz(6) AS from_at,
		         snapshot_at AS to_at
		    FROM requested
		   WHERE snapshot_at <= now_at
		     AND snapshot_at >= now_at - interval '31 days'
		)
		SELECT snapshot_at, from_at, to_at,
		       to_char(snapshot_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
		       to_char(from_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
		       to_char(to_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
		  FROM bounds`, requested, dashboardRangeInterval(in.Range)).Scan(
		&out.snapshot, &out.from, &out.to, &out.snapshotText, &out.fromText, &out.toText)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.New(httpx.CodeValidationFailed, "snapshot_at 超出允许范围")
	}
	return out, err
}

// dashboardTrafficClassificationCTE is the single SQL source of truth consumed
// by both node and user rankings. Every cast is fed only a bounded/type-checked
// value so arbitrary historical JSONB cannot turn a dashboard read into a 500.
const dashboardTrafficClassificationCTE = `
duplicate_quality AS (
  SELECT count(*)::bigint AS duplicate_report_count
    FROM node_traffic_reports
   WHERE tenant_id=$1 AND received_at >= $2 AND received_at < $3
     AND duplicate_of IS NOT NULL
),
report_shape AS MATERIALIZED (
  SELECT id AS report_id, tenant_id, node_id, received_at, raw_payload,
         jsonb_typeof(raw_payload)='object' AS root_is_object
    FROM node_traffic_reports
   WHERE tenant_id=$1 AND received_at >= $2 AND received_at < $3
     AND duplicate_of IS NULL
),
strict_entries AS MATERIALIZED (
  SELECT r.report_id, r.tenant_id, r.node_id, r.received_at,
         parsed.parsed_uid_numeric, numbers.upload_numeric, numbers.download_numeric,
         coalesce(
           parsed.parsed_uid_numeric IS NOT NULL AND components.shape_ok IS TRUE
           AND numbers.upload_numeric=trunc(numbers.upload_numeric)
           AND numbers.download_numeric=trunc(numbers.download_numeric)
           AND numbers.upload_numeric BETWEEN 0 AND 9223372036854775807::numeric
           AND numbers.download_numeric BETWEEN 0 AND 9223372036854775807::numeric,
           false
         ) AS is_valid
    FROM report_shape r
    CROSS JOIN LATERAL jsonb_each(
      CASE WHEN r.root_is_object IS TRUE THEN r.raw_payload ELSE '{}'::jsonb END
    ) entry(key_text,value_json)
    CROSS JOIN LATERAL (
      SELECT entry.key_text ~ '^[+-]?[0-9]+$' AS key_syntax_ok,
             CASE WHEN entry.key_text ~ '^[+-]?[0-9]+$' THEN
               CASE WHEN left(entry.key_text,1)='-' THEN '-' ELSE '' END ||
               coalesce(nullif(regexp_replace(ltrim(entry.key_text,'+-'), '^0+', ''), ''), '0')
             END AS normalized_key_text
    ) key_lex
    CROSS JOIN LATERAL (
      SELECT CASE WHEN key_lex.key_syntax_ok IS TRUE
                       AND length(ltrim(key_lex.normalized_key_text,'-')) <= 19
                  THEN key_lex.normalized_key_text END AS bounded_key_text
    ) key_bound
    CROSS JOIN LATERAL (
      SELECT CASE WHEN key_bound.bounded_key_text IS NOT NULL
                       AND key_bound.bounded_key_text::numeric BETWEEN
                           -9223372036854775808::numeric AND 9223372036854775807::numeric
                  THEN key_bound.bounded_key_text::numeric END AS parsed_uid_numeric
    ) parsed
    CROSS JOIN LATERAL (
      SELECT CASE WHEN jsonb_typeof(entry.value_json)='array'
                  THEN jsonb_array_length(entry.value_json) END AS array_len
    ) value_shape
    CROSS JOIN LATERAL (
      SELECT value_shape.array_len=2 AS shape_ok,
             CASE WHEN value_shape.array_len=2 THEN entry.value_json->0 END AS upload_json,
             CASE WHEN value_shape.array_len=2 THEN entry.value_json->1 END AS download_json
    ) components
    CROSS JOIN LATERAL (
      SELECT CASE WHEN jsonb_typeof(components.upload_json)='number'
                  THEN (components.upload_json #>> '{}')::numeric END AS upload_numeric,
             CASE WHEN jsonb_typeof(components.download_json)='number'
                  THEN (components.download_json #>> '{}')::numeric END AS download_numeric
    ) numbers
),
entry_report_quality AS (
  SELECT report_id,
         count(*) FILTER (WHERE is_valid IS NOT TRUE)::bigint AS invalid_entry_count,
         bool_or(is_valid IS NOT TRUE) AS has_invalid_entry
    FROM strict_entries GROUP BY report_id
),
report_quality AS (
  SELECT count(*) FILTER (
           WHERE r.root_is_object IS NOT TRUE
              OR coalesce(eq.has_invalid_entry,false)
         )::bigint AS invalid_report_count,
         coalesce(sum(eq.invalid_entry_count),0)::bigint AS invalid_entry_count
    FROM report_shape r
    LEFT JOIN entry_report_quality eq ON eq.report_id=r.report_id
),
attributed AS MATERIALIZED (
  SELECT s.report_id, s.tenant_id, s.node_id, s.received_at,
         sub.id AS subscription_id, sub.user_id,
         trunc(s.upload_numeric) AS valid_upload,
         trunc(s.download_numeric) AS valid_download
    FROM strict_entries s
    LEFT JOIN subscriptions sub
      ON sub.tenant_id=s.tenant_id
     AND sub.node_uid=s.parsed_uid_numeric::bigint
   WHERE s.is_valid IS TRUE
),
traffic_totals AS (
  SELECT coalesce(sum(valid_upload+valid_download),0)::numeric AS reported_bytes,
         coalesce(sum(valid_upload+valid_download) FILTER (WHERE subscription_id IS NOT NULL AND user_id IS NOT NULL),0)::numeric AS attributed_bytes
    FROM attributed
)
`

const dashboardNodeTrafficSQL = `WITH ` + dashboardTrafficClassificationCTE + `,
node_groups AS (
  SELECT a.node_id, n.name, n.display_name,
         sum(a.valid_upload)::numeric AS upload_bytes,
         sum(a.valid_download)::numeric AS download_bytes,
         sum(a.valid_upload+a.valid_download)::numeric AS total_bytes,
         count(*)::bigint AS contributing_entry_count,
         count(DISTINCT a.report_id)::bigint AS report_count,
         max(a.received_at)::timestamptz(6) AS last_report_at
    FROM attributed a
    JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.node_id
   WHERE a.valid_upload+a.valid_download > 0
   GROUP BY a.node_id,n.name,n.display_name
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
       d.duplicate_report_count,q.invalid_report_count,q.invalid_entry_count
  FROM traffic_totals t CROSS JOIN ranking r
  CROSS JOIN duplicate_quality d CROSS JOIN report_quality q`

const dashboardUserTrafficSQL = `WITH ` + dashboardTrafficClassificationCTE + `,
user_groups AS (
  SELECT a.user_id,u.email::text AS email,
         sum(a.valid_upload)::numeric AS upload_bytes,
         sum(a.valid_download)::numeric AS download_bytes,
         sum(a.valid_upload+a.valid_download)::numeric AS total_bytes,
         count(DISTINCT a.subscription_id)::bigint AS subscription_count,
         count(*)::bigint AS contributing_entry_count,
         max(a.received_at)::timestamptz(6) AS last_report_at
    FROM attributed a
    JOIN users u ON u.tenant_id=a.tenant_id AND u.id=a.user_id
   WHERE a.subscription_id IS NOT NULL
     AND a.user_id IS NOT NULL AND a.valid_upload+a.valid_download > 0
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
       d.duplicate_report_count,q.invalid_report_count,q.invalid_entry_count
  FROM traffic_totals t CROSS JOIN ranking r
  CROSS JOIN duplicate_quality d CROSS JOIN report_quality q`

func (s *Service) DashboardNodeTraffic(ctx context.Context, tenantID string, input DashboardTrafficQuery) (*DashboardNodeTraffic, error) {
	in, parsed, err := validateDashboardTrafficQuery(input)
	if err != nil {
		return nil, err
	}
	out := &DashboardNodeTraffic{Range: in.Range, Basis: "strict_raw_report_entries", Items: []DashboardNodeTrafficItem{}}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		window, err := resolveDashboardWindow(ctx, tx, in, parsed)
		if err != nil {
			return err
		}
		out.SnapshotAt, out.From, out.To = window.snapshotText, window.fromText, window.toText
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, dashboardNodeTrafficSQL, tenantID, window.from, window.to, in.Limit).Scan(
			&raw, &out.Totals.ReportedBytes, &out.Totals.AttributedBytes, &out.Totals.UnattributedBytes,
			&out.Ranking.ReturnedBytes, &out.Ranking.OtherNodeBytes,
			&out.Quality.DuplicateReportCount, &out.Quality.InvalidReportCount, &out.Quality.InvalidEntryCount); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out.Items)
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

func (s *Service) DashboardUserTraffic(ctx context.Context, tenantID string, input DashboardTrafficQuery) (*DashboardUserTraffic, error) {
	in, parsed, err := validateDashboardTrafficQuery(input)
	if err != nil {
		return nil, err
	}
	out := &DashboardUserTraffic{Range: in.Range, Basis: "strict_raw_report_entries", Items: []DashboardUserTrafficItem{}}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		window, err := resolveDashboardWindow(ctx, tx, in, parsed)
		if err != nil {
			return err
		}
		out.SnapshotAt, out.From, out.To = window.snapshotText, window.fromText, window.toText
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, dashboardUserTrafficSQL, tenantID, window.from, window.to, in.Limit).Scan(
			&raw, &out.Totals.ReportedBytes, &out.Totals.AttributedBytes, &out.Totals.UnattributedBytes,
			&out.Ranking.ReturnedBytes, &out.Ranking.OtherUserBytes,
			&out.Quality.DuplicateReportCount, &out.Quality.InvalidReportCount, &out.Quality.InvalidEntryCount); err != nil {
			return err
		}
		return json.Unmarshal(raw, &out.Items)
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
    LEFT JOIN notification_deliveries d ON d.tenant_id=$1
   GROUP BY c.as_of
), lag AS MATERIALIZED (
  SELECT f.*,CASE WHEN ready=0 THEN NULL::interval ELSE as_of-oldest_ready_at END AS lag_exact FROM facts f
)
SELECT to_char(as_of AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
       CASE WHEN lag_exact>interval '600 seconds' THEN 'backlogged' ELSE 'clear' END,
       ready,ready_retry,scheduled,scheduled_retry,sending_unobservable,
       failed_total,suppressed_total,bounced_total,
       CASE WHEN oldest_ready_at IS NULL THEN NULL ELSE to_char(oldest_ready_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
       CASE WHEN ready=0 THEN 0::bigint ELSE ceil(extract(epoch FROM greatest(lag_exact,interval '0 seconds')))::bigint END,
       CASE WHEN last_sent_at IS NULL THEN NULL ELSE to_char(last_sent_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END,
       CASE WHEN ready=0 THEN 'no_due_backlog'
            WHEN lag_exact>interval '600 seconds' THEN 'lag_exceeded'
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
