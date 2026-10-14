package repo

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

// OverrideOf 读某人的单独佣金设置；没有时 ok 为 false。
func (r *Repo) OverrideOf(ctx context.Context, userID uuid.UUID) (model.Override, bool, error) {
	var o model.Override
	err := r.d.Conn(ctx).Raw(
		`SELECT * FROM commission_user_overrides WHERE user_id = ?`,
		userID,
	).Scan(&o).Error
	if err != nil {
		return model.Override{}, false, mapErr(err)
	}
	if o.UserID == uuid.Nil {
		return model.Override{}, false, nil
	}
	return o, true, nil
}

// UpsertOverride 写入或覆盖某人的单独设置（INSERT … ON CONFLICT (user_id) DO UPDATE 改 rate_percent、scope、updated_by、updated_at）。
func (r *Repo) UpsertOverride(ctx context.Context, o *model.Override) error {
	if err := r.d.Conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"rate_percent", "scope", "updated_by", "updated_at"}),
	}).Create(o).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// LockOverride 锁住某人的单独设置行（FOR UPDATE）；没有时 ok 为 false。改与删之前取旧值写审计用。
func (r *Repo) LockOverride(ctx context.Context, userID uuid.UUID) (model.Override, bool, error) {
	var o model.Override
	err := r.d.Conn(ctx).Raw(
		`SELECT * FROM commission_user_overrides WHERE user_id = ? FOR UPDATE`,
		userID,
	).Scan(&o).Error
	if err != nil {
		return model.Override{}, false, mapErr(err)
	}
	if o.UserID == uuid.Nil {
		return model.Override{}, false, nil
	}
	return o, true, nil
}

// DeleteOverride 删除某人的单独设置，返回是否删到了。
func (r *Repo) DeleteOverride(ctx context.Context, userID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Where("user_id = ?", userID).Delete(&model.Override{})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ListOverrides 按更新时间倒序列出最多 limit 条。
func (r *Repo) ListOverrides(ctx context.Context, limit int) ([]model.Override, error) {
	var out []model.Override
	err := r.d.Conn(ctx).
		Order("updated_at DESC, user_id").
		Limit(limit).
		Find(&out).Error
	return out, mapErr(err)
}
