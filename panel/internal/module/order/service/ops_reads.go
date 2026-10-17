package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// UserOrderStat 是一个用户的订单总数与实付合计。
type UserOrderStat struct {
	Orders int64
	Paid   decimal.Decimal
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
		out[r.UserID] = UserOrderStat{Orders: r.Orders, Paid: r.Paid}
	}
	return out, nil
}
