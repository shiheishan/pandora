package service

// 流量重置挂在套餐上（plans.reset_price / reset_limit）：价格空 = 不卖；次数上限空 = 不限，
// 订阅按每周期、一次性按有效期内计（计数在 subscription 按 traffic_reset_logs 算）。
// 订单行快照了金额，所以价格直接可改、不随版本冻结。

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// UpdateReset 改流量重置（Enabled 为假 = 关掉不卖，价格与次数上限都清空），以套餐 row_version 做乐观锁。
func (s *Service) UpdateReset(ctx context.Context, planID uuid.UUID, in ResetUpdate) (PlanDetail, error) {
	if err := checkExpected(in.ExpectedRowVersion); err != nil {
		return PlanDetail{}, err
	}
	if in.Enabled {
		fields := map[string]string{}
		validateReset(in.Price, in.Limit, fields)
		if err := invalid(fields); err != nil {
			return PlanDetail{}, err
		}
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.lockEditablePlan(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		var price *decimal.Decimal
		limit := in.Limit
		if in.Enabled {
			price = &in.Price
		} else {
			limit = nil
		}
		ok, err := s.repo.UpdatePlanReset(ctx, planID, in.ExpectedRowVersion, price, limit)
		if err != nil {
			return err
		}
		if !ok {
			return rowConflict("套餐", cur.RowVersion)
		}
		s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
		return s.writeAudit(ctx, ActionPlanReset, ResourcePlan, planID, map[string]any{
			"before": map[string]any{
				"reset_price": resetPriceAudit(cur.ResetPrice),
				"reset_limit": cur.ResetLimit,
				"row_version": cur.RowVersion,
			},
			"after": map[string]any{
				"reset_price": resetPriceAudit(price),
				"reset_limit": limit,
				"row_version": in.ExpectedRowVersion + 1,
			},
		})
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

// resetPriceAudit 把重置价转成审计里的值：nil（不卖）或两位小数的字符串。
func resetPriceAudit(p *decimal.Decimal) any {
	if p == nil {
		return nil
	}
	return p.StringFixed(2)
}
