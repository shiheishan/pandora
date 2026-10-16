package service

import (
	"fmt"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// viewer 是看目录或下单的人：LoggedIn 为假表示匿名；GroupID 是所在用户组（可为 nil）。门户目录与下单共用这一套判断。
type viewer struct {
	LoggedIn bool
	GroupID  *uuid.UUID
}

func groupAllowed(viewerGroup *uuid.UUID, allowed []uuid.UUID) bool {
	if viewerGroup == nil {
		return false
	}
	for _, id := range allowed {
		if id == *viewerGroup {
			return true
		}
	}
	return false
}

// visibleTo 判断套餐是否对该观看者展示（看在售状态与可见性，不看新购/续费开关）。
// hidden 与未知可见性谁都看不到（后台开单不走这里）。
func visibleTo(p Plan, v viewer) bool {
	if p.Status != PlanActive {
		return false
	}
	switch p.Visibility {
	case VisibilityPublic:
		return true
	case VisibilityAuthenticated:
		return v.LoggedIn
	case VisibilityGroup:
		return v.LoggedIn && groupAllowed(v.GroupID, p.VisibleGroupIDs)
	default:
		return false
	}
}

// planRefusal 按用途判断套餐能否使用，nil 表示能用；不可见与不存在统一报 ErrNotFound（不暴露存在与否）。
//   - 续费：只许订阅套餐；归档或关了续费报 ErrRenewalNotAllowed；不看可见性；
//   - 新购、换套餐、叠加：套餐在售、对这个人可见（后台开单跳过可见性），再看各自的开关；
//   - 最后要求有当前发布版本。
func planRefusal(p Plan, use Use, v viewer, manual bool) error {
	switch use {
	case UseRenewal:
		if p.Kind != KindSubscription {
			return ErrChangeNotAllowed
		}
		if p.Status == PlanArchived {
			return ErrRenewalNotAllowed
		}
		if !p.AllowRenewal {
			return ErrRenewalNotAllowed
		}
	case UseNew, UseChange, UseStack:
		if p.Status != PlanActive {
			return ErrNotFound
		}
		if !manual && !visibleTo(p, v) {
			return ErrNotFound
		}
		if (use == UseNew || use == UseStack) && !p.AllowNewPurchase {
			return ErrNewPurchaseClosed
		}
		if use == UseStack && p.Kind != KindOneTime {
			return ErrStackNotAllowed
		}
		if use == UseChange && !p.AllowUpgrade {
			return ErrChangeNotAllowed
		}
	default:
		return errs.Wrap(errs.Internal, fmt.Errorf("catalog: 未知用途 %q", use))
	}
	if p.CurrentVersionID == nil {
		return ErrNoPublishedVersion
	}
	return nil
}

// priceRefusal 按用途判断价格能否使用，nil 表示能用；续费时归档一律报 ErrRenewPriceGone。
func priceRefusal(p Price, use Use) error {
	if p.Status != PriceActive {
		if use == UseRenewal {
			return ErrRenewPriceGone
		}
		return ErrPriceArchived
	}
	return nil
}

// usablePrices 筛出对这次用途有效的在售价格，永不为 nil。
func usablePrices(prices []Price, use Use) []Price {
	out := make([]Price, 0, len(prices))
	for _, p := range prices {
		if priceRefusal(p, use) != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}
