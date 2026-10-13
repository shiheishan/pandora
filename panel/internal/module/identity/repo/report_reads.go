package repo

import (
	"context"
	"time"
)

// ReportUserCountsRow 是看板用户计数查询的一行聚合结果。
type ReportUserCountsRow struct {
	Total         int64 `gorm:"column:total"`
	Active        int64 `gorm:"column:active"`
	CreatedSince  int64 `gorm:"column:created_since"`
	CreatedRecent int64 `gorm:"column:created_recent"`
}

// ReportUserCounts 一条 SQL 数出看板的用户计数。
func (r *Repo) ReportUserCounts(ctx context.Context, since, recentSince time.Time) (ReportUserCountsRow, error) {
	var out ReportUserCountsRow
	err := r.d.Conn(ctx).Raw(`
SELECT count(*) AS total,
       count(*) FILTER (WHERE status = 'active') AS active,
       count(*) FILTER (WHERE created_at >= ?) AS created_since,
       count(*) FILTER (WHERE created_at >= ?) AS created_recent
  FROM users`, since, recentSince).Scan(&out).Error
	if err != nil {
		return ReportUserCountsRow{}, mapErr(err)
	}
	return out, nil
}
