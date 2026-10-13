package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// UserOrderStat 是一个用户的订单总数与按币种划分的实付金额汇总。
type UserOrderStat struct {
	Orders int64
	Paid   map[string]decimal.Decimal
}

// UserOrderStats 批量查询用户的订单聚合统计；空输入返回空 map。
func (s *Service) UserOrderStats(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]UserOrderStat, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]UserOrderStat{}, nil
	}
	rows, err := s.repo.UserOrderStatRows(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]UserOrderStat, len(rows))
	for _, r := range rows {
		st := out[r.UserID]
		if st.Paid == nil {
			st.Paid = make(map[string]decimal.Decimal)
		}
		st.Orders += r.Orders
		st.Paid[r.Currency] = r.Paid
		out[r.UserID] = st
	}
	return out, nil
}
