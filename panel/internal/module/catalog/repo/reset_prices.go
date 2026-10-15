package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// ResetPricesByPlan 列出套餐的全部流量重置价格，按 currency 升序。
func (r *Repo) ResetPricesByPlan(ctx context.Context, planID uuid.UUID) ([]model.PlanResetPrice, error) {
	var rows []model.PlanResetPrice
	err := r.d.Conn(ctx).
		Where("plan_id = ?", planID).
		Order("currency ASC").
		Find(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if rows == nil {
		return []model.PlanResetPrice{}, nil
	}
	return rows, nil
}

// ResetPrice 按套餐与币种读一档流量重置价格；没有行时 found 为 false。
func (r *Repo) ResetPrice(ctx context.Context, planID uuid.UUID, currency string) (model.PlanResetPrice, bool, error) {
	var row model.PlanResetPrice
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM plan_reset_prices WHERE plan_id = ? AND currency = ?", planID, currency,
	).Scan(&row).Error
	if err != nil {
		return model.PlanResetPrice{}, false, mapErr(err)
	}
	if row.PlanID == uuid.Nil {
		return model.PlanResetPrice{}, false, nil
	}
	return row, true, nil
}

// ReplaceResetPrices 整组替换套餐的流量重置价格：先删再插；写前对每个 UnitAmount 做 money.Check。
func (r *Repo) ReplaceResetPrices(ctx context.Context, planID uuid.UUID, rows []model.PlanResetPrice) error {
	for _, row := range rows {
		if err := money.Check(row.UnitAmount); err != nil {
			return err
		}
	}
	tx := r.d.Conn(ctx)
	if err := tx.Exec("DELETE FROM plan_reset_prices WHERE plan_id = ?", planID).Error; err != nil {
		return mapErr(err)
	}
	if len(rows) == 0 {
		return nil
	}
	for i := range rows {
		rows[i].PlanID = planID
	}
	if err := tx.Create(&rows).Error; err != nil {
		return mapErr(err)
	}
	return nil
}
