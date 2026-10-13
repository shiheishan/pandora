// Package repo 是 appearance 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/appearance/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 appearance 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// ErrDuplicate 表示唯一约束冲突。
var ErrDuplicate = errors.New("appearance: 唯一约束冲突")

// mapErr 把 GORM 翻译后的约束错误转成模块哨兵。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}

// ListThemes 列出全部主题。
func (r *Repo) ListThemes(ctx context.Context) ([]model.Theme, error) {
	var rows []model.Theme
	err := r.d.Conn(ctx).Order("is_builtin DESC, code").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Theme{}, nil
	}
	return rows, nil
}

// ActiveTheme 返回当前生效主题；没有时 ok=false。
func (r *Repo) ActiveTheme(ctx context.Context) (model.Theme, bool, error) {
	var t model.Theme
	err := r.d.Conn(ctx).Where("is_active = ?", true).Take(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Theme{}, false, nil
		}
		return model.Theme{}, false, err
	}
	return t, true, nil
}

// ThemeByCode 按 code 查主题。
func (r *Repo) ThemeByCode(ctx context.Context, code string) (model.Theme, bool, error) {
	var t model.Theme
	err := r.d.Conn(ctx).Where("code = ?", code).Take(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Theme{}, false, nil
		}
		return model.Theme{}, false, err
	}
	return t, true, nil
}

// UpsertTheme 按 code 插入或更新 name、tokens、branding；t.ID 会回填。
func (r *Repo) UpsertTheme(ctx context.Context, t *model.Theme) error {
	res := r.d.Conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "tokens", "branding", "updated_at"}),
	}).Create(t)
	return mapErr(res.Error)
}

// DeactivateThemes 关闭全部生效主题。
func (r *Repo) DeactivateThemes(ctx context.Context) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE site_themes SET is_active = false, updated_at = now() WHERE is_active",
	).Error
}

// ActivateTheme 把指定 code 设为生效；未命中时 ok=false。
func (r *Repo) ActivateTheme(ctx context.Context, code string) (uuid.UUID, bool, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(
		`UPDATE site_themes SET is_active = true, updated_at = now() WHERE code = ? RETURNING id`,
		code,
	).Scan(&ids).Error
	if err != nil {
		return uuid.Nil, false, err
	}
	if len(ids) == 0 {
		return uuid.Nil, false, nil
	}
	return ids[0], true, nil
}

// DeleteCustomInactiveTheme 删除非内置且未生效的自定义主题。
func (r *Repo) DeleteCustomInactiveTheme(ctx context.Context, code string) (uuid.UUID, bool, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(
		`DELETE FROM site_themes WHERE code = ? AND NOT is_builtin AND NOT is_active RETURNING id`,
		code,
	).Scan(&ids).Error
	if err != nil {
		return uuid.Nil, false, err
	}
	if len(ids) == 0 {
		return uuid.Nil, false, nil
	}
	return ids[0], true, nil
}

// ListSlots 列出全部插槽行。
func (r *Repo) ListSlots(ctx context.Context) ([]model.Slot, error) {
	var rows []model.Slot
	err := r.d.Conn(ctx).Order("slot_key").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Slot{}, nil
	}
	return rows, nil
}

// EnabledSlots 返回启用且内容非空的插槽。
func (r *Repo) EnabledSlots(ctx context.Context) ([]model.Slot, error) {
	var rows []model.Slot
	err := r.d.Conn(ctx).Where("enabled AND content <> ''").Order("slot_key").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Slot{}, nil
	}
	return rows, nil
}

// UpsertSlot 按 slot_key 插入或更新 content、enabled、updated_by。
func (r *Repo) UpsertSlot(ctx context.Context, s *model.Slot) error {
	res := r.d.Conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "slot_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"content", "enabled", "updated_by", "updated_at"}),
	}).Create(s)
	return mapErr(res.Error)
}
