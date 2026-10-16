package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// GrantBonus 是送流量（礼品卡、后台「加流量」、盲盒奖品）：加到这一份流量配额行的 bonus_value。
// 订阅套餐加在本周期的 cycle 行（周期滚动时清零，当期有效），一次性套餐加在 total 行（不滚动，一直有效）。
// 不限流量（没有限额的流量配额行）回 ErrUnlimited；过期回 ErrNotActive。
// 不写事件、不写审计：后台入口自己写审计，礼品卡有兑换流水。
// 锁序同 ResetTraffic：调用方（order / giftcard）先锁订单或卡密，再调这里；在 ctx 的事务里自己锁订阅行，
// 提交后通知名单（用尽的人加了流量要重新进名单）。返回加完后的 bonus_value。
func (s *Service) GrantBonus(ctx context.Context, in BonusGrant) (int64, error) {
	if in.SubscriptionID == uuid.Nil {
		return 0, errs.Wrap(errs.Internal, errMissing("SubscriptionID"))
	}
	if in.Bytes <= 0 {
		return 0, errs.Invalid(map[string]string{"bytes": "流量必须大于 0"})
	}
	switch in.Source {
	case BonusGiftCard:
	case BonusAdmin:
		if in.ActorID == nil || *in.ActorID == uuid.Nil {
			return 0, errs.Wrap(errs.Internal, errMissing("ActorID"))
		}
	default:
		return 0, errs.Wrap(errs.Internal, errMissing("Source"))
	}
	if in.Note != "" {
		if _, err := cleanText(in.Note, 1, maxNoteRunes, "note", "备注最多 500 个字，不能含特殊字符"); err != nil {
			return 0, err
		}
	}
	var granted int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, in.SubscriptionID, in.UserID)
		if err != nil {
			return err
		}
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, s.clock()) != StateLive {
			return ErrNotActive
		}
		plan, err := s.planOf(ctx, m.PlanID)
		if err != nil {
			return err
		}
		// 流量落在哪一行按套餐类型判断，不看版本（开发库里可能有 cycle 的一次性版本）。
		period := trafficPeriod(plan.Kind)
		rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
		if err != nil {
			return subErr(err)
		}
		// 目标行：本类型的限额流量行；同一类型多行时取本期（period_start 最晚）那一行。
		var target *model.QuotaBalance
		for i := range rows {
			q := &rows[i]
			if q.Metric != MetricTraffic || q.Period != period || q.LimitValue == nil {
				continue
			}
			if target == nil || q.PeriodStart.After(target.PeriodStart) {
				target = q
			}
		}
		if target == nil {
			return ErrUnlimited
		}
		bonus, err := s.repo.AddQuotaBonus(ctx, target.ID, in.Bytes)
		if err != nil {
			return subErr(err)
		}
		granted = bonus
		s.notifyRoster(ctx, m.ID)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return granted, nil
}
