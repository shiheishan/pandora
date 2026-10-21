package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/settings/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListSwitches 按 code 升序返回全部开关。
func (r *Repo) ListSwitches(ctx context.Context) ([]model.FeatureSwitch, error) {
	var out []model.FeatureSwitch
	if err := r.d.Conn(ctx).Order("code").Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.FeatureSwitch{}, nil
	}
	return out, nil
}

// GetSwitch 读一个开关；缺行 ok 为 false。
func (r *Repo) GetSwitch(ctx context.Context, code string) (model.FeatureSwitch, bool, error) {
	return r.getSwitch(ctx, code, false)
}

// LockSwitch 同 GetSwitch，并加 FOR UPDATE。
func (r *Repo) LockSwitch(ctx context.Context, code string) (model.FeatureSwitch, bool, error) {
	return r.getSwitch(ctx, code, true)
}

func (r *Repo) getSwitch(ctx context.Context, code string, lock bool) (model.FeatureSwitch, bool, error) {
	q := r.d.Conn(ctx).Where("code = ?", code).Limit(1)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []model.FeatureSwitch
	if err := q.Find(&rows).Error; err != nil {
		return model.FeatureSwitch{}, false, err
	}
	if len(rows) == 0 {
		return model.FeatureSwitch{}, false, nil
	}
	return rows[0], true, nil
}

// UpdateSwitch 改 enabled、reason、changed_by。
func (r *Repo) UpdateSwitch(ctx context.Context, code string, enabled bool, reason *string, by *uuid.UUID) error {
	return r.d.Conn(ctx).Model(&model.FeatureSwitch{}).Where("code = ?", code).Updates(map[string]any{
		"enabled":    enabled,
		"reason":     reason,
		"changed_by": by,
		"updated_at": gorm.Expr("now()"),
	}).Error
}
