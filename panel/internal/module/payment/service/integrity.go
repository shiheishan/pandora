package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// SettledPaymentsSince 是巡检「收款了订单却没付」的 payment 这一半（report:integrity 调，只读）：paid_at 不早于 since、
// 没进挂账的收款与它的订单。收款行与 Settler 结算订单在同一事务（结算失败整笔回滚、订单不收就同事务落挂账案），
// 所以这些订单都应是 paid / fulfilled；report 再经 order 的 UnsettledAmong 核对。ID 是收款 id。
func (s *Service) SettledPaymentsSince(ctx context.Context, since time.Time) ([]integrity.OrderRef, error) {
	return s.repo.SettledPaymentsSince(ctx, since)
}
