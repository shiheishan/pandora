package repo

import (
	"context"
	"time"
)

// OpenTicketStatsRow 是待客服处理工单的聚合统计（SQL 扫描行）。
type OpenTicketStatsRow struct {
	Count             int64  `gorm:"column:n"`
	HighPriority      int64  `gorm:"column:high"`
	OldestWaitSeconds *int64 `gorm:"column:oldest_wait"`
}

// OpenTicketStats 统计 status 为 open、pending_agent、escalated 的工单；now 用于算最久等待秒数。
func (r *Repo) OpenTicketStats(ctx context.Context, now time.Time) (OpenTicketStatsRow, error) {
	var out OpenTicketStatsRow
	err := r.d.Conn(ctx).Raw(`
SELECT count(*) AS n,
       count(*) FILTER (WHERE t.priority IN ('high', 'urgent')) AS high,
       CASE WHEN min(coalesce(m.last_user_at, t.created_at)) IS NULL THEN NULL
            ELSE greatest(0, floor(extract(epoch FROM ?::timestamptz - min(coalesce(m.last_user_at, t.created_at))))::bigint)
       END AS oldest_wait
  FROM tickets t
  LEFT JOIN LATERAL (
        SELECT max(tm.created_at) AS last_user_at FROM ticket_messages tm
         WHERE tm.ticket_id = t.id AND tm.author_kind = 'user') m ON true
 WHERE t.status IN ('open', 'pending_agent', 'escalated')`, now).Scan(&out).Error
	if err != nil {
		return OpenTicketStatsRow{}, mapErr(err)
	}
	return out, nil
}
