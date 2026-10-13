package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
)

// Preferences 读本人改过的偏好项。
func (r *Repo) Preferences(ctx context.Context, userID uuid.UUID) ([]model.Preference, error) {
	out := []model.Preference{}
	if err := r.d.Conn(ctx).Where("user_id = ?", userID).Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.Preference{}, nil
	}
	return out, nil
}

// PreferencesForUsers 批量读一批用户改过的偏好项（一条 SQL）。
func (r *Repo) PreferencesForUsers(ctx context.Context, userIDs []uuid.UUID) ([]model.Preference, error) {
	if len(userIDs) == 0 {
		return []model.Preference{}, nil
	}
	out := []model.Preference{}
	if err := r.d.Conn(ctx).Where("user_id IN ?", userIDs).Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.Preference{}, nil
	}
	return out, nil
}

// UpsertPreference 按主键写一条偏好。
func (r *Repo) UpsertPreference(ctx context.Context, p *model.Preference) error {
	return r.d.Conn(ctx).Exec(
		`INSERT INTO notification_preferences (user_id, category, channel, enabled, updated_at)
		 VALUES (?, ?, ?, ?, now())
		 ON CONFLICT (user_id, category, channel) DO UPDATE
		 SET enabled = EXCLUDED.enabled, updated_at = now()`,
		p.UserID, p.Category, p.Channel, p.Enabled,
	).Error
}
