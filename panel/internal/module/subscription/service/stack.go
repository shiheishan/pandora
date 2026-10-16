package service

import (
	"context"
	"math"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// Stack 是一次性套餐的叠加（叠加单履约、同套餐的一次性套餐卡）：total 流量配额上限加上新版本的流量，
// 期末：新价有天数 → max(原期末, now) + N 天；新价永久或原来已永久 → 永久哨兵。
// 不按来源防重（同 Renew：靠订单状态转换或卡密唯一约束）；锁序同 Renew（调用方先锁订单再调这里）。
// 在 ctx 的事务里执行（没有就自己开），自己锁订阅行；提交后通知名单变化。
func (s *Service) Stack(ctx context.Context, in Stacking) (RenewalResult, error) {
	if err := validatePrice(in.Price); err != nil {
		return RenewalResult{}, err
	}
	if err := validateActor(in.Actor); err != nil {
		return RenewalResult{}, err
	}
	if in.PaidAt.IsZero() {
		return RenewalResult{}, errs.Wrap(errs.Internal, errMissing("PaidAt"))
	}
	if (in.OrderID == nil) == (in.GiftCodeID == nil) {
		return RenewalResult{}, errs.Wrap(errs.Internal, errMissing("OrderID 或 GiftCodeID 恰好一个"))
	}
	if in.Price.Interval != "one_time" {
		return RenewalResult{}, ErrStackNotOneTime
	}

	var out RenewalResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, in.SubscriptionID, &in.UserID)
		if err != nil {
			return err
		}
		now := s.clock()
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, now) == StateEnded {
			return ErrRenewalWindowClosed
		}
		plan, err := s.planOf(ctx, m.PlanID)
		if err != nil {
			return err
		}
		if plan.Kind != catalogsvc.KindOneTime {
			return ErrStackNotOneTime
		}
		v, err := s.versionFor(ctx, m.PlanID, in.PlanVersionID)
		if err != nil {
			return err
		}
		v = withKind(v, plan.Kind)

		end := StackEnd(m.CurrentPeriodEnd, now, in.Price)
		from := m.Status
		cols := snapshotCols(in.Price, m.PriceID)
		cols["current_period_end"] = end
		cols["plan_version_id"] = in.PlanVersionID
		if err := s.transition(ctx, m, StatusActive, cols); err != nil {
			return err
		}

		def, hasDef := quotaDef(v, MetricTraffic, PeriodTotal)
		rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
		if err != nil {
			return subErr(err)
		}
		var found *model.QuotaBalance
		for i := range rows {
			if rows[i].Metric == MetricTraffic && rows[i].Period == PeriodTotal {
				found = &rows[i]
				break
			}
		}

		if found != nil {
			if found.LimitValue != nil {
				if def.Limit != nil {
					newLimit := saturatedAdd(*found.LimitValue, *def.Limit)
					if err := s.repo.UpdateQuota(ctx, found.ID, map[string]any{
						"limit_value": &newLimit,
					}); err != nil {
						return subErr(err)
					}
				} else {
					if err := s.repo.UpdateQuota(ctx, found.ID, map[string]any{
						"limit_value": nil,
					}); err != nil {
						return subErr(err)
					}
				}
			}
		} else if hasDef {
			newQuota := model.QuotaBalance{
				SubscriptionID: m.ID,
				Metric:         MetricTraffic,
				Period:         PeriodTotal,
				PeriodStart:    now,
				PeriodEnd:      nil,
				LimitValue:     def.Limit,
			}
			if err := s.repo.InsertQuotas(ctx, []model.QuotaBalance{newQuota}); err != nil {
				return subErr(err)
			}
		}

		payload := map[string]any{
			"mode":            "stack",
			"source":          in.Source,
			"restart":         false,
			"previous_end":    m.CurrentPeriodEnd,
			"period_end":      end,
			"plan_version_id": in.PlanVersionID,
		}
		if in.GiftCodeID != nil {
			payload["gift_card_code_id"] = *in.GiftCodeID
		}
		if err := s.writeEvent(ctx, m.ID, EventRenewed, &from, StatusActive, in.Actor, in.OrderID, payload); err != nil {
			return err
		}
		s.notifyRoster(ctx, m.ID)
		out = RenewalResult{
			SubscriptionID: m.ID,
			FromStatus:     from,
			Restarted:      false,
			PreviousEnd:    m.CurrentPeriodEnd,
			PeriodEnd:      end,
		}
		return nil
	})
	return out, err
}

// saturatedAdd 执行 int64 饱和相加，超过 math.MaxInt64 取 math.MaxInt64。
func saturatedAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
