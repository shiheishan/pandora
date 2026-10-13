package repo

import (
	"context"
)

// DatabaseStatsRow 是数据库体积与连接数。
type DatabaseStatsRow struct {
	SizeBytes      int64 `gorm:"column:size_bytes"`
	Connections    int64 `gorm:"column:connections"`
	MaxConnections int64 `gorm:"column:max_connections"`
}

// Ping 执行 SELECT 1 探测数据库可达。
func (r *Repo) Ping(ctx context.Context) error {
	return mapErr(r.d.Conn(ctx).Exec("SELECT 1").Error)
}

// DatabaseStats 读取当前库大小、连接数与 max_connections。
func (r *Repo) DatabaseStats(ctx context.Context) (DatabaseStatsRow, error) {
	var out DatabaseStatsRow
	err := r.d.Conn(ctx).Raw(`
SELECT pg_database_size(current_database()) AS size_bytes,
       (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()) AS connections,
       current_setting('max_connections')::bigint AS max_connections`).Scan(&out).Error
	if err != nil {
		return DatabaseStatsRow{}, mapErr(err)
	}
	return out, nil
}
