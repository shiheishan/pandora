package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// CheckRedemptions 是数据巡检（report:integrity 每 30 分钟调一次，只读）的「礼品卡兑换对不上」：
// 码标了 used 却没有兑换流水，或兑换流水的码不是 used、兑换人与码上的 used_by 不同。
// Redeem 在同一事务里改码状态、发放、写流水，三者缺一说明兑换写了一半（发放的效果是否落地另见各属主的检查：
// 余额卡的流水与账本交易由 gift_card_redemptions_balance_shape 与外键钉住）。样例是码 id。
func (s *Service) CheckRedemptions(ctx context.Context) (integrity.Result, error) {
	rows, err := s.repo.RedemptionMismatchRows(ctx)
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}
