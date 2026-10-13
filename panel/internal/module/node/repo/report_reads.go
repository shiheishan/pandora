package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReportNodeCountsRow 是看板节点计数查询的一行聚合结果。
type ReportNodeCountsRow struct {
	Total  int64 `gorm:"column:total"`
	Online int64 `gorm:"column:online"`
}

// ReportNodeCounts 一条 SQL 数出未删除、未退役且 serving_status='active' 的节点总数与在线数（active 且服务器心跳在 onlineSince 之后）。
func (r *Repo) ReportNodeCounts(ctx context.Context, onlineSince time.Time) (ReportNodeCountsRow, error) {
	var out ReportNodeCountsRow
	err := r.d.Conn(ctx).Raw(`
SELECT count(*) AS total,
       count(*) FILTER (WHERE s.last_heartbeat_at >= ?) AS online
  FROM nodes n LEFT JOIN servers s ON s.id = n.server_id
 WHERE n.deleted_at IS NULL AND n.serving_status = 'active'`, onlineSince).Scan(&out).Error
	if err != nil {
		return ReportNodeCountsRow{}, err
	}
	return out, nil
}

// OfflineNodeRow 是失联节点样例查询的一行（含窗口聚合的总数与最长失联秒数）。
type OfflineNodeRow struct {
	ID      uuid.UUID `gorm:"column:id"`
	Name    string    `gorm:"column:name"`
	Total   int64     `gorm:"column:total"`
	Longest *int64    `gorm:"column:longest"`
}

// OfflineNodes 一条 SQL 取 active 但服务器从没心跳或心跳早于 onlineSince 的节点样例。
func (r *Repo) OfflineNodes(ctx context.Context, now, onlineSince time.Time, limit int) ([]OfflineNodeRow, error) {
	var out []OfflineNodeRow
	err := r.d.Conn(ctx).Raw(`
SELECT n.id, coalesce(nullif(n.display_name, ''), n.name) AS name,
       count(*) OVER () AS total,
       floor(extract(epoch FROM ?::timestamptz - min(s.last_heartbeat_at) OVER ()))::bigint AS longest
  FROM nodes n LEFT JOIN servers s ON s.id = n.server_id
 WHERE n.deleted_at IS NULL AND n.serving_status = 'active'
   AND (s.last_heartbeat_at IS NULL OR s.last_heartbeat_at < ?)
 ORDER BY s.last_heartbeat_at ASC NULLS LAST, n.id
 LIMIT ?`, now, onlineSince, limit).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ServedNodeAppliedRow 是在 ServedOnServerCond 上的节点及其已应用配置摘要。
type ServedNodeAppliedRow struct {
	NodeID        uuid.UUID `gorm:"column:node_id"`
	ServerID      uuid.UUID `gorm:"column:server_id"`
	AppliedSHA256 *string   `gorm:"column:applied_config_sha256"`
}

// ServedNodeApplied 一条 SQL 列出应跑在服务器上的节点及 applied_config_sha256。
func (r *Repo) ServedNodeApplied(ctx context.Context) ([]ServedNodeAppliedRow, error) {
	var out []ServedNodeAppliedRow
	err := r.d.Conn(ctx).Raw(`
SELECT n.id AS node_id, n.server_id, n.applied_config_sha256
  FROM nodes n JOIN servers s ON s.id = n.server_id
 WHERE ` + ServedOnServerCond).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TrafficByNodeRow 是窗口内按节点汇总的流量一行。
type TrafficByNodeRow struct {
	NodeID      uuid.UUID `gorm:"column:node_id"`
	Name        string    `gorm:"column:name"`
	DisplayName *string   `gorm:"column:display_name"`
	Upload      int64     `gorm:"column:upload"`
	Download    int64     `gorm:"column:download"`
	ActiveHours int64     `gorm:"column:active_hours"`
	Cells       int64     `gorm:"column:cells"`
	LastAt      time.Time `gorm:"column:last_at"`
}

// TrafficByNode 一条 SQL 汇总 hour_start 落在 [from, to) 的节点流量（仅上下行合计 > 0）。
func (r *Repo) TrafficByNode(ctx context.Context, from, to time.Time) ([]TrafficByNodeRow, error) {
	var out []TrafficByNodeRow
	err := r.d.Conn(ctx).Raw(`
WITH h AS (
  SELECT node_id,
         sum(upload_bytes)::bigint AS upload, sum(download_bytes)::bigint AS download,
         count(*) FILTER (WHERE upload_bytes + download_bytes > 0) AS active_hours,
         max(updated_at) FILTER (WHERE upload_bytes + download_bytes > 0) AS last_at
    FROM node_traffic_hourly
   WHERE hour_start >= ? AND hour_start < ?
   GROUP BY node_id
), c AS (
  SELECT node_id, count(*) AS cells
    FROM node_user_traffic_hourly
   WHERE hour_start >= ? AND hour_start < ? AND upload_bytes + download_bytes > 0
   GROUP BY node_id
)
SELECT h.node_id, n.name, n.display_name, h.upload, h.download, h.active_hours,
       coalesce(c.cells, 0) AS cells, h.last_at
  FROM h JOIN nodes n ON n.id = h.node_id
  LEFT JOIN c ON c.node_id = h.node_id
 WHERE h.upload + h.download > 0`, from, to, from, to).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TrafficBySubscriptionRow 是窗口内按订阅汇总的入账流量一行。
type TrafficBySubscriptionRow struct {
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	Upload         int64     `gorm:"column:upload"`
	Download       int64     `gorm:"column:download"`
	Cells          int64     `gorm:"column:cells"`
	LastAt         time.Time `gorm:"column:last_at"`
}

// TrafficBySubscription 一条 SQL 汇总 hour_start 落在 [from, to) 的订阅流量（仅合计 > 0）。
func (r *Repo) TrafficBySubscription(ctx context.Context, from, to time.Time) ([]TrafficBySubscriptionRow, error) {
	var out []TrafficBySubscriptionRow
	err := r.d.Conn(ctx).Raw(`
SELECT subscription_id,
       sum(upload_bytes)::bigint AS upload, sum(download_bytes)::bigint AS download,
       count(*) FILTER (WHERE upload_bytes + download_bytes > 0) AS cells,
       max(updated_at) FILTER (WHERE upload_bytes + download_bytes > 0) AS last_at
  FROM node_user_traffic_hourly
 WHERE hour_start >= ? AND hour_start < ?
 GROUP BY subscription_id
HAVING sum(upload_bytes + download_bytes) > 0`, from, to).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TrafficQualityRow 是窗口内流量上报质量计数一行。
type TrafficQualityRow struct {
	InvalidReports int64 `gorm:"column:invalid_reports"`
	InvalidEntries int64 `gorm:"column:invalid_entries"`
}

// TrafficQuality 一条 SQL 统计 received_at 落在 [from, to) 的上报留档质量计数。
func (r *Repo) TrafficQuality(ctx context.Context, from, to time.Time) (TrafficQualityRow, error) {
	var out TrafficQualityRow
	err := r.d.Conn(ctx).Raw(`
SELECT count(*) FILTER (WHERE invalid_count > 0) AS invalid_reports,
       coalesce(sum(invalid_count), 0)::bigint AS invalid_entries
  FROM node_traffic_reports
 WHERE received_at >= ? AND received_at < ?`, from, to).Scan(&out).Error
	if err != nil {
		return TrafficQualityRow{}, err
	}
	return out, nil
}
