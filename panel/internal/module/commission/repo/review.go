package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

// EntryCursor 是待复核列表的键集分页位置（按 created_at DESC, id DESC 排序的上一页最后一行）。
type EntryCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListReviewEntries 列出待复核条目（status pending 且 review_required），按计提时间倒序；after 非空时从它之后接着取。
// 走部分索引 commission_entries_review_idx。
func (r *Repo) ListReviewEntries(ctx context.Context, after *EntryCursor, limit int) ([]model.Entry, error) {
	// 状态写成字面量：与部分索引的条件逐字一致，通用计划下也能用上它（同 DueEntryIDs）
	q := r.d.Conn(ctx).Where("status = 'pending' AND review_required")
	if after != nil {
		q = q.Where("(created_at, id) < (?, ?)", after.CreatedAt, after.ID)
	}
	var out []model.Entry
	err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&out).Error
	return out, mapErr(err)
}

// LockEntry 按 id 锁定一条佣金条目（FOR UPDATE，等锁不跳过）；不存在时 ok 为 false。
func (r *Repo) LockEntry(ctx context.Context, id uuid.UUID) (model.Entry, bool, error) {
	var e model.Entry
	err := r.d.Conn(ctx).Raw(`SELECT * FROM commission_entries WHERE id = ? FOR UPDATE`, id).Scan(&e).Error
	if err != nil {
		return model.Entry{}, false, mapErr(err)
	}
	if e.ID == uuid.Nil {
		return model.Entry{}, false, nil
	}
	return e, true, nil
}

// ApproveEntry 复核通过：清 review_required、记复核人与复核时间，review_reason 留作历史；只改待复核的行。
func (r *Repo) ApproveEntry(ctx context.Context, id, by uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("id = ? AND status = ? AND review_required", id, "pending").
		Updates(map[string]any{
			"review_required": false,
			"reviewed_by":     by,
			"reviewed_at":     at,
			"updated_at":      at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// VoidEntry 复核作废：转 voided、写冲回交易与作废原因、记复核人与复核时间；只改待复核的行。
func (r *Repo) VoidEntry(ctx context.Context, id, voidTxnID uuid.UUID, reason string, by uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Entry{}).
		Where("id = ? AND status = ? AND review_required", id, "pending").
		Updates(map[string]any{
			"status":          "voided",
			"review_required": false,
			"void_txn_id":     voidTxnID,
			"void_reason":     reason,
			"reviewed_by":     by,
			"reviewed_at":     at,
			"updated_at":      at,
		})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
