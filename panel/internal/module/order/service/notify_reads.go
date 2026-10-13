package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PaidNotice 是一张已履约、挂在订阅上的订单，notify 写「支付成功」通知用。
type PaidNotice struct {
	OrderID        uuid.UUID
	UserID         uuid.UUID
	OrderNo        string
	SubscriptionID uuid.UUID
	FulfilledAt    time.Time
}

// PaidForNotify 按 (FulfilledAt, OrderID) 游标翻页取已履约订单（充值单不在内），取 (afterAt, afterID) 之后的至多 limit 张。
// 支付成功通知不进结算事务：通知出错不该牵连一笔已经收到的钱，由 notify 扫描按游标补写、按订单去重。
func (s *Service) PaidForNotify(ctx context.Context, afterAt time.Time, afterID uuid.UUID, limit int) ([]PaidNotice, error) {
	rows, err := s.repo.PaidForNotify(ctx, afterAt, afterID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]PaidNotice, len(rows))
	for i, r := range rows {
		out[i] = PaidNotice{OrderID: r.ID, UserID: r.UserID, OrderNo: r.OrderNo,
			SubscriptionID: r.SubscriptionID, FulfilledAt: r.FulfilledAt}
	}
	return out, nil
}
