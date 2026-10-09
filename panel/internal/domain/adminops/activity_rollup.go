package adminops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 后台行为趋势的按天汇总（迁移 00114）。
//
// 逐天口径只有一个出处：app.activity_daily_compute(租户, 起日, 止日)，它是原
// ActivityTimeseries 请求 SQL 的原文（按会话时区切日）。activity_daily 只存已结束的日子，
// 并记下算它时的会话时区；读路径只用 tz 等于当前会话时区、且可用（定稿，或昨天且没有迟到写入，
// 判据见迁移 00114 文件头）的行，其余日子实时经同一个函数算，所以数字与原来逐天现场聚合相同
// （PG18 对照：activity_rollup_pg18_test.go）。

// ActivityDailyRetentionDays 是按天汇总的保留期。读路径最远 90 天（接口上限），留长是为了
// 以后的长周期趋势；更早的行由保留期任务删除。
const ActivityDailyRetentionDays = 400

// activityDailyPurgeBatch 是一次清理最多删的行数（每租户每天一行，正常一次只删一两行）。
const activityDailyPurgeBatch = 1000

// activityDailyNeedSQL 找出最近 2 个已结束日（会话时区的昨天与前天）里要重算的范围，
// 返回 [最早, 最晚]；都不需要时两列为 NULL。一天不需要重算，当且仅当它的行已经「不会再变」：
//   - 定稿：computed_at 不早于 day + 2 的零点（与读路径 activityDailyFinalSQL 同一判据）。那时
//     审计、拉取日志和按日流量都不会再写进这一天，重算只会得到同样的数。前天的行在 day + 2
//     那天第一次运行时写出定稿行，之后不再碰；原先它在这一天剩下的每一轮（共约 144 次）
//     都重算一遍；
//   - 昨天且读路径此刻就在用：算于零点 10 分钟之后、且这一天的按日流量此后没被写过
//     （用户时区晚于会话时区时昨天还会被迟到写入，写停之后的下一轮才可用）。读路径用着的行
//     重算不会改变它读到什么，等到 day + 2 那天再定稿一次。
//
// 迟到写入的吸收不变：昨天的行被迟到写入弄成不可用，下一轮就重算；前天的行还没定稿时在
// day + 2 那天第一轮重算，吸收截至那时的全部写入。
const activityDailyNeedSQL = `
SELECT min(g.day), max(g.day)
  FROM unnest(ARRAY[current_date - 2, current_date - 1]) AS g(day)
 WHERE NOT EXISTS (
         SELECT 1 FROM activity_daily a
          WHERE a.tenant_id = $1 AND a.tz = current_setting('TimeZone') AND a.day = g.day
            AND (a.computed_at >= (g.day + 2)::timestamptz
                 OR (g.day = current_date - 1
                     AND a.computed_at >= (g.day + 1)::timestamptz + interval '10 minutes'
                     AND NOT EXISTS (
                           SELECT 1 FROM subscription_usage_daily u
                            WHERE u.tenant_id = $1 AND u.day = g.day
                              AND u.updated_at > a.computed_at - interval '5 minutes'))))`

// refreshActivityDailySQL 重算 [$2, $3] 这几个已结束日（会话时区）并覆盖写入。$2、$3 来自
// activityDailyNeedSQL：只有前天与昨天两个候选日，所以范围要么是其中一天，要么是连着的两天。
const refreshActivityDailySQL = `
INSERT INTO activity_daily AS a
  (tenant_id, tz, day, registered, logins, orders, unique_ips, active_users, computed_at)
SELECT $1, current_setting('TimeZone'), f.day, f.registered, f.logins, f.orders,
       f.unique_ips, f.active_users, now()
  FROM app.activity_daily_compute($1, $2::date, $3::date) f
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

// activityDailyFinalSQL 读窗口内可用的汇总行。第一列是窗口第一天（与原 SQL 的
// generate_series 起点同一个日界），没有任何可用行时只返回这一列有值的一行。
//
// 可用 = 定稿（算于该日结束整一天之后），或算于该日结束 10 分钟之后、且这一天的按日流量在
// 算它之前 5 分钟以来没被写过（00113 的 (tenant_id, day) 索引让这个判断只读这一天的行）。
// 稳态下只有今天要实时算。
const activityDailyFinalSQL = `
WITH w AS MATERIALIZED (
  SELECT (date_trunc('day', now()) - make_interval(days => $2 - 1))::date AS first_day
)
SELECT w.first_day, a.day, a.registered, a.logins, a.orders, a.unique_ips, a.active_users
  FROM w
  LEFT JOIN activity_daily a
    ON a.tenant_id = $1 AND a.tz = current_setting('TimeZone')
   AND a.day >= w.first_day AND a.day < current_date
   AND (a.computed_at >= (a.day + 2)::timestamptz
        OR (a.computed_at >= (a.day + 1)::timestamptz + interval '10 minutes'
            AND NOT EXISTS (
              SELECT 1 FROM subscription_usage_daily u
               WHERE u.tenant_id = $1 AND u.day = a.day
                 AND u.updated_at > a.computed_at - interval '5 minutes')))
 ORDER BY a.day`

// activityLiveSQL 实时算 [$2, 今天] 的每一天（今天，以及缺行或不可用的日子）。
const activityLiveSQL = `
SELECT to_char(f.day, 'MM-DD'), f.registered, f.logins, f.orders, f.unique_ips, f.active_users
  FROM app.activity_daily_compute($1, $2::date, current_date) f
 ORDER BY f.day`

// RefreshActivityDaily 重算本租户最近 2 个已结束日里还没定稿、读路径也还用不上的行为汇总，返回
// 写入的行数（都已定稿时为 0，只做一次两个主键探测的查询）。由 aegis-admin 的保留期循环每个节拍
// 调用，幂等（覆盖写）。
func (s *Service) RefreshActivityDaily(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var lo, hi *time.Time
		if err := tx.QueryRow(ctx, activityDailyNeedSQL, tenantID).Scan(&lo, &hi); err != nil {
			return err
		}
		if lo == nil || hi == nil {
			return nil
		}
		ct, err := tx.Exec(ctx, refreshActivityDailySQL, tenantID, *lo, *hi)
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

// activityTimeseriesTx 在调用方事务里拼出最近 days 天的趋势：从窗口第一天起连续的可用行直接用，
// 第一个缺口（最晚是今天）及之后的日子实时算。两条语句在同一事务里，now() 相同，日界一致。
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
