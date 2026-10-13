package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/report/model"
)

// AdjustmentRow 是调整列表的一行；Reversed 表示该行已被冲正。
type AdjustmentRow struct {
	ID          uuid.UUID       `gorm:"column:id"`
	Currency    string          `gorm:"column:currency"`
	Amount      decimal.Decimal `gorm:"column:amount"`
	Reason      string          `gorm:"column:reason"`
	EffectiveOn string          `gorm:"column:effective_on"`
	ReversalOf  *uuid.UUID      `gorm:"column:reversal_of"`
	CreatedBy   uuid.UUID       `gorm:"column:created_by"`
	CreatedAt   time.Time       `gorm:"column:created_at"`
	Reversed    bool            `gorm:"column:reversed"`
}

// AdjustmentDayRow 是（币种, 生效日）的调整合计。
type AdjustmentDayRow struct {
	Currency string          `gorm:"column:currency"`
	Day      string          `gorm:"column:day"`
	Amount   decimal.Decimal `gorm:"column:amount"`
}

// AdjustmentTotalRow 是某币种全部调整的合计。
type AdjustmentTotalRow struct {
	Currency string          `gorm:"column:currency"`
	Amount   decimal.Decimal `gorm:"column:amount"`
}

// InsertAdjustment 插入一行调整或冲正，并回填 id、created_at。
func (r *Repo) InsertAdjustment(ctx context.Context, a *model.Adjustment) error {
	row := r.d.Conn(ctx).Raw(`
INSERT INTO revenue_report_adjustments (currency, amount, reason, effective_on, reversal_of, created_by)
VALUES (?, ?, ?, ?::date, ?, ?)
RETURNING id, created_at`,
		a.Currency, a.Amount, a.Reason, a.EffectiveOn, a.ReversalOf, a.CreatedBy,
	).Row()
	if err := row.Scan(&a.ID, &a.CreatedAt); err != nil {
		return mapErr(err)
	}
	return nil
}

// GetAdjustment 按 id 读一行；不存在时 found 为 false。
func (r *Repo) GetAdjustment(ctx context.Context, id uuid.UUID) (model.Adjustment, bool, error) {
	var rows []AdjustmentRow
	err := r.d.Conn(ctx).Raw(`
SELECT id, currency, amount, reason, to_char(effective_on, 'YYYY-MM-DD') AS effective_on,
       reversal_of, created_by, created_at
  FROM revenue_report_adjustments
 WHERE id = ?`, id).Scan(&rows).Error
	if err != nil {
		return model.Adjustment{}, false, mapErr(err)
	}
	if len(rows) == 0 {
		return model.Adjustment{}, false, nil
	}
	r0 := rows[0]
	return model.Adjustment{
		ID:          r0.ID,
		Currency:    r0.Currency,
		Amount:      r0.Amount,
		Reason:      r0.Reason,
		EffectiveOn: r0.EffectiveOn,
		ReversalOf:  r0.ReversalOf,
		CreatedBy:   r0.CreatedBy,
		CreatedAt:   r0.CreatedAt,
	}, true, nil
}

// LockAdjustment 对同一调整行在事务内串行化冲正；只追加表不能 FOR UPDATE，用咨询锁，UNIQUE(reversal_of) 兜底。
func (r *Repo) LockAdjustment(ctx context.Context, id uuid.UUID) error {
	return mapErr(r.d.Conn(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended('report:adjustment:' || ?::text, 0))",
		id.String(),
	).Error)
}

// HasReversal 报告该行是否已有冲正行。
func (r *Repo) HasReversal(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := r.d.Conn(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM revenue_report_adjustments WHERE reversal_of = ?)`,
		id,
	).Scan(&exists).Error
	if err != nil {
		return false, mapErr(err)
	}
	return exists, nil
}

// ListAdjustments 按登记时间倒序列出调整行；currency 为空表示全部币种。
func (r *Repo) ListAdjustments(ctx context.Context, currency string, limit int) ([]AdjustmentRow, error) {
	var out []AdjustmentRow
	err := r.d.Conn(ctx).Raw(`
SELECT a.id, a.currency, a.amount, a.reason, to_char(a.effective_on, 'YYYY-MM-DD') AS effective_on,
       a.reversal_of, a.created_by, a.created_at,
       EXISTS (SELECT 1 FROM revenue_report_adjustments r WHERE r.reversal_of = a.id) AS reversed
  FROM revenue_report_adjustments a
 WHERE (?::text = '' OR a.currency = ?)
 ORDER BY a.created_at DESC, a.id DESC
 LIMIT ?`, currency, currency, limit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []AdjustmentRow{}, nil
	}
	return out, nil
}

// AdjustmentSumsByDay 汇总生效日在 [fromDay, toDay) 内按币种与生效日的调整金额。
func (r *Repo) AdjustmentSumsByDay(ctx context.Context, fromDay, toDay string) ([]AdjustmentDayRow, error) {
	var out []AdjustmentDayRow
	err := r.d.Conn(ctx).Raw(`
SELECT currency, to_char(effective_on, 'YYYY-MM-DD') AS day, sum(amount) AS amount
  FROM revenue_report_adjustments
 WHERE effective_on >= ?::date AND effective_on < ?::date
 GROUP BY currency, effective_on
 ORDER BY currency, effective_on`, fromDay, toDay).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []AdjustmentDayRow{}, nil
	}
	return out, nil
}

// AdjustmentTotals 按币种汇总全部调整金额。
func (r *Repo) AdjustmentTotals(ctx context.Context) ([]AdjustmentTotalRow, error) {
	var out []AdjustmentTotalRow
	err := r.d.Conn(ctx).Raw(`
SELECT currency, sum(amount) AS amount
  FROM revenue_report_adjustments
 GROUP BY currency
 ORDER BY currency`).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []AdjustmentTotalRow{}, nil
	}
	return out, nil
}
