package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// liveCountRow 是 LiveSubscriptionCounts 查询的一行聚合结果。
type liveCountRow struct {
	UserID uuid.UUID
	N      int
}

// LiveSubscriptionCounts 数每个用户在 now 时刻生效中的订阅份数（口径同 service.StateAt 的 StateLive：
// status='active'、current_period_end > now、没关续费窗口）。一条 SQL，走 subscriptions_user_idx；没有生效订阅的用户不在结果里。
func (r *Repo) LiveSubscriptionCounts(ctx context.Context, userIDs []uuid.UUID, now time.Time) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(userIDs) == 0 {
		return out, nil
	}
	var rows []liveCountRow
	err := r.d.Conn(ctx).Raw(
		`SELECT user_id, count(*) AS n FROM subscriptions
		  WHERE user_id IN ? AND status = 'active' AND current_period_end > ? AND renewal_closed_at IS NULL
		  GROUP BY user_id`,
		userIDs, now,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.UserID] = row.N
	}
	return out, nil
}
