package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LinkNotifier 由 notify 实现（app 装配适配器）：管理员在后台替用户换链接（AdminRotate）后，在换链接的同一事务里
// 给这份订阅的属主排一条「链接已更换」通知；返回错误时换链接一起回滚。用户自己在门户换（MyRotate）不调：他刚自己做的。
type LinkNotifier interface {
	LinkRotatedByAdmin(ctx context.Context, n LinkRotatedNotice) error
}

// LinkRotatedNotice 是一次后台换链接的通知内容。
type LinkRotatedNotice struct {
	UserID         uuid.UUID
	SubscriptionID uuid.UUID
	PlanID         uuid.UUID
	Label          string    // 备注名，空 = 没起名
	RotatedAt      time.Time // 换链接的时刻（去重键用）
}

// UseLinkNotifier 只在启动装配时调用（不并发安全）。
func (s *Service) UseLinkNotifier(n LinkNotifier) { s.linkNotifier = n }
