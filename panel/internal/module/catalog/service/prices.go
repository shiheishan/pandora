package service

import (
	"context"
	"errors"
	"sort"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// CreatePrice 给套餐加一档价格。
func (s *Service) CreatePrice(ctx context.Context, planID uuid.UUID, in PriceInput) (Price, error) {
	if err := validatePriceInput(&in); err != nil {
		return Price{}, err
	}
	var out Price
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		p, err := s.createPriceTx(ctx, planID, in)
		if err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, catalogErr(err)
}

// createPriceTx 在调用方事务里插入价格并写审计。
func (s *Service) createPriceTx(ctx context.Context, planID uuid.UUID, in PriceInput) (Price, error) {
	plan, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return Price{}, err
	}
	if !found {
		return Price{}, ErrNotFound
	}
	if plan.Status == PlanArchived {
		return Price{}, errs.Newf(errs.Conflict, "已归档套餐不能新增价格")
	}

	m := &model.Price{
		PlanID:          planID,
		Currency:        in.Currency,
		UnitAmount:      in.UnitAmount,
		BillingInterval: in.BillingInterval,
		IntervalCount:   in.IntervalCount,
		TrialDays:       in.TrialDays,
		Status:          PriceActive,
		UserGroupID:     in.UserGroupID,
		ValidFrom:       in.ValidFrom,
		ValidUntil:      in.ValidUntil,
	}
	if err := s.repo.InsertPrice(ctx, m); err != nil {
		if errors.Is(err, repo.ErrForeignKey) {
			return Price{}, errs.Invalid(map[string]string{"user_group_id": "用户组不存在"})
		}
		if errors.Is(err, repo.ErrDuplicate) {
			return Price{}, errs.Newf(errs.Conflict, "同一周期与币种已有在售价格，请先下架旧价")
		}
		return Price{}, err
	}
	s.catalogChanged(ctx, rtTablePrices, realtime.OpInsert, m.ID)

	row, found, err := s.repo.PriceByID(ctx, m.ID, repo.NoLock)
	if err != nil {
		return Price{}, err
	}
	if !found {
		return Price{}, ErrNotFound
	}
	out := priceFromModel(row)

	var ug any
	if in.UserGroupID != nil {
		ug = in.UserGroupID.String()
	}
	if err := s.writeAudit(ctx, ActionPriceCreate, ResourcePrice, out.ID, map[string]any{
		"after": map[string]any{
			"plan_id":          planID,
			"currency":         in.Currency,
			"unit_amount":      in.UnitAmount.StringFixed(2),
			"billing_interval": in.BillingInterval,
			"interval_count":   in.IntervalCount,
			"user_group_id":    ug,
		},
	}); err != nil {
		return Price{}, err
	}
	return out, nil
}

// ArchivePrice 下架一档价格，返回新的 row_version。
func (s *Service) ArchivePrice(ctx context.Context, planID, priceID uuid.UUID, expected int64) (int64, error) {
	if expected <= 0 {
		return 0, errs.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	var next int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.PriceByID(ctx, priceID, repo.ForUpdate)
		if err != nil {
			return err
		}
		if !found || cur.PlanID != planID {
			return ErrNotFound
		}
		if cur.RowVersion != expected {
			return rowConflict("价格", cur.RowVersion)
		}
		if !CanTransitionPrice(cur.Status, PriceArchived) {
			return errs.Newf(errs.Conflict, "价格已经归档")
		}
		ok, err := s.repo.ArchivePrice(ctx, priceID, expected)
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("价格", cur.RowVersion)
		}
		s.catalogChanged(ctx, rtTablePrices, realtime.OpUpdate, priceID)
		next = expected + 1
		return s.writeAudit(ctx, ActionPriceArchive, ResourcePrice, priceID, map[string]any{
			"before": map[string]any{"status": cur.Status, "row_version": expected},
			"after":  map[string]any{"status": PriceArchived, "row_version": next},
		})
	})
	return next, catalogErr(err)
}

// syncPublicPricesTx 让清单币种内的在售公开价与 wanted 一致，返回改动条数。
func (s *Service) syncPublicPricesTx(ctx context.Context, planID uuid.UUID, wanted map[string]WizardPrice) (int, error) {
	currencies := priceSyncCurrencies(wanted)
	if len(currencies) == 0 {
		return 0, nil
	}

	plan, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrNotFound
	}
	if plan.Status == PlanArchived {
		return 0, errs.Newf(errs.Conflict, "已归档套餐不能更新价格")
	}

	locked, err := s.repo.LockActivePublicPrices(ctx, planID, currencies)
	if err != nil {
		return 0, err
	}
	have := make(map[string]model.Price, len(locked))
	for _, row := range locked {
		k := wizardKey(row.BillingInterval, row.IntervalCount, row.Currency)
		have[k] = row
	}

	changes := 0

	hkeys := make([]string, 0, len(have))
	for k := range have {
		hkeys = append(hkeys, k)
	}
	sort.Strings(hkeys)
	for _, k := range hkeys {
		old := have[k]
		w, keep := wanted[k]
		if keep && priceRowMatchesWizard(old, w) {
			continue
		}
		ok, err := s.repo.ArchivePrice(ctx, old.ID, old.RowVersion)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, rowConflict("价格", old.RowVersion)
		}
		s.catalogChanged(ctx, rtTablePrices, realtime.OpUpdate, old.ID)
		changes++
	}

	wkeys := make([]string, 0, len(wanted))
	for k := range wanted {
		wkeys = append(wkeys, k)
	}
	sort.Strings(wkeys)
	for _, k := range wkeys {
		w := wanted[k]
		old, exists := have[k]
		if exists && priceRowMatchesWizard(old, w) {
			continue
		}
		pin := wizardPriceInput(w)
		m := &model.Price{
			PlanID:          planID,
			Currency:        pin.Currency,
			UnitAmount:      pin.UnitAmount,
			BillingInterval: pin.BillingInterval,
			IntervalCount:   pin.IntervalCount,
			TrialDays:       pin.TrialDays,
			Status:          PriceActive,
		}
		if err := s.repo.InsertPrice(ctx, m); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return 0, errs.Newf(errs.Conflict, "同一周期与币种已有在售价格，请先下架旧价")
			}
			return 0, err
		}
		s.catalogChanged(ctx, rtTablePrices, realtime.OpInsert, m.ID)
		changes++
	}

	if changes == 0 {
		return 0, nil
	}
	if err := s.writeAudit(ctx, ActionPriceSync, ResourcePlan, planID, map[string]any{
		"after": map[string]any{"changes": changes, "currencies": currencies},
	}); err != nil {
		return 0, err
	}
	return changes, nil
}

func priceSyncCurrencies(wanted map[string]WizardPrice) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range wanted {
		if !seen[p.Currency] {
			seen[p.Currency] = true
			out = append(out, p.Currency)
		}
	}
	sort.Strings(out)
	return out
}

func priceRowMatchesWizard(old model.Price, w WizardPrice) bool {
	return old.UnitAmount.Equal(w.UnitAmount) && old.TrialDays == w.TrialDays
}
