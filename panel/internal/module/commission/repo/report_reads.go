package repo

import (
	"context"

	"github.com/shopspring/decimal"
)

// PendingWithdrawalSumRow 是某币种待审核提现的笔数与金额合计（SQL 扫描行）。
type PendingWithdrawalSumRow struct {
	Currency string          `gorm:"column:currency"`
	Count    int64           `gorm:"column:n"`
	Amount   decimal.Decimal `gorm:"column:amount"`
}

// PendingWithdrawalSums 按币种汇总 status = requested 的提现，币种升序。
func (r *Repo) PendingWithdrawalSums(ctx context.Context) ([]PendingWithdrawalSumRow, error) {
	var out []PendingWithdrawalSumRow
	err := r.d.Conn(ctx).Raw(
		`SELECT currency, count(*) AS n, sum(amount) AS amount FROM withdrawals WHERE status = 'requested' GROUP BY currency ORDER BY currency`,
	).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
