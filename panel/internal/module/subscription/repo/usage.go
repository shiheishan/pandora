package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TrafficPeriodAt 取 at 时刻所在的 traffic.bytes 配额周期；没有返回 ok=false。
func (r *Repo) TrafficPeriodAt(ctx context.Context, subID uuid.UUID, at time.Time) (start time.Time, end *time.Time, ok bool, err error) {
	var rows []struct {
		PeriodStart time.Time  `gorm:"column:period_start"`
		PeriodEnd   *time.Time `gorm:"column:period_end"`
	}
	err = r.d.Conn(ctx).Raw(`
		SELECT period_start, period_end
		  FROM quota_balances
		 WHERE subscription_id = ?
		   AND metric = 'traffic.bytes'
		   AND period_start <= ?
		   AND (period_end IS NULL OR period_end > ?)
		 ORDER BY period_start DESC
		 LIMIT 1`, subID, at, at).Scan(&rows).Error
	if err != nil {
		return time.Time{}, nil, false, mapErr(err)
	}
	if len(rows) == 0 {
		return time.Time{}, nil, false, nil
	}
	return rows[0].PeriodStart, rows[0].PeriodEnd, true, nil
}

// UsageDays 读一份订阅 [from, to]（含两端，日期串形如 2026-10-12）的按天用量，键是 YYYY-MM-DD。
func (r *Repo) UsageDays(ctx context.Context, subID uuid.UUID, from, to string) (map[string]int64, error) {
	var rows []struct {
		Date  string `gorm:"column:date"`
		Bytes int64  `gorm:"column:bytes"`
	}
	err := r.d.Conn(ctx).Raw(`
		SELECT to_char(day, 'YYYY-MM-DD') AS date, bytes
		  FROM subscription_usage_daily
		 WHERE subscription_id = ?
		   AND day BETWEEN ?::date AND ?::date`, subID, from, to).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.Date] = row.Bytes
	}
	return out, nil
}
