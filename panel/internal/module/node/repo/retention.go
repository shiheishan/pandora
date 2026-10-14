package repo

import (
	"context"
	"time"
)

// PurgeTrafficReports 调库函数 purge_node_traffic_reports(before, batch)，返回删除行数。
func (r *Repo) PurgeTrafficReports(ctx context.Context, before time.Time, batch int) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw("SELECT purge_node_traffic_reports(?, ?)", before, batch).Scan(&n).Error
	return n, err
}

// PurgeMetrics 调库函数 purge_node_metrics(before, batch)，返回删除行数。
func (r *Repo) PurgeMetrics(ctx context.Context, before time.Time, batch int) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw("SELECT purge_node_metrics(?, ?)", before, batch).Scan(&n).Error
	return n, err
}

// PurgeAliveIPs 删一批 last_seen_at 早于 before 的在线设备行（最多 batch 行）。
func (r *Repo) PurgeAliveIPs(ctx context.Context, before time.Time, batch int) (int64, error) {
	res := r.d.Conn(ctx).Exec(`
		DELETE FROM node_alive_ips a
		 USING (SELECT node_id, subscription_id, ip_hash
		          FROM node_alive_ips
		         WHERE last_seen_at < ?
		         ORDER BY last_seen_at
		         LIMIT ?) d
		 WHERE a.node_id = d.node_id AND a.subscription_id = d.subscription_id AND a.ip_hash = d.ip_hash`,
		before, batch)
	return res.RowsAffected, res.Error
}

// PurgeTrafficHourly 删一批 hour_start 早于 before 的节点小时汇总行（最多 batch 行）。
func (r *Repo) PurgeTrafficHourly(ctx context.Context, before time.Time, batch int) (int64, error) {
	res := r.d.Conn(ctx).Exec(`
		DELETE FROM node_traffic_hourly a
		 USING (SELECT node_id, hour_start
		          FROM node_traffic_hourly
		         WHERE hour_start < ?
		         ORDER BY hour_start
		         LIMIT ?) d
		 WHERE a.node_id = d.node_id AND a.hour_start = d.hour_start`,
		before, batch)
	return res.RowsAffected, res.Error
}

// PurgeUserTrafficHourly 删一批 hour_start 早于 before 的节点×订阅小时汇总行（最多 batch 行）。
func (r *Repo) PurgeUserTrafficHourly(ctx context.Context, before time.Time, batch int) (int64, error) {
	res := r.d.Conn(ctx).Exec(`
		DELETE FROM node_user_traffic_hourly a
		 USING (SELECT hour_start, node_id, subscription_id
		          FROM node_user_traffic_hourly
		         WHERE hour_start < ?
		         ORDER BY hour_start
		         LIMIT ?) d
		 WHERE a.hour_start = d.hour_start AND a.node_id = d.node_id AND a.subscription_id = d.subscription_id`,
		before, batch)
	return res.RowsAffected, res.Error
}
