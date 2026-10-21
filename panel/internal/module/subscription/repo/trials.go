package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// InsertTrial 插入一条注册试用记录。
func (r *Repo) InsertTrial(ctx context.Context, t *model.SubscriptionTrial) error {
	return mapErr(r.d.Conn(ctx).Create(t).Error)
}

// TrialExists 查询用户是否已经领过注册试用。
func (r *Repo) TrialExists(ctx context.Context, userID uuid.UUID) (bool, error) {
	var n int64
	err := r.d.Conn(ctx).Model(&model.SubscriptionTrial{}).Where("user_id = ?", userID).Count(&n).Error
	return n > 0, mapErr(err)
}

// TrialSubscriptionIDs 返回这些用户名下注册试用发的订阅 id（subscription_trials，按主键 user_id 查，一条 SQL）。
func (r *Repo) TrialSubscriptionIDs(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	out := make(map[uuid.UUID]struct{})
	if len(userIDs) == 0 {
		return out, nil
	}
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Model(&model.SubscriptionTrial{}).Where("user_id IN ?", userIDs).Pluck("subscription_id", &ids).Error
	if err != nil {
		return nil, mapErr(err)
	}
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}
