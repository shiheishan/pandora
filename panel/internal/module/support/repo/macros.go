package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/support/model"
)

// ListMacros 全部快捷回复，按 sort_order、created_at、id 升序。
func (r *Repo) ListMacros(ctx context.Context) ([]model.Macro, error) {
	var out []model.Macro
	if err := r.d.Conn(ctx).Order("sort_order, created_at, id").Find(&out).Error; err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		out = []model.Macro{}
	}
	return out, nil
}

// InsertMacro 插入快捷回复，回填 ID 与时间。
func (r *Repo) InsertMacro(ctx context.Context, m *model.Macro) error {
	return mapErr(r.d.Conn(ctx).Create(m).Error)
}

// UpdateMacro 覆盖标题、正文、排序；不存在回 false。
func (r *Repo) UpdateMacro(ctx context.Context, id uuid.UUID, title, body string, sortOrder int) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Macro{}).Where("id = ?", id).Updates(map[string]any{
		"title":      title,
		"body":       body,
		"sort_order": sortOrder,
		"updated_at": now(),
	})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// DeleteMacro 删除并返回原标题；不存在回 false。
func (r *Repo) DeleteMacro(ctx context.Context, id uuid.UUID) (string, bool, error) {
	var rows []struct {
		Title string `gorm:"column:title"`
	}
	if err := r.d.Conn(ctx).Raw("DELETE FROM ticket_macros WHERE id = ? RETURNING title", id).Scan(&rows).Error; err != nil {
		return "", false, mapErr(err)
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Title, true, nil
}
