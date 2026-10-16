package service

import (
	"context"

	"github.com/shopspring/decimal"
)

// PendingWithdrawalSum 是待审核（requested）提现的笔数与金额合计。
type PendingWithdrawalSum struct {
	Count  int64
	Amount decimal.Decimal
}

// PendingWithdrawalSums 汇总待审核提现（status = requested）；没有时为 0。看板「需要处理」用。
func (s *Service) PendingWithdrawalSums(ctx context.Context) (PendingWithdrawalSum, error) {
	row, err := s.repo.PendingWithdrawalSums(ctx)
	if err != nil {
		return PendingWithdrawalSum{}, err
	}
	return PendingWithdrawalSum{
		Count:  row.Count,
		Amount: row.Amount,
	}, nil
}
