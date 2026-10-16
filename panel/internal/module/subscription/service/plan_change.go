package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ChangePlan 把原订阅原地换成另一个套餐（换套餐订单、套餐卡遇到不同套餐），可以跨类型（订阅 ↔ 一次性）：周期从现在起按新价格档重开一整期，
// 全部配额行按新版本重置（新版本没有的指标上限置空即不限，多出来的指标补行），写 plan_change 重置日志与
// plan_changed 事件。凭据不换（链接不变）。剩余价值与退余额由调用方算并记账。
// 已停用回 ErrRenewalWindowClosed。在 ctx 的事务里执行，自己锁订阅行；提交后通知名单变化。
// 不按来源防重：同一笔订单或卡密调两次就换两次。防重靠调用方——订单的状态转换（paid → fulfilled，带旧状态条件）
// 或卡密兑换的唯一约束——且要在同一事务里先于这里完成。
func (s *Service) ChangePlan(ctx context.Context, in PlanChange) (PlanChangeResult, error) {
	if err := validatePrice(in.Price); err != nil {
		return PlanChangeResult{}, err
	}
	if err := validateActor(in.Actor); err != nil {
		return PlanChangeResult{}, err
	}
	if (in.OrderID == nil) == (in.GiftCodeID == nil) {
		return PlanChangeResult{}, errs.Wrap(errs.Internal, errMissing("OrderID 或 GiftCodeID 恰好一个"))
	}
	v, err := s.versionFor(ctx, in.PlanID, in.PlanVersionID)
	if err != nil {
		return PlanChangeResult{}, err
	}
	plan, err := s.planOf(ctx, in.PlanID)
	if err != nil {
		return PlanChangeResult{}, err
	}
	v = withKind(v, plan.Kind)
	var out PlanChangeResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, in.SubscriptionID, &in.UserID)
		if err != nil {
			return err
		}
		now := s.clock()
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, now) == StateEnded {
			return ErrRenewalWindowClosed
		}
		start := now
		end := PeriodEndFor(start, in.Price)
		// 换套餐不沿用原价格档：新价格档为空（套餐卡）时 price_id 置空
		cols := snapshotCols(in.Price, nil)
		cols["plan_id"] = in.PlanID
		cols["plan_version_id"] = in.PlanVersionID
		cols["current_period_start"] = start
		cols["current_period_end"] = end
		if err := s.transition(ctx, m, StatusActive, cols); err != nil {
			return err
		}
		before, err := s.rebuildQuotas(ctx, m, v, start, end)
		if err != nil {
			return err
		}
		if err := s.logResets(ctx, m, before, ResetPlanChange, nil, ""); err != nil {
			return err
		}
		payload := map[string]any{
			"from_plan_id":         m.PlanID,
			"from_plan_version_id": m.PlanVersionID,
			"to_plan_id":           in.PlanID,
			"to_plan_version_id":   in.PlanVersionID,
			"previous_end":         m.CurrentPeriodEnd,
			"period_end":           end,
			"source":               "order",
		}
		if in.GiftCodeID != nil {
			payload["source"] = "gift_card"
			payload["gift_card_code_id"] = *in.GiftCodeID
		}
		from := m.Status
		if err := s.writeEvent(ctx, m.ID, EventPlanChanged, &from, StatusActive, in.Actor, in.OrderID, payload); err != nil {
			return err
		}
		s.notifyRoster(ctx, m.ID)
		out = PlanChangeResult{
			SubscriptionID:    m.ID,
			FromPlanID:        m.PlanID,
			FromPlanVersionID: m.PlanVersionID,
			FromStatus:        from,
			PreviousEnd:       m.CurrentPeriodEnd,
			PeriodEnd:         end,
		}
		return nil
	})
	return out, err
}

// rebuildQuotas 按版本 v 重建一份订阅的全部配额：已有行的上限改成 v 的定义、已用量与送的量清零、周期对齐到 [start, cycleEnd)；
// v 没有定义的行直接删掉（不置 NULL：置空等于不限，门户、后台、机器人按第一条流量行取值时会显示成不限，审查 1016-A2 阻塞 1）；
// v 多出来的 (metric, period) 补插新行。返回清零前的旧行（写重置日志用）。
func (s *Service) rebuildQuotas(ctx context.Context, m model.Subscription, v catalogsvc.Version, start, cycleEnd time.Time) ([]model.QuotaBalance, error) {
	rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
	if err != nil {
		return nil, subErr(err)
	}
	have := make(map[[2]string]bool, len(rows))
	var stale []uuid.UUID
	for _, q := range rows {
		def, ok := quotaDef(v, q.Metric, q.Period)
		if !ok {
			stale = append(stale, q.ID)
			continue
		}
		have[[2]string{q.Metric, q.Period}] = true
		if err := s.repo.UpdateQuota(ctx, q.ID, map[string]any{
			"limit_value":  def.Limit,
			"consumed":     0,
			"bonus_value":  0,
			"period_start": start,
			"period_end":   QuotaPeriodEnd(q.Period, start, cycleEnd),
		}); err != nil {
			return nil, subErr(err)
		}
	}
	if err := s.repo.DeleteQuotas(ctx, stale); err != nil {
		return nil, subErr(err)
	}
	var missing []model.QuotaBalance
	for _, row := range quotaRows(m.ID, v.Quotas, start, cycleEnd) {
		if !have[[2]string{row.Metric, row.Period}] {
			missing = append(missing, row)
		}
	}
	if err := s.repo.InsertQuotas(ctx, missing); err != nil {
		return nil, subErr(err)
	}
	return rows, nil
}
