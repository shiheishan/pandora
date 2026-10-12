package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/giftcard/model"
)

// BatchRow 是批次视图：批次列（不含 codes_sealed）加模板名与已用、已停用数。
type BatchRow struct {
	ID           uuid.UUID  `gorm:"column:id"`
	TemplateID   uuid.UUID  `gorm:"column:template_id"`
	TemplateName string     `gorm:"column:template_name"`
	Prefix       string     `gorm:"column:prefix"`
	Count        int        `gorm:"column:count"`
	Used         int        `gorm:"column:used"`
	Disabled     int        `gorm:"column:disabled"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	CreatedBy    *uuid.UUID `gorm:"column:created_by"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	ExportedAt   *time.Time `gorm:"column:exported_at"`
	ExportedBy   *uuid.UUID `gorm:"column:exported_by"`
}

// CodeState 是一张码在导出时要的列。
type CodeState struct {
	CodeHash  []byte
	Status    string
	ExpiresAt *time.Time
}

const batchViewSelect = `
	SELECT b.id, b.template_id, t.name AS template_name, b.prefix, b.count,
	       COALESCE(st.used, 0) AS used, COALESCE(st.disabled, 0) AS disabled,
	       b.expires_at, b.created_by, b.created_at, b.exported_at, b.exported_by
	FROM gift_card_batches b
	JOIN gift_card_templates t ON t.id = b.template_id
	LEFT JOIN LATERAL (
		SELECT count(*) FILTER (WHERE c.status = 'used') AS used,
		       count(*) FILTER (WHERE c.status = 'disabled') AS disabled
		FROM gift_card_codes c WHERE c.batch_id = b.id
	) st ON true`

// CreateBatch 插入批次（b.ID 由调用方预先填好 UUIDv7，CodesSealed 非空）。
func (r *Repo) CreateBatch(ctx context.Context, b *model.Batch) error {
	return mapErr(r.d.Conn(ctx).Create(b).Error)
}

// BatchByID 按 id 读批次（含 codes_sealed）；forUpdate 为真加 FOR UPDATE。
func (r *Repo) BatchByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.Batch, bool, error) {
	var b model.Batch
	err := r.d.Conn(ctx).Raw("SELECT * FROM gift_card_batches WHERE id = ?"+forUpdateSuffix(forUpdate), id).Scan(&b).Error
	if err != nil {
		return model.Batch{}, false, mapErr(err)
	}
	if b.ID == uuid.Nil {
		return model.Batch{}, false, nil
	}
	return b, true, nil
}

// BatchView 读单个批次视图。
func (r *Repo) BatchView(ctx context.Context, id uuid.UUID) (BatchRow, bool, error) {
	var row BatchRow
	err := r.d.Conn(ctx).Raw(batchViewSelect+" WHERE b.id = ?", id).Scan(&row).Error
	if err != nil {
		return BatchRow{}, false, mapErr(err)
	}
	if row.ID == uuid.Nil {
		return BatchRow{}, false, nil
	}
	return row, true, nil
}

// ListBatches 分页列出批次视图，可按模板筛选。
func (r *Repo) ListBatches(ctx context.Context, templateID *uuid.UUID, limit, offset int) ([]BatchRow, int64, error) {
	conn := r.d.Conn(ctx)
	where := "true"
	var args []any
	if templateID != nil {
		where = "b.template_id = ?"
		args = append(args, *templateID)
	}
	var total int64
	if err := conn.Raw("SELECT count(*) FROM gift_card_batches b WHERE "+where, args...).Scan(&total).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, limit, offset)
	var rows []BatchRow
	if err := conn.Raw(batchViewSelect+" WHERE "+where+" ORDER BY b.created_at DESC, b.id DESC LIMIT ? OFFSET ?", listArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	if rows == nil {
		rows = []BatchRow{}
	}
	return rows, total, nil
}

// MarkBatchExported 标记批次已导出并清空 codes_sealed。
func (r *Repo) MarkBatchExported(ctx context.Context, id, by uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE gift_card_batches SET exported_at = ?, exported_by = ?, codes_sealed = NULL WHERE id = ? AND exported_at IS NULL`,
		at, by, id,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// CodeStatesByBatch 取一批码的哈希、状态、有效期。
func (r *Repo) CodeStatesByBatch(ctx context.Context, batchID uuid.UUID) ([]CodeState, error) {
	rows, err := r.d.Conn(ctx).Raw(
		`SELECT code_hash, status, expires_at FROM gift_card_codes WHERE batch_id = ? ORDER BY created_at, id`, batchID,
	).Rows()
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []CodeState
	for rows.Next() {
		var s CodeState
		if err := rows.Scan(&s.CodeHash, &s.Status, &s.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}
