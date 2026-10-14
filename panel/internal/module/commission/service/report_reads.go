package service

import (
	"context"

	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

// PendingWithdrawalSum 是某币种待审核（requested）提现的笔数与金额合计。
type PendingWithdrawalSum struct {
	Currency money.Currency
	Count    int64
	Amount   decimal.Decimal
}

// PendingWithdrawalSums 按币种汇总待审核提现（status = requested），按币种升序；没有的币种不在结果里。看板「需要处理」用。
func (s *Service) PendingWithdrawalSums(ctx context.Context) ([]PendingWithdrawalSum, error) {
	rows, err := s.repo.PendingWithdrawalSums(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PendingWithdrawalSum, 0, len(rows))
	for _, row := range rows {
		cur, err := money.ParseCurrency(row.Currency)
		if err != nil {
			return nil, err
		}
		out = append(out, PendingWithdrawalSum{
			Currency: cur,
			Count:    row.Count,
			Amount:   row.Amount,
		})
	}
	return out, nil
}
