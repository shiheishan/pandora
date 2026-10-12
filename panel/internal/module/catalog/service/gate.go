package service

import (
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// viewer 是看目录或下单的人：LoggedIn 为假表示匿名；GroupID 是所在用户组（可为 nil）。门户目录与下单共用这一套判断。
type viewer struct {
	LoggedIn bool
	GroupID  *uuid.UUID
}

func planInVisibleWindow(p Plan, now time.Time) bool {
	if p.VisibleFrom != nil && p.VisibleFrom.After(now) {
		return false
	}
	if p.VisibleUntil != nil && !p.VisibleUntil.After(now) {
		return false
	}
	return true
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

// visibleTo 判断套餐此刻是否对该观看者展示（看在售状态、上架窗口与可见性，不看新购/续费开关）。
// invite_only 的邀请购买没有实现，与 hidden 一样谁都看不到。
func visibleTo(p Plan, v viewer, now time.Time) bool {
	if p.Status != PlanActive {
		return false
	}
	if !planInVisibleWindow(p, now) {
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
//   - 续费：只看 allow_renewal 与是否发布过版本（已买的人续自己的套餐，不看状态与可见性）；
//   - 新购、换套餐目标：套餐在售、对这个人可见（后台开单跳过可见性），再看各自的开关与是否发布过版本。
func planRefusal(p Plan, use Use, v viewer, manual bool, now time.Time) error {
	switch use {
	case UseRenewal:
		if !p.AllowRenewal {
			return ErrRenewalNotAllowed
		}
	case UseNew, UseChange:
		if p.Status != PlanActive {
			return ErrNotFound
		}
		if !manual && !visibleTo(p, v, now) {
			return ErrNotFound
		}
		if use == UseNew && !p.AllowNewPurchase {
			return ErrNewPurchaseClosed
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

// priceRefusal 按用途判断价格能否使用，nil 表示能用。先看用户组（不对这个人开放的组价当不存在，后台开单跳过），
// 再看是否在售、是否在有效期内；续费时这三种一律报 ErrRenewPriceGone（续费页让用户重新选价格档）。
func priceRefusal(p Price, use Use, v viewer, manual bool, now time.Time) error {
	var err error
	switch {
	case !manual && p.UserGroupID != nil && (v.GroupID == nil || *p.UserGroupID != *v.GroupID):
		err = ErrNotFound
	case p.Status != PriceActive:
		err = ErrPriceArchived
	case !priceInWindow(p, now):
		err = ErrPriceOutOfWindow
	}
	if err != nil && use == UseRenewal {
		return ErrRenewPriceGone
	}
	return err
}

// usablePrices 筛出对这次用途与观看者有效的在售价格。
func usablePrices(prices []Price, use Use, v viewer, manual bool, now time.Time) []Price {
	out := make([]Price, 0, len(prices))
	for _, p := range prices {
		if priceRefusal(p, use, v, manual, now) != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}
