package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// EntriesSince 是巡检「佣金挂在没付的订单上」的 commission 这一半（report:integrity 调，只读）：created_at 不早于 since、没作废的条目与订单。
// 条目只由 OnOrderPaid 在订单结算事务里（markPaid 之后）写，订单付了就不会再回到未付（不做退款），
// 所以这些订单都应是 paid / fulfilled；report 再经 order 的 UnsettledAmong 核对。ID 是条目 id。
func (s *Service) EntriesSince(ctx context.Context, since time.Time) ([]integrity.OrderRef, error) {
	return s.repo.EntriesSince(ctx, since)
}
