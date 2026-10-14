package repo

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/commission/model"
)

// InsertTransfer 插入佣金转余额记录。
func (r *Repo) InsertTransfer(ctx context.Context, t *model.Transfer) error {
	if err := checkMoney(t.Amount); err != nil {
		return err
	}
	if err := r.d.Conn(ctx).Create(t).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// TransfersOf 用户最近若干条转余额记录。
func (r *Repo) TransfersOf(ctx context.Context, userID uuid.UUID, limit int) ([]model.Transfer, error) {
	var out []model.Transfer
	err := r.d.Conn(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&out).Error
	return out, mapErr(err)
}

// TransferTotalOf 用户在某币种下累计转余额金额。
func (r *Repo) TransferTotalOf(ctx context.Context, userID uuid.UUID, currency string) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := r.d.Conn(ctx).Raw(
		`SELECT COALESCE(sum(amount), 0) FROM commission_transfers WHERE user_id = ? AND currency = ?`,
		userID, currency,
	).Scan(&total).Error
	return total, mapErr(err)
}
