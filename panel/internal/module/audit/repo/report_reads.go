package repo

import (
	"context"
	"time"
)

// ActivityDayRow 是站点时区某一天的行为统计查询行。
type ActivityDayRow struct {
	Day        string `gorm:"column:day"`
	Registered int64  `gorm:"column:registered"`
	Logins     int64  `gorm:"column:logins"`
	Orders     int64  `gorm:"column:orders"`
	UniqueIPs  int64  `gorm:"column:unique_ips"`
}

// ActivityByDay 按站点时区的日汇总 [from, to) 内的审计事件；走 audit_events_time_idx。
func (r *Repo) ActivityByDay(ctx context.Context, tz string, from, to time.Time, registered, login, orderCreated string) ([]ActivityDayRow, error) {
	var rows []ActivityDayRow
	err := r.d.Conn(ctx).Raw(`
SELECT to_char((occurred_at AT TIME ZONE ?)::date, 'YYYY-MM-DD') AS day,
       count(*) FILTER (WHERE action = ?) AS registered,
       count(*) FILTER (WHERE action = ? AND outcome = 'success') AS logins,
       count(*) FILTER (WHERE action = ?) AS orders,
       count(DISTINCT ip_hash) AS unique_ips
  FROM audit_events
 WHERE occurred_at >= ? AND occurred_at < ?
 GROUP BY 1
 ORDER BY 1`, tz, registered, login, orderCreated, from, to).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ActivityDayRow{}, nil
	}
	return rows, nil
}
