package repo

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// InsertLateCase 插一行；同一笔收款第二个案返回 ErrDuplicate。写前 money.Check(c.Amount)。
func (r *Repo) InsertLateCase(ctx context.Context, c *model.LateCase) error {
	if err := money.Check(c.Amount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(c).Error)
}

// LockLateCase 按 ID 读一行并 FOR UPDATE。
func (r *Repo) LockLateCase(ctx context.Context, id uuid.UUID) (model.LateCase, bool, error) {
	var rows []model.LateCase
	if err := r.d.Conn(ctx).Where("id = ?", id).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Limit(1).Find(&rows).Error; err != nil {
		return model.LateCase{}, false, err
	}
	if len(rows) == 0 {
		return model.LateCase{}, false, nil
	}
	return rows[0], true, nil
}

// ApplyLateCase suspense → applied：applied_txn_id、resolution_reason、resolved_at = now()、updated_at = now()，WHERE id AND status = 'suspense'。返回是否更新到一行。
func (r *Repo) ApplyLateCase(ctx context.Context, id, txnID uuid.UUID, reason string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE late_payment_cases SET status = 'applied', applied_txn_id = ?, resolution_reason = ?, resolved_at = now(), updated_at = now() WHERE id = ? AND status = 'suspense'`,
		txnID, reason, id,
	)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// ListLateCases status 为空串列全部，否则只列该状态；ORDER BY (status = 'suspense') DESC, received_at DESC, id DESC；另返回同条件的总数。
func (r *Repo) ListLateCases(ctx context.Context, status string, limit, offset int) ([]model.LateCase, int64, error) {
	// 计数与取页各用一条新链，不复用 Count 之后的语句
	base := func() *gorm.DB {
		q := r.d.Conn(ctx).Model(&model.LateCase{})
		if status != "" {
			q = q.Where("status = ?", status)
		}
		return q
	}
	var total int64
	if err := base().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []model.LateCase
	order := "(status = 'suspense') DESC, received_at DESC, id DESC"
	if err := base().Order(order).Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	if out == nil {
		out = []model.LateCase{}
	}
	return out, total, nil
}

// PendingLateTotals status = 'suspense' 的待处理金额合计。
func (r *Repo) PendingLateTotals(ctx context.Context) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.d.Conn(ctx).Raw(
		`SELECT coalesce(sum(amount), 0) FROM late_payment_cases WHERE status = 'suspense'`,
	).Row().Scan(&total)
	if err != nil {
		return decimal.Zero, err
	}
	return total, nil
}
