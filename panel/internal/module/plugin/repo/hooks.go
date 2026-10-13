package repo

import (
	"context"
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListHooks 返回全部钩子，按 code 升序。
func (r *Repo) ListHooks(ctx context.Context) ([]model.Hook, error) {
	var rows []model.Hook
	err := r.d.Conn(ctx).Order("code").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Hook{}, nil
	}
	return rows, nil
}

// HookByCode 按 code 取一行；lock 为 true 时加行锁。
func (r *Repo) HookByCode(ctx context.Context, code string, lock bool) (model.Hook, bool, error) {
	q := r.d.Conn(ctx).Where("code = ?", code)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var h model.Hook
	err := q.Take(&h).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Hook{}, false, nil
		}
		return model.Hook{}, false, err
	}
	return h, true, nil
}

// HooksByIDs 按 id 批量取钩子。
func (r *Repo) HooksByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Hook, error) {
	if len(ids) == 0 {
		return []model.Hook{}, nil
	}
	var rows []model.Hook
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Hook{}, nil
	}
	return rows, nil
}

// InsertHook 插入一行钩子。
func (r *Repo) InsertHook(ctx context.Context, h *model.Hook) error {
	return mapErr(r.d.Conn(ctx).Create(h).Error)
}

// UpdateHook 按 id 更新钩子字段；withSecret 为 true 时一并更新 secret_enc。
func (r *Repo) UpdateHook(ctx context.Context, h *model.Hook, withSecret bool) error {
	cols := []string{"name", "description", "enabled", "events", "endpoint_url", "timeout_ms", "max_attempts", "updated_at"}
	if withSecret {
		cols = append(cols, "secret_enc")
	}
	h.UpdatedAt = time.Now()
	res := r.d.Conn(ctx).Model(&model.Hook{}).Where("id = ?", h.ID).Select(cols).Updates(h)
	return mapErr(res.Error)
}

// DeleteHook 按 id 删除钩子（投递由外键 CASCADE 删除）。
func (r *Repo) DeleteHook(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec("DELETE FROM plugin_hooks WHERE id = ?", id).Error
}

// HookStats 按钩子汇总投递计数与最近送达时间。
func (r *Repo) HookStats(ctx context.Context, since time.Time) ([]HookStat, error) {
	var rows []HookStat
	err := r.d.Conn(ctx).Raw(
		`SELECT h.id AS hook_id,
		        (SELECT count(*) FROM plugin_hook_deliveries d WHERE d.hook_id = h.id AND d.status = 'queued') AS queued,
		        (SELECT count(*) FROM plugin_hook_deliveries d WHERE d.hook_id = h.id AND d.status = 'failed' AND d.created_at > ?) AS failed,
		        (SELECT count(*) FROM plugin_hook_deliveries d WHERE d.hook_id = h.id AND d.status = 'sent' AND d.created_at > ?) AS sent,
		        (SELECT max(d.sent_at) FROM plugin_hook_deliveries d WHERE d.hook_id = h.id AND d.status = 'sent') AS last_sent_at
		   FROM plugin_hooks h`,
		since, since).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []HookStat{}, nil
	}
	return rows, nil
}
