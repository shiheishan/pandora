package repo

import (
	"context"
	"strings"

	"github.com/aegispanel/aegis/internal/module/report/model"
)

// UpsertActivity 按 (tz, day) 批量覆盖写行为汇总。
func (r *Repo) UpsertActivity(ctx context.Context, rows []model.ActivityDaily) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`
INSERT INTO activity_daily (tz, day, registered, logins, orders, unique_ips, active_users, computed_at)
VALUES `)
	args := make([]any, 0, len(rows)*8)
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?::date, ?, ?, ?, ?, ?, ?)")
		args = append(args,
			row.TZ, row.Day, row.Registered, row.Logins, row.Orders, row.UniqueIPs, row.ActiveUsers, row.ComputedAt,
		)
	}
	b.WriteString(`
ON CONFLICT (tz, day) DO UPDATE SET
  registered = EXCLUDED.registered,
  logins = EXCLUDED.logins,
  orders = EXCLUDED.orders,
  unique_ips = EXCLUDED.unique_ips,
  active_users = EXCLUDED.active_users,
  computed_at = EXCLUDED.computed_at`)
	return mapErr(r.d.Conn(ctx).Exec(b.String(), args...).Error)
}

// ActivityRows 读取 tz 下 [fromDay, toDay) 的汇总行，按日子升序。
func (r *Repo) ActivityRows(ctx context.Context, tz, fromDay, toDay string) ([]model.ActivityDaily, error) {
	var out []model.ActivityDaily
	err := r.d.Conn(ctx).Raw(`
SELECT tz, to_char(day, 'YYYY-MM-DD') AS day, registered, logins, orders, unique_ips, active_users, computed_at
  FROM activity_daily
 WHERE tz = ? AND day >= ?::date AND day < ?::date
 ORDER BY day`, tz, fromDay, toDay).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []model.ActivityDaily{}, nil
	}
	return out, nil
}

// PurgeActivity 删除日子早于 beforeDay 的汇总行，一次至多 batch 行。
func (r *Repo) PurgeActivity(ctx context.Context, beforeDay string, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(`
DELETE FROM activity_daily
 WHERE (tz, day) IN (
       SELECT tz, day FROM activity_daily WHERE day < ?::date ORDER BY day LIMIT ?
 )`, beforeDay, batch)
	if res.Error != nil {
		return 0, mapErr(res.Error)
	}
	return int(res.RowsAffected), nil
}
