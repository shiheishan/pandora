package service

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// maxExtendDays 是一次延期的最多天数。
const maxExtendDays = 3650

// rescueResetNote 是救回时日、月配额重新起算写进重置日志的备注。
const rescueResetNote = "救回已过期订阅，日 / 月配额从今天重新起算"

// Extend 只往后推到期时间、不开新周期（礼品卡加时长、后台加时长）。
//
// 基准取「现在」与「原到期」里更晚的：未到期的接着原到期往后，走过到期日的从现在起（周期起点也改成现在）。
// 本周期 cycle 配额行（周期末等于原到期的）跟着推到新到期，已用量不清零。
// 救回（已过期或已走过到期日）：状态回到 active；cycle 配额按「延长天数 ÷ 一个计费周期」折算加进上限；
// 走过到期日的，日、月配额从现在重新起算并写重置日志（礼品卡记 gift_card，后台记 manual 带操作人）。
// 已停用回 ErrExtendWindowClosed。在 ctx 的事务里执行，自己锁订阅行；提交后通知名单变化。
func (s *Service) Extend(ctx context.Context, in Extension) (ExtensionResult, error) {
	if in.Days < 1 || in.Days > maxExtendDays {
		return ExtensionResult{}, errs.Invalid(map[string]string{"days": "天数须在 1 到 3650 之间"})
	}
	if err := validateActor(in.Actor); err != nil {
		return ExtensionResult{}, err
	}
	resetReason, resetActor := ResetGiftCard, (*uuid.UUID)(nil)
	switch in.Source {
	case ExtendSourceGiftCard:
	case ExtendSourceAdmin:
		if in.Actor.Kind != ActorAdmin {
			return ExtensionResult{}, errs.Wrap(errs.Internal, errMissing("admin Actor"))
		}
		resetReason, resetActor = ResetManual, in.Actor.ID
	default:
		return ExtensionResult{}, errs.Wrap(errs.Internal, errMissing("Source"))
	}
	var out ExtensionResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, in.SubscriptionID, in.UserID)
		if err != nil {
			return err
		}
		now := s.clock()
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, now) == StateEnded {
			return ErrExtendWindowClosed
		}
		prev := m.CurrentPeriodEnd
		base, lapsed := ExtendBase(prev, now)
		rescued := lapsed || m.Status == StatusExpired
		end := base.UTC().AddDate(0, 0, in.Days).Truncate(time.Microsecond)
		cols := map[string]any{"current_period_end": end}
		if lapsed {
			cols["current_period_start"] = now
		}
		if err := s.transition(ctx, m, StatusActive, cols); err != nil {
			return err
		}
		rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
		if err != nil {
			return subErr(err)
		}
		cycle := CycleLength(now, m.SnapshotInterval, m.SnapshotIntervalCount)
		prorated := map[string]int64{}
		var reset []model.QuotaBalance
		for _, q := range rows {
			switch {
			case q.Period == PeriodCycle && q.PeriodEnd != nil && q.PeriodEnd.Equal(prev):
				qc := map[string]any{"period_end": end}
				if rescued && q.LimitValue != nil {
					if add := ProratedQuota(*q.LimitValue, in.Days, cycle); add > 0 && add <= math.MaxInt64-*q.LimitValue {
						qc["limit_value"] = *q.LimitValue + add
						prorated[q.Metric] += add
					}
				}
				if err := s.repo.UpdateQuota(ctx, q.ID, qc); err != nil {
					return subErr(err)
				}
			case lapsed && (q.Period == PeriodDay || q.Period == PeriodMonth):
				if err := s.repo.UpdateQuota(ctx, q.ID, map[string]any{
					"consumed":     0,
					"period_start": now,
					"period_end":   QuotaPeriodEnd(q.Period, now, end),
				}); err != nil {
					return subErr(err)
				}
				reset = append(reset, q)
			}
		}
		if err := s.logResets(ctx, m, reset, resetReason, resetActor, rescueResetNote); err != nil {
			return err
		}
		payload := map[string]any{
			"source":       in.Source,
			"days":         in.Days,
			"previous_end": prev,
			"period_end":   end,
			"rescued":      rescued,
			"restart":      lapsed,
		}
		if len(prorated) > 0 {
			payload["prorated"] = prorated
		}
		if in.Reason != "" {
			payload["reason"] = in.Reason
		}
		from := m.Status
		if err := s.writeEvent(ctx, m.ID, EventExtended, &from, StatusActive, in.Actor, nil, payload); err != nil {
			return err
		}
		s.notifyRoster(ctx, m.ID)
		out = ExtensionResult{
			SubscriptionID: m.ID,
			UserID:         m.UserID,
			FromStatus:     from,
			Rescued:        rescued,
			Restarted:      lapsed,
			PreviousEnd:    prev,
			PeriodEnd:      end,
			Prorated:       prorated,
		}
		return nil
	})
	return out, err
}
