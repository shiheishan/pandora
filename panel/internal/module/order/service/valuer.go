package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/money"
)

// RemainingValue 是一份订阅在 now 时刻没用完的已付价值（与换套餐同一份折算，向零截断到分）、它的币种，
// 以及换掉这一份时不保留的赠送整天数。giftcard 套餐卡「换掉那一份」用：价值退进余额。
// 本周期没有付费单时金额为 0、币种为空。调用方要在事务里先锁订阅行，读到的基数才不会被并发续费改掉。
// 这份订阅挂着未完结的续费或变更单时回 ErrOpenChange：那张单冻结了同一份剩余价值，先换掉再付那张单会把它退两次
// （结算时另有兜底：订阅周期在建单之后重起过的换套餐单转挂账，见 SettlePayment）。
func (s *Service) RemainingValue(ctx context.Context, subscriptionID uuid.UUID, now time.Time) (decimal.Decimal, money.Currency, int, error) {
	sub, err := s.subs.Get(ctx, subscriptionID, nil)
	if err != nil {
		return decimal.Zero, "", 0, err
	}
	open, err := s.repo.OpenChangeOrderExists(ctx, sub.ID)
	if err != nil {
		return decimal.Zero, "", 0, err
	}
	if open {
		return decimal.Zero, "", 0, ErrOpenChange
	}
	b, err := s.loadBasis(ctx, sub)
	if err != nil {
		return decimal.Zero, "", 0, err
	}
	credit, _ := prorationCredit(b, now)
	return credit, b.Currency, giftedDaysLeft(b, now), nil
}

// HasPaidOrder 判断用户有没有付过钱的订单（已支付或已履约、应付大于 0）。giftcard 的「仅新用户」「仅付费用户」条件用。
func (s *Service) HasPaidOrder(ctx context.Context, userID uuid.UUID) (bool, error) {
	return s.repo.HasPaidOrder(ctx, userID)
}
