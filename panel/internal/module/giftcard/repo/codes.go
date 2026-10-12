package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/giftcard/model"
)

// CodeQuery 是卡密列表与掩码报表共用的筛选，nil / 空串表示不筛。
type CodeQuery struct {
	TemplateID *uuid.UUID
	BatchID    *uuid.UUID
	Status     string
}

// CodeReportRow 是掩码报表的一行。
type CodeReportRow struct {
	CodeMask     string     `gorm:"column:code_mask"`
	Status       string     `gorm:"column:status"`
	TemplateName string     `gorm:"column:template_name"`
	BatchID      uuid.UUID  `gorm:"column:batch_id"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UsedBy       *uuid.UUID `gorm:"column:used_by"`
	UsedAt       *time.Time `gorm:"column:used_at"`
}

// InsertCodes 批量插入卡密，哈希冲突的行跳过。
func (r *Repo) InsertCodes(ctx context.Context, codes []model.Code) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool)
	if len(codes) == 0 {
		return out, nil
	}
	conn := r.d.Conn(ctx)
	if err := conn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code_hash"}},
		DoNothing: true,
	}).CreateInBatches(&codes, 500).Error; err != nil {
		return nil, mapErr(err)
	}
	ids := make([]uuid.UUID, len(codes))
	for i, c := range codes {
		ids[i] = c.ID
	}
	for i := 0; i < len(ids); i += 1000 {
		end := i + 1000
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[i:end]
		rows, err := conn.Raw("SELECT id FROM gift_card_codes WHERE id IN ?", chunk).Rows()
		if err != nil {
			return nil, mapErr(err)
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// CodeByHash 按哈希读一张码；forUpdate 为真加 FOR UPDATE。
func (r *Repo) CodeByHash(ctx context.Context, hash []byte, forUpdate bool) (model.Code, bool, error) {
	var c model.Code
	err := r.d.Conn(ctx).Raw("SELECT * FROM gift_card_codes WHERE code_hash = ?"+forUpdateSuffix(forUpdate), hash).Scan(&c).Error
	if err != nil {
		return model.Code{}, false, mapErr(err)
	}
	if c.ID == uuid.Nil {
		return model.Code{}, false, nil
	}
	return c, true, nil
}

// CodeByID 按 id 读一张码；forUpdate 为真加 FOR UPDATE。
func (r *Repo) CodeByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.Code, bool, error) {
	var c model.Code
	err := r.d.Conn(ctx).Raw("SELECT * FROM gift_card_codes WHERE id = ?"+forUpdateSuffix(forUpdate), id).Scan(&c).Error
	if err != nil {
		return model.Code{}, false, mapErr(err)
	}
	if c.ID == uuid.Nil {
		return model.Code{}, false, nil
	}
	return c, true, nil
}

// UpdateCodeStatus 带旧状态条件改码的状态。
func (r *Repo) UpdateCodeStatus(ctx context.Context, id uuid.UUID, from, to string, usedBy *uuid.UUID, usedAt *time.Time) (bool, error) {
	cols := map[string]any{
		"status":     to,
		"used_by":    usedBy,
		"used_at":    usedAt,
		"updated_at": now(),
	}
	res := r.d.Conn(ctx).Model(&model.Code{}).Where("id = ? AND status = ?", id, from).Updates(cols)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ListCodes 按筛选分页列卡密。
func (r *Repo) ListCodes(ctx context.Context, q CodeQuery, limit, offset int) ([]model.Code, int64, error) {
	conn := r.d.Conn(ctx)
	where, args := codeScope(q)
	var total int64
	if err := conn.Raw("SELECT count(*) FROM gift_card_codes c WHERE "+where, args...).Scan(&total).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, limit, offset)
	var rows []model.Code
	if err := conn.Raw("SELECT c.* FROM gift_card_codes c WHERE "+where+" ORDER BY c.created_at DESC, c.id DESC LIMIT ? OFFSET ?", listArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	if rows == nil {
		rows = []model.Code{}
	}
	return rows, total, nil
}

// CodeReport 掩码报表：超过 max 时只返回总数。
func (r *Repo) CodeReport(ctx context.Context, q CodeQuery, max int) ([]CodeReportRow, int64, error) {
	conn := r.d.Conn(ctx)
	where, args := codeScope(q)
	var total int64
	if err := conn.Raw("SELECT count(*) FROM gift_card_codes c WHERE "+where, args...).Scan(&total).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	if total > int64(max) {
		return nil, total, nil
	}
	var rows []CodeReportRow
	sql := `SELECT c.code_mask, c.status, t.name AS template_name, c.batch_id, c.expires_at,
		c.created_at, c.used_by, c.used_at
		FROM gift_card_codes c JOIN gift_card_templates t ON t.id = c.template_id
		WHERE ` + where + ` ORDER BY c.created_at DESC, c.id`
	if err := conn.Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, 0, mapErr(err)
	}
	if rows == nil {
		rows = []CodeReportRow{}
	}
	return rows, total, nil
}

func codeScope(q CodeQuery) (clause string, args []any) {
	var parts []string
	if q.TemplateID != nil {
		parts = append(parts, "c.template_id = ?")
		args = append(args, *q.TemplateID)
	}
	if q.BatchID != nil {
		parts = append(parts, "c.batch_id = ?")
		args = append(args, *q.BatchID)
	}
	if strings.TrimSpace(q.Status) != "" {
		parts = append(parts, "c.status = ?")
		args = append(args, q.Status)
	}
	if len(parts) == 0 {
		return "true", nil
	}
	return strings.Join(parts, " AND "), args
}
