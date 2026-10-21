package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PendingNotifier 由 notify 实现（app 装配适配器）：后台人工开单选「待用户支付」（SettlementPending）后，在开单的同一事务里
// 给这个用户排一条「有一张待付款订单」通知（订单号、应付、付款期限、去订单页付款）；返回错误时开单一起回滚。
// 赠送（grant）与线下已收款（offline）不调：前者当场开通，后者钱已经收了。
type PendingNotifier interface {
	ManualPendingCreated(ctx context.Context, n PendingNotice) error
}

// PendingNotice 是一张后台开的待付单的通知内容。
type PendingNotice struct {
	UserID    uuid.UUID
	OrderID   uuid.UUID
	PlanID    uuid.UUID
	OrderNo   string
	Amount    decimal.Decimal // 应付（payable_amount）
	ExpiresAt time.Time       // 付款期限，过了自动取消
}

// UsePendingNotifier 只在启动装配时调用（不并发安全）。
func (s *Service) UsePendingNotifier(n PendingNotifier) { s.pending = n }
