package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/promo/model"
	"github.com/aegispanel/aegis/internal/module/promo/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// CreateCoupon 后台新建一张券。
func (s *Service) CreateCoupon(ctx context.Context, code string, spec Spec) (uuid.UUID, string, error) {
	code = NormalizeCode(code)
	if err := validateCode(code); err != nil {
		return uuid.Nil, "", err
	}
	spec, err := validateSpec(spec)
	if err != nil {
		return uuid.Nil, "", err
	}
	if err := s.checkPlans(ctx, spec.PlanIDs); err != nil {
		return uuid.Nil, "", err
	}

	c := couponFromSpec(code, spec)
	c.CreatedBy = actorID(ctx)

	var id uuid.UUID
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		// ON CONFLICT DO NOTHING 判重：撞码不让调用方的事务进入出错状态
		inserted, err := s.repo.InsertCouponIfAbsent(ctx, &c, spec.PlanIDs)
		if err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return errs.Invalid(map[string]string{"user_group_id": "用户组不存在"})
			}
			return err
		}
		if !inserted {
			return ErrCodeExists
		}
		id = c.ID
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionCreated,
			ResourceType: ResourceCoupon,
			ResourceID:   &id,
			Data:         auditFace(code, spec),
		})
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, code, nil
}

// GenerateCoupons 后台按前缀批量生成券。
func (s *Service) GenerateCoupons(ctx context.Context, prefix string, count int, spec Spec) ([]string, error) {
	if count < 1 || count > BatchMax {
		return nil, errs.Invalid(map[string]string{"count": "一次生成 1 到 1000 张，要更多就分批"})
	}
	prefix, err := normalizePrefix(prefix)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, errs.Invalid(map[string]string{"name": "批量生成必须填活动名称，之后要靠它把整批券找回来"})
	}
	spec, err = validateSpec(spec)
	if err != nil {
		return nil, err
	}
	if err := s.checkPlans(ctx, spec.PlanIDs); err != nil {
		return nil, err
	}

	codes := make([]string, 0, count)
	attempts := 0
	maxAttempts := count*10 + 100

	err = s.tx.Do(ctx, func(ctx context.Context) error {
		for len(codes) < count {
			attempts++
			if attempts > maxAttempts {
				return errs.Wrap(errs.Internal, errors.New("promo: 生成券码反复撞码"))
			}
			gen, err := newCode(prefix)
			if err != nil {
				return err
			}
			c := couponFromSpec(gen, spec)
			c.CreatedBy = actorID(ctx)
			inserted, err := s.repo.InsertCouponIfAbsent(ctx, &c, spec.PlanIDs)
			if err != nil {
				if errors.Is(err, repo.ErrForeignKey) {
					return errs.Invalid(map[string]string{"user_group_id": "用户组不存在"})
				}
				return err
			}
			if inserted {
				codes = append(codes, gen)
			}
		}

		data := auditFace("", spec)
		delete(data, "code")
		data["name"] = spec.Name
		data["prefix"] = prefix
		data["count"] = len(codes)
		data["max_redemptions"] = spec.MaxRedemptions

		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionBatchGenerated,
			ResourceType: ResourceCoupon,
			Data:         data,
		})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// SetStatus 后台切换券的 active / paused。
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	if status != StatusActive && status != StatusPaused {
		return errs.Invalid(map[string]string{"status": "状态只能是 active 或 paused"})
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.SetCouponStatus(ctx, id, status)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionStatusChange,
			ResourceType: ResourceCoupon,
			ResourceID:   &id,
			Data:         map[string]any{"status": status},
		})
	})
}

func (s *Service) checkPlans(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	cat, err := s.needCatalog()
	if err != nil {
		return err
	}
	missing, err := cat.MissingPlans(ctx, ids)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return errs.Invalid(map[string]string{"applicable_plan_ids": "有套餐不存在"})
	}
	return nil
}

// ListCoupons 后台分页列出券。
func (s *Service) ListCoupons(ctx context.Context, f ListFilter, limit, offset int) ([]Coupon, int64, error) {
	if f.Status != "" && f.Status != StatusActive && f.Status != StatusPaused {
		return nil, 0, errs.Invalid(map[string]string{"status": "状态只能是 active 或 paused"})
	}
	rows, total, err := s.repo.ListCoupons(ctx, repo.CouponListQuery{
		Query:  strings.TrimSpace(f.Query),
		Status: f.Status,
	}, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	plans, err := s.repo.PlanIDsByCoupons(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Coupon, 0, len(rows))
	for _, row := range rows {
		planIDs := plans[row.ID]
		if planIDs == nil {
			planIDs = []uuid.UUID{}
		}
		out = append(out, Coupon{
			ID:              row.ID,
			Code:            row.Code,
			Name:            row.Name,
			Face:            faceOf(row.Coupon),
			Currency:        row.Currency,
			MaxDiscount:     row.MaxDiscount,
			MinOrder:        row.MinOrderAmount,
			MaxRedemptions:  row.MaxRedemptions,
			MaxPerUser:      row.MaxRedemptionsPerUser,
			Redeemed:        row.RedeemedCount,
			Reserved:        row.ReservedCount,
			PlanIDs:         planIDs,
			UserGroupID:     row.UserGroupID,
			ValidFrom:       row.ValidFrom,
			ValidUntil:      row.ValidUntil,
			Status:          row.Status,
			CreatedAt:       row.CreatedAt,
			DiscountedTotal: row.DiscountedTotal,
		})
	}
	return out, total, nil
}

// Redemptions 后台查单张券的核销明细。
func (s *Service) Redemptions(ctx context.Context, id uuid.UUID) ([]Redemption, error) {
	rows, err := s.repo.RedemptionsByCoupon(ctx, id, RedemptionsLimit)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []Redemption{}, nil
	}

	userSet := make(map[uuid.UUID]struct{})
	for _, row := range rows {
		userSet[row.UserID] = struct{}{}
	}
	emails := map[uuid.UUID]string{}
	if len(userSet) > 0 {
		users, err := s.needUsers()
		if err != nil {
			return nil, err
		}
		ids := make([]uuid.UUID, 0, len(userSet))
		for uid := range userSet {
			ids = append(ids, uid)
		}
		emails, err = users.Emails(ctx, ids)
		if err != nil {
			return nil, err
		}
	}

	out := make([]Redemption, 0, len(rows))
	for _, row := range rows {
		out = append(out, Redemption{
			UserID:   row.UserID,
			Email:    emails[row.UserID],
			OrderNo:  row.OrderNo,
			Discount: row.DiscountAmount,
			Currency: row.Currency,
			At:       row.CreatedAt,
			Released: row.Status == model.RedemptionReleased,
		})
	}
	return out, nil
}

func couponFromSpec(code string, spec Spec) model.Coupon {
	c := model.Coupon{
		Code:                  code,
		Name:                  spec.Name,
		Currency:              spec.Currency,
		MaxDiscount:           spec.MaxDiscount,
		MinOrderAmount:        spec.MinOrder,
		MaxRedemptions:        spec.MaxRedemptions,
		MaxRedemptionsPerUser: spec.MaxPerUser,
		UserGroupID:           spec.UserGroupID,
		ValidFrom:             spec.ValidFrom,
		ValidUntil:            spec.ValidUntil,
		Status:                model.CouponActive,
	}
	switch spec.Face.Kind {
	case KindPercent:
		bp := spec.Face.PercentBP
		c.PercentOffBP = &bp
	case KindFixed:
		amt := spec.Face.AmountOff
		c.AmountOff = &amt
	}
	return c
}

func auditFace(code string, spec Spec) map[string]any {
	m := map[string]any{
		"type": spec.Face.Kind,
	}
	if code != "" {
		m["code"] = code
	}
	if spec.Face.Kind == KindPercent {
		m["value"] = spec.Face.PercentBP
	} else {
		m["value"] = spec.Face.AmountOff.StringFixed(2)
	}
	return m
}
