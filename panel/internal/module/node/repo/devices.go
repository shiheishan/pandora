package repo

// 设备数限制：在线来源按订阅聚合的读数。

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DeviceOnlineRow 是一份订阅在窗口内的在线读数。
type DeviceOnlineRow struct {
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	// Online 是跨节点去重的设备数（count(DISTINCT ip_hash)）
	Online int `gorm:"column:online"`
	// Nodes 是有在线记录的节点数（count(DISTINCT node_id)）
	Nodes      int       `gorm:"column:nodes"`
	LastSeenAt time.Time `gorm:"column:last_seen_at"`
}

type deviceCountRow struct {
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	Online         int       `gorm:"column:online"`
}

// OnlineDeviceOverview 返回窗口内有在线记录的订阅，按跨节点去重设备数降序，同数按最近在线、再按 id，最多 limit 条；走 last_seen_at 索引。没有行时返回空切片。
func (r *Repo) OnlineDeviceOverview(ctx context.Context, since time.Time, limit int) ([]DeviceOnlineRow, error) {
	var rows []DeviceOnlineRow
	err := r.d.Conn(ctx).Raw(`
SELECT subscription_id,
       count(DISTINCT ip_hash) AS online,
       count(DISTINCT node_id) AS nodes,
       max(last_seen_at)       AS last_seen_at
  FROM node_alive_ips
 WHERE last_seen_at > ?
 GROUP BY subscription_id
 ORDER BY online DESC, last_seen_at DESC, subscription_id
 LIMIT ?`, since, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []DeviceOnlineRow{}, nil
	}
	return rows, nil
}

// DeviceCountsAbove 返回窗口内跨节点去重设备数大于 minCount 的订阅 → 设备数。strict 判定用：上限至少 1，超限要 > 上限+宽容，调用方传 宽容+1 作 minCount。没有行时返回空 map。
func (r *Repo) DeviceCountsAbove(ctx context.Context, since time.Time, minCount int) (map[uuid.UUID]int, error) {
	var rows []deviceCountRow
	err := r.d.Conn(ctx).Raw(`
SELECT subscription_id, count(DISTINCT ip_hash) AS online
  FROM node_alive_ips
 WHERE last_seen_at > ?
 GROUP BY subscription_id
HAVING count(DISTINCT ip_hash) > ?`, since, minCount).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		out[row.SubscriptionID] = row.Online
	}
	return out, nil
}

// DeviceCounts 返回给定订阅在窗口内的跨节点去重设备数；没有在线记录的订阅不在结果里。走 (subscription_id, last_seen_at) 索引。subIDs 为空不查库。
func (r *Repo) DeviceCounts(ctx context.Context, subIDs []uuid.UUID, since time.Time) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int)
	if len(subIDs) == 0 {
		return out, nil
	}
	var rows []deviceCountRow
	err := r.d.Conn(ctx).Raw(`
SELECT subscription_id, count(DISTINCT ip_hash) AS online
  FROM node_alive_ips
 WHERE subscription_id IN ? AND last_seen_at > ?
 GROUP BY subscription_id`, subIDs, since).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.SubscriptionID] = row.Online
	}
	return out, nil
}
