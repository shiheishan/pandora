package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ActivePlanVersionIDs 一条 SQL 取指定版本里有生效订阅的版本 id（去重）：
// status = 'active' 且 current_period_end > now。
func (r *Repo) ActivePlanVersionIDs(ctx context.Context, versionIDs []uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	if len(versionIDs) == 0 {
		return []uuid.UUID{}, nil
	}
	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(
		`SELECT DISTINCT plan_version_id FROM subscriptions WHERE plan_version_id IN ? AND status = 'active' AND current_period_end > ?`,
		versionIDs, now,
	).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []uuid.UUID{}, nil
	}
	return out, nil
}
