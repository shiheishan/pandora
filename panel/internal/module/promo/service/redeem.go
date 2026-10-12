package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/promo/model"
	"github.com/aegispanel/aegis/internal/module/promo/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// apply 按可用性规则校验券并计算折扣（不含锁券）。
func (s *Service) apply(ctx context.Context, c model.Coupon, t Target) (*Applied, error) {
	if err := checkBasics(c, t, s.now()); err != nil {
		return nil, err
	}
	if c.UserGroupID != nil {
		users, err := s.needUsers()
		if err != nil {
			return nil, err
		}
		gid, err := users.UserGroupID(ctx, t.UserID)
		if err != nil {
			return nil, err
		}
		if gid == nil || *gid != *c.UserGroupID {
			return nil, ErrCouponNotForUser
		}
	}
	plans, err := s.repo.PlanIDsByCoupons(ctx, []uuid.UUID{c.ID})
	if err != nil {
		return nil, err
	}
	if err := checkPlan(plans[c.ID], t.PlanID); err != nil {
		return nil, err
	}
	n, err := s.repo.CountActiveByUser(ctx, c.ID, t.UserID)
	if err != nil {
		return nil, err
	}
	if n >= int64(c.MaxRedemptionsPerUser) {
		return nil, ErrCouponUserLimit
	}
	face := faceOf(c)
	return &Applied{
		CouponID: c.ID,
		Code:     c.Code,
		Face:     face,
		Discount: discountFor(face, c.MaxDiscount, t.Subtotal),
	}, nil
}

// Evaluate 只读试算，不锁券。
func (s *Service) Evaluate(ctx context.Context, code string, t Target) (*Applied, error) {
	code = NormalizeCode(code)
	if code == "" {
		return nil, nil
	}
	if err := checkTarget(t); err != nil {
		return nil, err
	}
	c, ok, err := s.repo.CouponByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrCouponNotFound
	}
	return s.apply(ctx, c, t)
}

// Reserve 在下单事务里占用一张券。
func (s *Service) Reserve(ctx context.Context, code string, t Target, o OrderRef) (*Applied, error) {
	code = NormalizeCode(code)
	if code == "" {
		return nil, nil
	}
	if err := checkTarget(t); err != nil {
		return nil, err
	}
	if err := validateOrderRef(o); err != nil {
		return nil, err
	}

	var applied *Applied
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		c, ok, err := s.repo.LockCouponByCode(ctx, code)
		if err != nil {
			return err
		}
		if !ok {
			return ErrCouponNotFound
		}
		a, err := s.apply(ctx, c, t)
		if err != nil {
			return err
		}
		inc, err := s.repo.IncrementReserved(ctx, c.ID)
		if err != nil {
			return err
		}
		if !inc {
			return ErrCouponUsedUp
		}
		rd := &model.Redemption{
			CouponID:       c.ID,
			UserID:         t.UserID,
			OrderID:        o.ID,
			OrderNo:        strings.TrimSpace(o.No),
			DiscountAmount: a.Discount,
			Currency:       t.Currency,
			Status:         model.RedemptionHeld,
		}
		if err := s.repo.InsertRedemption(ctx, rd); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return ErrOrderHasCoupon
			}
			return err
		}
		applied = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applied, nil
}

func validateOrderRef(o OrderRef) error {
	if o.ID == uuid.Nil {
		return errs.Wrap(errs.Internal, errors.New("promo: order_id 不能为空"))
	}
	no := strings.TrimSpace(o.No)
	if no == "" {
		return errs.Wrap(errs.Internal, errors.New("promo: order_no 不能为空"))
	}
	if utf8.RuneCountInString(no) > 64 {
		return errs.Wrap(errs.Internal, errors.New("promo: order_no 超过 64 字符"))
	}
	return nil
}

// Release 订单取消或超时时释放占用。
func (s *Service) Release(ctx context.Context, orderID uuid.UUID, reason string) error {
	reasonPtr := normalizeReleaseReason(reason)
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if !CanTransition(RedemptionHeld, RedemptionReleased) {
			return errs.Wrap(errs.Internal, errors.New("promo: 非法占用状态转换"))
		}
		couponID, ok, err := s.repo.FinishHeld(ctx, orderID, model.RedemptionReleased, reasonPtr)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotHeld
		}
		dec, err := s.repo.DecrementReserved(ctx, couponID)
		if err != nil {
			return err
		}
		if !dec {
			return errs.Wrap(errs.Internal, errors.New("promo: 券的占用计数对不上"))
		}
		return nil
	})
}

// Capture 支付成功时确认占用。
func (s *Service) Capture(ctx context.Context, orderID uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if !CanTransition(RedemptionHeld, RedemptionCaptured) {
			return errs.Wrap(errs.Internal, errors.New("promo: 非法占用状态转换"))
		}
		couponID, ok, err := s.repo.FinishHeld(ctx, orderID, model.RedemptionCaptured, nil)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotHeld
		}
		moved, err := s.repo.MoveReservedToRedeemed(ctx, couponID)
		if err != nil {
			return err
		}
		if !moved {
			return errs.Wrap(errs.Internal, errors.New("promo: 券的占用计数对不上"))
		}
		return nil
	})
}

// Codes 按券 ID 批量取券码。
func (s *Service) Codes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return s.repo.CouponCodes(ctx, ids)
}

// Preview 门户试算套餐或流量包应付金额。
func (s *Service) Preview(ctx context.Context, userID uuid.UUID, code string, item PreviewItem) (Preview, error) {
	cat, err := s.needCatalog()
	if err != nil {
		return Preview{}, err
	}

	var planID *uuid.UUID
	var cur string
	var amt decimal.Decimal

	if item.PackID != nil {
		cur, amt, err = cat.PackSubtotal(ctx, *item.PackID)
		planID = nil
	} else {
		if item.PlanID == nil || item.PriceID == nil {
			return Preview{}, errs.New(errs.NotFound)
		}
		cur, amt, err = cat.PriceSubtotal(ctx, *item.PlanID, *item.PriceID)
		planID = item.PlanID
	}
	if err != nil {
		return Preview{}, err
	}

	applied, err := s.Evaluate(ctx, code, Target{
		UserID:   userID,
		PlanID:   planID,
		Currency: cur,
		Subtotal: amt,
	})
	if err != nil {
		return Preview{}, err
	}

	discount := decimal.Zero
	if applied != nil {
		discount = applied.Discount
	}
	return Preview{
		Subtotal: amt,
		Discount: discount,
		Payable:  amt.Sub(discount),
		Currency: cur,
		Applied:  applied,
	}, nil
}

func normalizeReleaseReason(reason string) *string {
	r := strings.TrimSpace(reason)
	r = strings.ReplaceAll(r, "\x00", "")
	if r == "" {
		return nil
	}
	if utf8.RuneCountInString(r) > 200 {
		r = string([]rune(r)[:200])
	}
	return &r
}
