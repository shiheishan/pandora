package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// Renew 在原订阅上续一期（续费订单、同套餐的套餐卡），版本沿用订阅自己的。
//
//	提前续费   付款时还没到期：新周期接在原到期之后，本期配额一行都不动（到点由配额滚动滚进新周期）
//	回调晚到   到期前下单、到期后才回调：同样接在原到期之后
//	过期恢复   其余付款时已到期的：从付款时刻起算一整期，全部配额行清零、上限按版本写回，写 renewal 重置日志；
//	           事件的起始状态记 expired（状态列还停在 active、过期扫描没赶上的也一样）
//
// 已停用（续费窗口关了）回 ErrRenewalWindowClosed。凭据不动（链接不变）；流量包不碰。
// 在 ctx 的事务里执行（没有就自己开），自己锁订阅行；提交后通知名单变化。
// 不按来源防重：同一笔订单或卡密调两次就续两次。防重靠调用方——订单的状态转换（paid → fulfilled，带旧状态条件）
// 或卡密兑换的唯一约束——且要在同一事务里先于这里完成。
func (s *Service) Renew(ctx context.Context, in Renewal) (RenewalResult, error) {
	if err := validatePrice(in.Price); err != nil {
		return RenewalResult{}, err
	}
	if err := validateActor(in.Actor); err != nil {
		return RenewalResult{}, err
	}
	if in.PaidAt.IsZero() {
		return RenewalResult{}, errs.Wrap(errs.Internal, errMissing("PaidAt"))
	}
	var out RenewalResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, in.SubscriptionID, &in.UserID)
		if err != nil {
			return err
		}
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, s.clock()) == StateEnded {
			return ErrRenewalWindowClosed
		}
		paidAt := in.PaidAt.UTC().Truncate(time.Microsecond)
		base, restart := RenewalBase(m.CurrentPeriodEnd, paidAt, in.OrderCreatedAt)
		end := AddInterval(base, in.Price.Interval, in.Price.IntervalCount).Truncate(time.Microsecond)
		from := m.Status
		if restart {
			from = StatusExpired
		}
		cols := snapshotCols(in.Price, m.PriceID)
		cols["current_period_end"] = end
		if restart {
			cols["current_period_start"] = paidAt
		}
		if err := s.transition(ctx, m, StatusActive, cols); err != nil {
			return err
		}
		if restart {
			if err := s.restartQuotas(ctx, m, paidAt, end); err != nil {
				return err
			}
		}
		if err := s.writeEvent(ctx, m.ID, EventRenewed, &from, StatusActive, in.Actor, in.OrderID, map[string]any{
			"source":       in.Source,
			"restart":      restart,
			"previous_end": m.CurrentPeriodEnd,
			"period_end":   end,
		}); err != nil {
			return err
		}
		s.notifyRoster(ctx, m.ID)
		out = RenewalResult{
			SubscriptionID: m.ID,
			FromStatus:     from,
			Restarted:      restart,
			PreviousEnd:    m.CurrentPeriodEnd,
			PeriodEnd:      end,
		}
		return nil
	})
	return out, err
}

// snapshotCols 是更新价格快照的列；新价格档为空时保留原来的 price_id。
func snapshotCols(p PriceSnapshot, oldPriceID *uuid.UUID) map[string]any {
	priceID := p.PriceID
	if priceID == nil {
		priceID = oldPriceID
	}
	return map[string]any{
		"price_id":                priceID,
		"snapshot_currency":       string(p.Currency),
		"snapshot_amount":         p.Amount,
		"snapshot_interval":       p.Interval,
		"snapshot_interval_count": p.IntervalCount,
	}
}

// restartQuotas 是过期恢复时的配额对齐：全部配额行已用量清零、周期对齐到 start，上限按订阅自己的版本写回
// （救回时折算加进来的那部分只属于上一个周期）；清零前的用量写 renewal 重置日志。
func (s *Service) restartQuotas(ctx context.Context, m model.Subscription, start, cycleEnd time.Time) error {
	v, err := s.versionFor(ctx, m.PlanID, m.PlanVersionID)
	if err != nil {
		return err
	}
	rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
	if err != nil {
		return subErr(err)
	}
	for _, q := range rows {
		cols := map[string]any{
			"consumed":     0,
			"period_start": start,
			"period_end":   QuotaPeriodEnd(q.Period, start, cycleEnd),
		}
		if def, ok := quotaDef(v, q.Metric, q.Period); ok {
			cols["limit_value"] = def.Limit
		}
		if err := s.repo.UpdateQuota(ctx, q.ID, cols); err != nil {
			return subErr(err)
		}
	}
	return s.logResets(ctx, m, rows, ResetRenewal, nil, "")
}
