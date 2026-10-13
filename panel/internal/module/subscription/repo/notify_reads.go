package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// NotifyCandidateRow 是到期/过期类通知扫描的一行。
type NotifyCandidateRow struct {
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	UserID         uuid.UUID `gorm:"column:user_id"`
	PlanID         uuid.UUID `gorm:"column:plan_id"`
	PeriodEnd      time.Time `gorm:"column:period_end"`
}

// QuotaNoticeRow 是流量预警扫描的一行。
type QuotaNoticeRow struct {
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	UserID         uuid.UUID `gorm:"column:user_id"`
	PlanID         uuid.UUID `gorm:"column:plan_id"`
	Consumed       int64     `gorm:"column:consumed"`
	Total          int64     `gorm:"column:total"`
	PeriodStart    time.Time `gorm:"column:period_start"`
}

// ExpiringForNotify 取 status=active、current_period_end 在 (after, until] 内的订阅。
func (r *Repo) ExpiringForNotify(ctx context.Context, after, until time.Time) ([]NotifyCandidateRow, error) {
	var out []NotifyCandidateRow
	err := r.d.Conn(ctx).Raw(`
		SELECT s.id AS subscription_id, s.user_id, s.plan_id, s.current_period_end AS period_end
		  FROM subscriptions s
		 WHERE s.status = 'active'
		   AND s.current_period_end > ?
		   AND s.current_period_end <= ?
		 ORDER BY s.current_period_end, s.id`, after, until).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []NotifyCandidateRow{}, nil
	}
	return out, nil
}

// ExpiredForNotify 取 status=expired、renewal_closed_at 为空、current_period_end 在 (endedAfter, endedUntil] 内的订阅；
// onlyWithoutLive 为 true 时排除名下另有生效订阅的用户。
func (r *Repo) ExpiredForNotify(ctx context.Context, endedAfter, endedUntil time.Time, onlyWithoutLive bool, now time.Time) ([]NotifyCandidateRow, error) {
	var out []NotifyCandidateRow
	err := r.d.Conn(ctx).Raw(`
		SELECT s.id AS subscription_id, s.user_id, s.plan_id, s.current_period_end AS period_end
		  FROM subscriptions s
		 WHERE s.status = 'expired'
		   AND s.renewal_closed_at IS NULL
		   AND s.current_period_end > ?
		   AND s.current_period_end <= ?
		   AND (? = false OR NOT EXISTS (
		         SELECT 1 FROM subscriptions o
		          WHERE o.user_id = s.user_id AND o.id <> s.id
		            AND o.status = 'active' AND o.current_period_end > ?))
		 ORDER BY s.current_period_end, s.id`, endedAfter, endedUntil, onlyWithoutLive, now).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []NotifyCandidateRow{}, nil
	}
	return out, nil
}

// QuotaUsageForNotify 取生效订阅本期限量 traffic.bytes 配额行，用量达到 Total 的 minPercent% 及以上。
// 比较一律用 numeric：consumed 允许超额上报、流量包余量是多行之和，bigint 乘 100 会溢出；Total 封顶到 bigint 上限。
func (r *Repo) QuotaUsageForNotify(ctx context.Context, now time.Time, minPercent int) ([]QuotaNoticeRow, error) {
	var out []QuotaNoticeRow
	err := r.d.Conn(ctx).Raw(`
		WITH usage AS (
			SELECT q.subscription_id, s.user_id, s.plan_id, q.consumed, q.limit_value, q.period_start,
			       q.limit_value::numeric + pk.pack_left AS total
			  FROM quota_balances q
			  JOIN subscriptions s ON s.id = q.subscription_id
			 CROSS JOIN LATERAL (
			       SELECT COALESCE(sum(g.granted_bytes - g.consumed_bytes), 0) AS pack_left
			         FROM traffic_pack_grants g
			        WHERE g.subscription_id = q.subscription_id
			          AND g.consumed_bytes < g.granted_bytes) pk
			 WHERE s.status = 'active'
			   AND s.current_period_end > ?
			   AND q.metric = 'traffic.bytes'
			   AND q.limit_value > 0
			   AND q.period_start <= ?
			   AND (q.period_end IS NULL OR q.period_end > ?)
			   AND q.consumed::numeric * 100 >= q.limit_value::numeric * ?)
		SELECT subscription_id, user_id, plan_id, consumed,
		       LEAST(total, 9223372036854775807)::bigint AS total, period_start
		  FROM usage
		 WHERE consumed::numeric * 100 >= total * ?
		 ORDER BY subscription_id, period_start`, now, now, now, minPercent, minPercent).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []QuotaNoticeRow{}, nil
	}
	return out, nil
}

// LivePlanIDs 取用户名下生效订阅的套餐 id，去重升序。
func (r *Repo) LivePlanIDs(ctx context.Context, userID uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(`
		SELECT DISTINCT plan_id
		  FROM subscriptions
		 WHERE user_id = ?
		   AND status = 'active'
		   AND current_period_end > ?
		 ORDER BY plan_id`, userID, now).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []uuid.UUID{}, nil
	}
	return out, nil
}
