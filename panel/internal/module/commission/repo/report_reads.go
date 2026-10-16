package repo

import (
	"context"

	"github.com/shopspring/decimal"
)

// PendingWithdrawalSumRow 是待审核提现的笔数与金额合计（SQL 扫描行）。
type PendingWithdrawalSumRow struct {
	Count  int64           `gorm:"column:n"`
	Amount decimal.Decimal `gorm:"column:amount"`
}

// PendingWithdrawalSums 汇总 status = requested 的提现笔数与金额。
func (r *Repo) PendingWithdrawalSums(ctx context.Context) (PendingWithdrawalSumRow, error) {
	var out PendingWithdrawalSumRow
	err := r.d.Conn(ctx).Raw(
		`SELECT count(*) AS n, coalesce(sum(amount), 0) AS amount FROM withdrawals WHERE status = 'requested'`,
	).Scan(&out).Error
	if err != nil {
		return PendingWithdrawalSumRow{}, mapErr(err)
	}
	return out, nil
}
