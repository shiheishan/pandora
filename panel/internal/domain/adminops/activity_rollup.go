package adminops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 后台行为趋势的按天汇总（迁移 00107）。
//
// 逐天口径只有一个出处：app.activity_daily_compute(租户, 起日, 止日)，它是原
// ActivityTimeseries 请求 SQL 的原文（按会话时区切日）。activity_daily 只存已结束的日子，
// 并记下算它时的会话时区；读路径只用 tz 等于当前会话时区、且已定稿的行，其余日子实时经
// 同一个函数算，所以数字与原来逐天现场聚合相同（PG18 对照：activity_rollup_pg18_test.go）。

// ActivityDailyRetentionDays 是按天汇总的保留期。读路径最远 90 天（接口上限），留长是为了
// 以后的长周期趋势；更早的行由保留期任务删除。
const ActivityDailyRetentionDays = 400

// activityDailyPurgeBatch 是一次清理最多删的行数（每租户每天一行，正常一次只删一两行）。
const activityDailyPurgeBatch = 1000

// refreshActivityDailySQL 重算最近 2 个已结束日（会话时区的昨天与前天）并覆盖写入。
// 前天那一行在当天第一次运行后即定稿（computed_at 不早于 day + 2 的零点）。
const refreshActivityDailySQL = `
INSERT INTO activity_daily AS a
  (tenant_id, tz, day, registered, logins, orders, unique_ips, active_users, computed_at)
SELECT $1, current_setting('TimeZone'), f.day, f.registered, f.logins, f.orders,
       f.unique_ips, f.active_users, now()
  FROM app.activity_daily_compute($1, current_date - 2, current_date - 1) f
ON CONFLICT (tenant_id, tz, day) DO UPDATE SET
  registered   = EXCLUDED.registered,
  logins       = EXCLUDED.logins,
  orders       = EXCLUDED.orders,
  unique_ips   = EXCLUDED.unique_ips,
  active_users = EXCLUDED.active_users,
  computed_at  = EXCLUDED.computed_at`

const purgeActivityDailySQL = `
DELETE FROM activity_daily a
 USING (SELECT tenant_id, tz, day
          FROM activity_daily
         WHERE tenant_id = $1 AND day < current_date - $2::int
         ORDER BY day
         LIMIT $3) d
 WHERE a.tenant_id = d.tenant_id AND a.tz = d.tz AND a.day = d.day`

// activityDailyFinalSQL 读窗口内已定稿的汇总行。第一列是窗口第一天（与原 SQL 的
// generate_series 起点同一个日界），没有任何定稿行时只返回这一列有值的一行。
const activityDailyFinalSQL = `
WITH w AS MATERIALIZED (
  SELECT (date_trunc('day', now()) - make_interval(days => $2 - 1))::date AS first_day
)
SELECT w.first_day, a.day, a.registered, a.logins, a.orders, a.unique_ips, a.active_users
  FROM w
  LEFT JOIN activity_daily a
    ON a.tenant_id = $1 AND a.tz = current_setting('TimeZone')
   AND a.day >= w.first_day AND a.day < current_date
   AND a.computed_at >= (a.day + 2)::timestamptz
 ORDER BY a.day`

// activityLiveSQL 实时算 [$2, 今天] 的每一天（今天、昨天与缺定稿行的日子）。
const activityLiveSQL = `
SELECT to_char(f.day, 'MM-DD'), f.registered, f.logins, f.orders, f.unique_ips, f.active_users
  FROM app.activity_daily_compute($1, $2::date, current_date) f
 ORDER BY f.day`

// RefreshActivityDaily 重算本租户最近 2 个已结束日的行为汇总，返回写入的行数。由 aegis-admin
// 的保留期任务定时调用，幂等（覆盖写）。
func (s *Service) RefreshActivityDaily(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, refreshActivityDailySQL, tenantID)
		n = ct.RowsAffected()
		return err
	})
	return n, err
}

// PurgeActivityDaily 删除本租户超出保留期（400 天）的行为汇总，每次最多一批，返回删除的行数。
func (s *Service) PurgeActivityDaily(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, purgeActivityDailySQL, tenantID, ActivityDailyRetentionDays, activityDailyPurgeBatch)
		n = ct.RowsAffected()
		return err
	})
	return n, err
}

// activityTimeseriesTx 在调用方事务里拼出最近 days 天的趋势：从窗口第一天起连续的定稿行直接用，
// 第一个缺口（最晚是昨天）及之后的日子实时算。两条语句在同一事务里，now() 相同，日界一致。
func activityTimeseriesTx(ctx context.Context, tx pgx.Tx, tenantID string, days int) ([]TimeseriesPoint, error) {
	out := []TimeseriesPoint{}
	rows, err := tx.Query(ctx, activityDailyFinalSQL, tenantID, days)
	if err != nil {
		return nil, err
	}
	var next time.Time // 下一个应当出现的日子；第一个缺口就是实时部分的起点
	first := true
	stopped := false
	for rows.Next() {
		var firstDay time.Time
		var day *time.Time
		var registered, logins, orders, uniqueIPs *int64
		var active *int32
		if err := rows.Scan(&firstDay, &day, &registered, &logins, &orders, &uniqueIPs, &active); err != nil {
			rows.Close()
			return nil, err
		}
		if first {
			next, first = firstDay, false
		}
		if stopped || day == nil || !day.Equal(next) {
			stopped = true
			continue
		}
		out = append(out, TimeseriesPoint{
			Day: day.Format("01-02"), Registered: int(*registered), Logins: int(*logins), Orders: int(*orders),
			UniqueIPs: int(*uniqueIPs), ActiveUsers: int(*active),
		})
		next = next.AddDate(0, 0, 1)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	live, err := tx.Query(ctx, activityLiveSQL, tenantID, next)
	if err != nil {
		return nil, err
	}
	defer live.Close()
	for live.Next() {
		var p TimeseriesPoint
		if err := live.Scan(&p.Day, &p.Registered, &p.Logins, &p.Orders, &p.UniqueIPs, &p.ActiveUsers); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, live.Err()
}
