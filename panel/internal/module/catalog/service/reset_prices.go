package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// ActionResetPriceSet 是后台整组替换套餐流量重置价格的审计动作名。
const ActionResetPriceSet = "plan.reset_prices_set"

// ResetPrice 是套餐按币种的流量重置价格（元）。
type ResetPrice struct {
	Currency   money.Currency
	UnitAmount decimal.Decimal
}

// PlanResetPrices 后台读取套餐的流量重置价格；套餐不存在时返回 ErrNotFound。
func (s *Service) PlanResetPrices(ctx context.Context, planID uuid.UUID) ([]ResetPrice, error) {
	_, found, err := s.repo.PlanByID(ctx, planID, repo.NoLock)
	if err != nil {
		return nil, catalogErr(err)
	}
	if !found {
		return nil, ErrNotFound
	}
	rows, err := s.repo.ResetPricesByPlan(ctx, planID)
	if err != nil {
		return nil, catalogErr(err)
	}
	return resetPricesFromModels(rows), nil
}

// ReplacePlanResetPrices 后台整组替换套餐的流量重置价格；空切片表示不再售卖重置。
func (s *Service) ReplacePlanResetPrices(ctx context.Context, planID uuid.UUID, in []ResetPrice) ([]ResetPrice, error) {
	normalized, err := validateResetPrices(in)
	if err != nil {
		return nil, catalogErr(err)
	}
	var out []ResetPrice
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		_, found, err := s.repo.PlanByID(ctx, planID, repo.ForShare)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		beforeRows, err := s.repo.ResetPricesByPlan(ctx, planID)
		if err != nil {
			return err
		}
		modelRows := resetPricesToModels(planID, normalized)
		if err := s.repo.ReplaceResetPrices(ctx, planID, modelRows); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "重置价格刚被改过，请刷新后重试")
			}
			return err
		}
		if err := s.writeAudit(ctx, ActionResetPriceSet, ResourcePlan, planID, map[string]any{
			"before": resetPriceDigest(beforeRows),
			"after":  resetPriceDigest(modelRows),
		}); err != nil {
			return err
		}
		out = normalized
		return nil
	})
	if err != nil {
		return nil, catalogErr(err)
	}
	return out, nil
}

// ResetPrice 供订单模块按套餐与币种取流量重置价格；没有配置时返回 (decimal.Zero, false, nil)。
func (s *Service) ResetPrice(ctx context.Context, planID uuid.UUID, cur money.Currency) (decimal.Decimal, bool, error) {
	row, found, err := s.repo.ResetPrice(ctx, planID, string(cur))
	if err != nil {
		return decimal.Zero, false, catalogErr(err)
	}
	if !found {
		return decimal.Zero, false, nil
	}
	return row.UnitAmount, true, nil
}

func validateResetPrices(in []ResetPrice) ([]ResetPrice, error) {
	if len(in) > 2 {
		return nil, errs.Invalid(map[string]string{
			"reset_prices": "最多配置 CNY、USD 各一档",
		})
	}
	fields := map[string]string{}
	seen := map[money.Currency]int{}
	out := make([]ResetPrice, 0, len(in))
	for i, row := range in {
		cur, err := money.ParseCurrency(string(row.Currency))
		if err != nil {
			fields[fieldKey(i, "currency")] = "只支持 CNY 或 USD"
			continue
		}
		if prev, ok := seen[cur]; ok {
			fields[fieldKey(prev, "currency")] = "同一币种只能填一档"
			fields[fieldKey(i, "currency")] = "同一币种只能填一档"
			continue
		}
		seen[cur] = i
		if row.UnitAmount.LessThanOrEqual(decimal.Zero) {
			fields[fieldKey(i, "unit_amount")] = "重置价格须大于 0，最多两位小数"
			continue
		}
		if err := money.Check(row.UnitAmount); err != nil {
			fields[fieldKey(i, "unit_amount")] = "重置价格须大于 0，最多两位小数"
			continue
		}
		out = append(out, ResetPrice{Currency: cur, UnitAmount: row.UnitAmount})
	}
	if len(fields) > 0 {
		return nil, errs.Invalid(fields)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out, nil
}

func fieldKey(i int, name string) string {
	return fmt.Sprintf("reset_prices.%d.%s", i, name)
}

func resetPricesFromModels(rows []model.PlanResetPrice) []ResetPrice {
	out := make([]ResetPrice, 0, len(rows))
	for _, row := range rows {
		cur, _ := money.ParseCurrency(row.Currency)
		out = append(out, ResetPrice{Currency: cur, UnitAmount: row.UnitAmount})
	}
	if out == nil {
		return []ResetPrice{}
	}
	return out
}

func resetPricesToModels(planID uuid.UUID, rows []ResetPrice) []model.PlanResetPrice {
	out := make([]model.PlanResetPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, model.PlanResetPrice{
			PlanID:     planID,
			Currency:   string(row.Currency),
			UnitAmount: row.UnitAmount,
		})
	}
	return out
}

func resetPriceDigest(rows []model.PlanResetPrice) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"currency":    row.Currency,
			"unit_amount": row.UnitAmount.StringFixed(2),
		})
	}
	return out
}
