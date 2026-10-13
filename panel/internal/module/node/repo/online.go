package repo

// 在线来源（p5-traffic）：upsert 与给节点列表的在线、流量读数。

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AliveRow 是一条在线来源（JSON 标签给 jsonb_to_recordset 用）。IPHash 是设备键 HMAC-SHA256（InfoHMACNodeDevice）的小写 hex。
type AliveRow struct {
	NodeID         uuid.UUID `json:"node_id"`
	SubscriptionID uuid.UUID `json:"subscription_id"`
	IPHash         string    `json:"ip_hash"`
}

// NodeLive 是一个节点的在线与流量读数。
type NodeLive struct {
	NodeID          uuid.UUID `gorm:"column:node_id"`
	OnlineUsers     int       `gorm:"column:online_users"`
	OnlineIPs       int       `gorm:"column:online_ips"`
	TrafficBytes24h int64     `gorm:"column:traffic_bytes_24h"`
}

// ServerNodeIDs 取给定节点里属于这台服务器的那些（不看删除与在役状态，名单另判）。
func (r *Repo) ServerNodeIDs(ctx context.Context, serverID uuid.UUID, nodeIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err := r.d.Conn(ctx).Raw(`SELECT id FROM nodes WHERE server_id = ? AND id IN ?`, serverID, nodeIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = true
	}
	return out, nil
}

// UpsertAliveIPs 一条语句记一批在线来源。已有行只在 last_seen_at 落后超过 refreshAfter 才刷新：节点每分钟报一次，
// 每次都重写会产生大量非 HOT 更新与 WAL。按 (节点, 订阅, 哈希) 排序插入，同一服务器两份上报并发时加锁顺序一致。
func (r *Repo) UpsertAliveIPs(ctx context.Context, seenAt time.Time, refreshAfter time.Duration, rows []AliveRow) error {
	if len(rows) == 0 {
		return nil
	}
	j, err := jsonArray(rows)
	if err != nil {
		return err
	}
	return r.d.Conn(ctx).Exec(`
		INSERT INTO node_alive_ips AS a (node_id, subscription_id, ip_hash, last_seen_at)
		SELECT c.node_id, c.subscription_id, decode(c.ip_hash, 'hex'), ?
		  FROM jsonb_to_recordset(?::jsonb) AS c(node_id uuid, subscription_id uuid, ip_hash text)
		 ORDER BY c.node_id, c.subscription_id, c.ip_hash
		ON CONFLICT (node_id, subscription_id, ip_hash) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
		 WHERE a.last_seen_at < ?`, seenAt, j, seenAt.Add(-refreshAfter)).Error
}

// NodeLiveStats 按节点批量取在线订阅数、在线 (订阅, 设备键) 数与窗口内流量（一条语句）。没有数据的节点不在结果里。
// 两段都走主键前缀（node_id …）；流量是实际字节（未乘倍率、含名单外），按整点桶，桶起点落在窗口内的才算。
func (r *Repo) NodeLiveStats(ctx context.Context, nodeIDs []uuid.UUID, onlineSince, trafficSince time.Time) ([]NodeLive, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	var out []NodeLive
	err := r.d.Conn(ctx).Raw(`
		WITH a AS (
		  SELECT node_id, count(DISTINCT subscription_id) AS users, count(*) AS ips
		    FROM node_alive_ips
		   WHERE node_id IN ? AND last_seen_at > ?
		   GROUP BY node_id
		), t AS (
		  SELECT node_id, LEAST(sum(upload_bytes::numeric + download_bytes), 9223372036854775807)::bigint AS bytes
		    FROM node_traffic_hourly
		   WHERE node_id IN ? AND hour_start >= ?
		   GROUP BY node_id
		)
		SELECT coalesce(a.node_id, t.node_id) AS node_id,
		       coalesce(a.users, 0) AS online_users, coalesce(a.ips, 0) AS online_ips,
		       coalesce(t.bytes, 0) AS traffic_bytes_24h
		  FROM a FULL JOIN t ON t.node_id = a.node_id`, nodeIDs, onlineSince, nodeIDs, trafficSince).Scan(&out).Error
	return out, err
}
