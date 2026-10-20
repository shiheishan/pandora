package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// PurchaseLimits 是购买限制的读取面（属 settings）；*settingssvc.Service 直接满足。每次现查库。
type PurchaseLimits interface {
	PurchaseLimits(ctx context.Context) (settingssvc.PurchaseLimits, error)
}

// UsePurchaseLimits 接上购买限制设置（启动装配时调用，不并发安全）。
func (s *Service) UsePurchaseLimits(p PurchaseLimits) { s.limits = p }

// purchaseGate 在门户建单且配置了限制时拿咨询锁，并在新购时检查订阅数上限。
func (s *Service) purchaseGate(ctx context.Context, d draft, now time.Time) (settingssvc.PurchaseLimits, error) {
	if d.Manual != nil || s.limits == nil {
		return settingssvc.PurchaseLimits{}, nil
	}
	lim, err := s.limits.PurchaseLimits(ctx)
	if err != nil {
		return settingssvc.PurchaseLimits{}, err
	}
	if lim.MaxUnpaidOrders <= 0 && lim.MaxSubscriptions <= 0 {
		return settingssvc.PurchaseLimits{}, nil
	}
	if err := s.repo.LockUserPurchases(ctx, d.UserID); err != nil {
		return settingssvc.PurchaseLimits{}, err
	}
	if d.Kind == KindNew && lim.MaxSubscriptions > 0 {
		if err := s.checkSubscriptionCap(ctx, d.UserID, lim.MaxSubscriptions, now); err != nil {
			return settingssvc.PurchaseLimits{}, err
		}
	}
	return lim, nil
}

// checkSubscriptionCap 检查每人订阅数上限（生效订阅 + 待付新购单）。
func (s *Service) checkSubscriptionCap(ctx context.Context, userID uuid.UUID, max int, now time.Time) error {
	// 先数订单再数订阅：并发结算会在同一事务里把待付新购单改成已付并开出订阅，
	// READ COMMITTED 下两条语句看到的是不同时刻；先订单后订阅最多多算一份（偏严、拒掉），
	// 反过来会少算一份、钻过上限。
	pending, err := s.repo.PendingCounts(ctx, userID, now)
	if err != nil {
		return err
	}
	liveMap, err := s.subs.LiveSubscriptionCounts(ctx, []uuid.UUID{userID}, now)
	if err != nil {
		return err
	}
	live := liveMap[userID]
	have := live + pending.New
	if have >= max {
		return errTooManySubscriptions(max, have)
	}
	return nil
}

// checkUnpaidCap 检查每人同时未支付订单上限（本单已建好、仍须付钱时调用）。
func (s *Service) checkUnpaidCap(ctx context.Context, userID uuid.UUID, max int, now time.Time) error {
	if max <= 0 {
		return nil
	}
	pending, err := s.repo.PendingCounts(ctx, userID, now)
	if err != nil {
		return err
	}
	if pending.All >= max {
		return errTooManyUnpaid(pending.All)
	}
	return nil
}

func errTooManyUnpaid(n int) error {
	return errs.Newf(errs.Conflict, fmt.Sprintf("你还有 %d 笔订单没付款，先付款或等它们失效", n))
}

func errTooManySubscriptions(max, have int) error {
	return errs.Newf(errs.Conflict, fmt.Sprintf("每人最多 %d 份订阅，你已经有 %d 份（含还没付款的新开订单）；可以给已有的续费或换套餐", max, have))
}
