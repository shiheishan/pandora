package repo

// 机器指标：按服务器记，只追加。

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// MetricRow 是 node_metrics 的一行（整数口径）。
type MetricRow struct {
	ServerID       uuid.UUID `gorm:"column:server_id"`
	RecordedAt     time.Time `gorm:"column:recorded_at"`
	CPUBp          int32     `gorm:"column:cpu_bp"`
	MemUsedBytes   int64     `gorm:"column:mem_used_bytes"`
	MemTotalBytes  int64     `gorm:"column:mem_total_bytes"`
	DiskUsedBytes  int64     `gorm:"column:disk_used_bytes"`
	DiskTotalBytes int64     `gorm:"column:disk_total_bytes"`
	Load1Cp        int32     `gorm:"column:load1_cp"`
	Load5Cp        int32     `gorm:"column:load5_cp"`
	Load15Cp       int32     `gorm:"column:load15_cp"`
	NetRxBytes     int64     `gorm:"column:net_rx_bytes"`
	NetTxBytes     int64     `gorm:"column:net_tx_bytes"`
	TCPConns       int32     `gorm:"column:tcp_conns"`
	UptimeSec      int64     `gorm:"column:uptime_sec"`
	Partial        bool      `gorm:"column:partial"`
}

// InsertMetric 记一个点；同一服务器同一时刻已有就丢弃（表只追加）。
func (r *Repo) InsertMetric(ctx context.Context, m MetricRow) error {
	return r.d.Conn(ctx).Exec(`
		INSERT INTO node_metrics
		  (server_id, recorded_at, cpu_bp, mem_used_bytes, mem_total_bytes, disk_used_bytes, disk_total_bytes,
		   load1_cp, load5_cp, load15_cp, net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec, partial)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server_id, recorded_at) DO NOTHING`,
		m.ServerID, m.RecordedAt, m.CPUBp, m.MemUsedBytes, m.MemTotalBytes, m.DiskUsedBytes, m.DiskTotalBytes,
		m.Load1Cp, m.Load5Cp, m.Load15Cp, m.NetRxBytes, m.NetTxBytes, m.TCPConns, m.UptimeSec, m.Partial).Error
}

// MetricsSince 取一台服务器 since 之后的点，按时间升序（走主键）。
func (r *Repo) MetricsSince(ctx context.Context, serverID uuid.UUID, since time.Time) ([]MetricRow, error) {
	var out []MetricRow
	err := r.d.Conn(ctx).Raw(`SELECT * FROM node_metrics WHERE server_id = ? AND recorded_at > ? ORDER BY recorded_at`,
		serverID, since).Scan(&out).Error
	return out, err
}

// LatestMetrics 取每台服务器最新一点：每台一次主键倒序取一行，不扫 48 小时的全部点。没有点的服务器不在结果里。
func (r *Repo) LatestMetrics(ctx context.Context, serverIDs []uuid.UUID) ([]MetricRow, error) {
	if len(serverIDs) == 0 {
		return nil, nil
	}
	var out []MetricRow
	err := r.d.Conn(ctx).Raw(`
		SELECT m.* FROM servers s
		 CROSS JOIN LATERAL (
		   SELECT * FROM node_metrics nm WHERE nm.server_id = s.id ORDER BY nm.recorded_at DESC LIMIT 1) m
		 WHERE s.id IN ?`, serverIDs).Scan(&out).Error
	return out, err
}
