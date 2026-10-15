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
