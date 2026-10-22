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

// purchaseGate 在门户建单且配置了限制时拿咨询锁，并在新购时检查份数上限。
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

// subscriptionCap 数 userID 手里的份数并与上限 max 比：手里的份（subscription 的 HeldCountsByUser：生效的加过期但还能续费的）
// + 全部待付新购单（PendingCounts.New，不看付款期限）。达到上限回 refusal（409 subscription_limit_reached），系统错误走 err。
// 建单（checkSubscriptionCap，持建单咨询锁）与落点、报价（subscriptionCapRefusal，不拿锁）共用这一个计数，口径不得分叉。
func (s *Service) subscriptionCap(ctx context.Context, userID uuid.UUID, max int, now time.Time) (*errs.Error, error) {
	// 先数订单再数订阅：并发结算会在同一事务里把待付新购单改成已付并开出订阅，
	// READ COMMITTED 下两条语句看到的是不同时刻；先订单后订阅最多多算一份（偏严、拒掉），
	// 反过来会少算一份、钻过上限。
	pending, err := s.repo.PendingCounts(ctx, userID, now)
	if err != nil {
		return nil, err
	}
	heldMap, err := s.subs.HeldCountsByUser(ctx, []uuid.UUID{userID})
	if err != nil {
		return nil, err
	}
	have := heldMap[userID] + pending.New
	if have >= max {
		return errTooManySubscriptions(max, have, pending.New), nil
	}
	return nil, nil
}

// checkSubscriptionCap 检查每人份数上限（建单路径，调用方已持建单咨询锁）。
func (s *Service) checkSubscriptionCap(ctx context.Context, userID uuid.UUID, max int, now time.Time) error {
	refusal, err := s.subscriptionCap(ctx, userID, max, now)
	if err != nil {
		return err
	}
	if refusal != nil {
		return refusal
	}
	return nil
}

// subscriptionCapRefusal 是读路径（落点、报价）的份数上限判断：不拿建单咨询锁，只看建单时会不会被拒。
// 没装配购买限制或上限为 0（缺省不限）时只读一次设置、不查别的表。
func (s *Service) subscriptionCapRefusal(ctx context.Context, userID uuid.UUID, now time.Time) (*errs.Error, error) {
	if s.limits == nil {
		return nil, nil
	}
	lim, err := s.limits.PurchaseLimits(ctx)
	if err != nil {
		return nil, err
	}
	if lim.MaxSubscriptions <= 0 {
		return nil, nil
	}
	return s.subscriptionCap(ctx, userID, lim.MaxSubscriptions, now)
}

// checkUnpaidCap 检查每人同时未支付订单上限（本单算好应付、插单之前调用，计数里不含本单，所以用 >=）。
// 只数没过付款期限的（PendingCounts.All）：过了期限的单用户已付不了，不该再占他的下单名额。
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

func errTooManySubscriptions(max, have, pendingNew int) *errs.Error {
	msg := fmt.Sprintf("每人最多 %d 份套餐（过期不到 30 天、还能续费的也算），你已经有 %d 份", max, have)
	if pendingNew > 0 {
		msg += fmt.Sprintf("（含 %d 张没付款的新买订单）", pendingNew)
	}
	msg += "。可以给已有的一份续费或换套餐"
	if pendingNew > 0 {
		msg += "，或先付掉、取消没付款的订单"
	}
	return errs.Newf(errs.SubscriptionLimitReached, msg)
}
