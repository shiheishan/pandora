package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReportSubscriptionCountsRow 是看板订阅计数的一条 SQL 聚合结果。
type ReportSubscriptionCountsRow struct {
	Live     int64 `gorm:"column:live"`
	Expiring int64 `gorm:"column:expiring"`
	NotLive  int64 `gorm:"column:not_live"`
	NewLive  int64 `gorm:"column:new_live"`
}

// ReportSubscriptionCounts 一条 SQL 数出看板的订阅计数；「生效」与 StateAt 的 StateLive 同一口径。
func (r *Repo) ReportSubscriptionCounts(ctx context.Context, now, expiringBefore, createdSince time.Time) (ReportSubscriptionCountsRow, error) {
	var out ReportSubscriptionCountsRow
	err := r.d.Conn(ctx).Raw(`
		SELECT count(*) FILTER (WHERE live) AS live,
		       count(*) FILTER (WHERE live AND current_period_end < ?) AS expiring,
		       count(*) FILTER (WHERE NOT live) AS not_live,
		       count(*) FILTER (WHERE live AND created_at >= ?) AS new_live
		  FROM (SELECT current_period_end, created_at,
		               (status = 'active' AND current_period_end > ? AND renewal_closed_at IS NULL) AS live
		          FROM subscriptions) s`, expiringBefore, createdSince, now).Scan(&out).Error
	if err != nil {
		return ReportSubscriptionCountsRow{}, mapErr(err)
	}
	return out, nil
}

// SubscriptionOwnerRow 是订阅 id 与主人用户 id 的一行。
type SubscriptionOwnerRow struct {
	ID     uuid.UUID `gorm:"column:id"`
	UserID uuid.UUID `gorm:"column:user_id"`
}

// OwnersBySubscription 批量取订阅的主人；ids 为空时不查库。
func (r *Repo) OwnersBySubscription(ctx context.Context, ids []uuid.UUID) ([]SubscriptionOwnerRow, error) {
	if len(ids) == 0 {
		return []SubscriptionOwnerRow{}, nil
	}
	var out []SubscriptionOwnerRow
	err := r.d.Conn(ctx).Raw(`SELECT id, user_id FROM subscriptions WHERE id IN ?`, ids).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []SubscriptionOwnerRow{}, nil
	}
	return out, nil
}

// ActiveUsersDayRow 是站点时区某一天的活跃用户数一行。
type ActiveUsersDayRow struct {
	Day   string `gorm:"column:day"`
	Users int64  `gorm:"column:users"`
}

// ActiveUsersByDay 数 [from, to) 内每一天的活跃用户（拉取成功或按日用量 > 0，按用户去重）。
func (r *Repo) ActiveUsersByDay(ctx context.Context, tz string, from, to time.Time, fromDay, toDay string) ([]ActiveUsersDayRow, error) {
	var out []ActiveUsersDayRow
	err := r.d.Conn(ctx).Raw(`
		SELECT day, count(DISTINCT user_id) AS users FROM (
		  SELECT to_char((f.fetched_at AT TIME ZONE ?)::date, 'YYYY-MM-DD') AS day, s.user_id
		    FROM subscription_fetch_log f
		    JOIN subscriptions s ON s.id = f.subscription_id
		   WHERE f.result = 'ok' AND f.fetched_at >= ? AND f.fetched_at < ?
		  UNION
		  SELECT to_char(u.day, 'YYYY-MM-DD') AS day, s.user_id
		    FROM subscription_usage_daily u
		    JOIN subscriptions s ON s.id = u.subscription_id
		   WHERE u.bytes > 0 AND u.day >= ?::date AND u.day < ?::date
		) x
		GROUP BY day
		ORDER BY day`, tz, from, to, fromDay, toDay).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []ActiveUsersDayRow{}, nil
	}
	return out, nil
}
