package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// insertPriceTx 在调用方事务里插入一档在售价格并推送变更。
func (s *Service) insertPriceTx(ctx context.Context, planID uuid.UUID, kind, interval string, count int, amount decimal.Decimal, validityDays *int) (model.Price, error) {
	m := &model.Price{
		PlanID:          planID,
		PlanKind:        kind,
		UnitAmount:      amount,
		BillingInterval: interval,
		IntervalCount:   count,
		ValidityDays:    validityDays,
		Status:          PriceActive,
	}
	if err := s.repo.InsertPrice(ctx, m); err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			return model.Price{}, errs.Newf(errs.Conflict, "同一周期已有在售价格")
		}
		return model.Price{}, err
	}
	s.catalogChanged(ctx, rtTablePrices, realtime.OpInsert, m.ID)
	return *m, nil
}

// archivePriceTx 在调用方事务里归档一档在售价格。
func (s *Service) archivePriceTx(ctx context.Context, p model.Price) error {
	ok, err := s.repo.ArchivePrice(ctx, p.ID, p.RowVersion)
	if err != nil {
		return err
	}
	if !ok {
		return rowConflict("价格", p.RowVersion)
	}
	s.catalogChanged(ctx, rtTablePrices, realtime.OpUpdate, p.ID)
	return nil
}

func validityDaysEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// priceAuditRow 是改价审计里的一档：UnitAmount 为 nil 表示这一档不再卖。
type priceAuditRow struct {
	BillingInterval string  `json:"billing_interval"`
	IntervalCount   int     `json:"interval_count"`
	UnitAmount      *string `json:"unit_amount"`
	ValidityDays    *int    `json:"validity_days"`
}

func priceAuditFromAmount(amount decimal.Decimal) *string {
	s := amount.StringFixed(2)
	return &s
}

// UpdatePrices 改价：归档旧价、建新价；以套餐 row_version 做乐观锁。
func (s *Service) UpdatePrices(ctx context.Context, planID uuid.UUID, in PricesUpdate) (PlanDetail, error) {
	if err := checkExpected(in.ExpectedRowVersion); err != nil {
		return PlanDetail{}, err
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.lockEditablePlan(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		fields := map[string]string{}
		switch cur.Kind {
		case KindSubscription:
			if in.OneTime != nil {
				fields["unit_amount"] = "订阅套餐按周期定价，不能设一次性价格"
			}
			validateTierChanges(in.Tiers, fields)
		case KindOneTime:
			if len(in.Tiers) > 0 {
				fields["prices"] = "一次性套餐只有一个价格，不能设周期价"
			}
			if in.OneTime == nil {
				fields["unit_amount"] = "一次性套餐必须设价格"
			} else {
				validateOneTime(*in.OneTime, fields)
			}
		default:
			fields["kind"] = "套餐类型无效"
		}
		if err := invalid(fields); err != nil {
			return err
		}

		active, err := s.repo.ActivePrices(ctx, []uuid.UUID{planID}, repo.ForUpdate)
		if err != nil {
			return err
		}
		byTier := make(map[string]model.Price, len(active))
		for _, p := range active {
			byTier[tierKey(p.BillingInterval, p.IntervalCount)] = p
		}

		changes := 0
		auditPrices := make([]priceAuditRow, 0)

		switch cur.Kind {
		case KindSubscription:
			for _, t := range in.Tiers {
				k := tierKey(t.BillingInterval, t.IntervalCount)
				old, hasOld := byTier[k]
				if t.UnitAmount == nil {
					if hasOld {
						if err := s.archivePriceTx(ctx, old); err != nil {
							return err
						}
						changes++
						auditPrices = append(auditPrices, priceAuditRow{BillingInterval: t.BillingInterval, IntervalCount: t.IntervalCount})
					}
					continue
				}
				if hasOld && old.UnitAmount.Equal(*t.UnitAmount) {
					continue
				}
				if hasOld {
					if err := s.archivePriceTx(ctx, old); err != nil {
						return err
					}
				}
				if _, err := s.insertPriceTx(ctx, planID, cur.Kind, t.BillingInterval, t.IntervalCount, *t.UnitAmount, nil); err != nil {
					return err
				}
				changes++
				auditPrices = append(auditPrices, priceAuditRow{
					BillingInterval: t.BillingInterval,
					IntervalCount:   t.IntervalCount,
					UnitAmount:      priceAuditFromAmount(*t.UnitAmount),
					ValidityDays:    nil,
				})
			}
		case KindOneTime:
			if in.OneTime == nil {
				return nil
			}
			k := tierKey(IntervalOneTime, 1)
			old, hasOld := byTier[k]
			if hasOld && old.UnitAmount.Equal(in.OneTime.UnitAmount) && validityDaysEqual(old.ValidityDays, in.OneTime.ValidityDays) {
				break
			}
			if hasOld {
				if err := s.archivePriceTx(ctx, old); err != nil {
					return err
				}
			}
			if _, err := s.insertPriceTx(ctx, planID, cur.Kind, IntervalOneTime, 1, in.OneTime.UnitAmount, in.OneTime.ValidityDays); err != nil {
				return err
			}
			changes++
			auditPrices = append(auditPrices, priceAuditRow{
				BillingInterval: IntervalOneTime,
				IntervalCount:   1,
				UnitAmount:      priceAuditFromAmount(in.OneTime.UnitAmount),
				ValidityDays:    in.OneTime.ValidityDays,
			})
		}

		if cur.Status == PlanActive {
			left, err := s.repo.ActivePrices(ctx, []uuid.UUID{planID}, repo.NoLock)
			if err != nil {
				return err
			}
			if len(left) == 0 {
				return errs.Invalid(map[string]string{"prices": "在售套餐至少保留一档价格"})
			}
		}

		if changes == 0 {
			return nil
		}

		next, err := s.bumpPlanRow(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		return s.writeAudit(ctx, ActionPlanPrices, ResourcePlan, planID, map[string]any{
			"before": map[string]any{"row_version": in.ExpectedRowVersion},
			"after": map[string]any{
				"row_version": next,
				"changes":     changes,
				"prices":      auditPrices,
			},
		})
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}
