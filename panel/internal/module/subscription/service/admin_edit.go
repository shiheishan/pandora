package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

const (
	ActionPeriodEndSet  = "subscription.period_end_set"
	ActionQuotaLimitSet = "subscription.quota_limit_set"
	// maxQuotaLimitBytes 是后台直接设流量上限的最大值（10 TiB）
	maxQuotaLimitBytes int64 = 10 << 40
)

// AdminSetPeriodEndResult 是改到期的结果。
type AdminSetPeriodEndResult struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	PreviousEnd    time.Time
	PeriodEnd      time.Time
}

// AdminSetPeriodEnd 把一份正在生效的订阅的到期时间直接改成 end（可提前也可延后，不折算流量、不改状态）；已过期的请用加时长救回。写订阅事件 period_end_set 与审计。
func (s *Service) AdminSetPeriodEnd(ctx context.Context, subID uuid.UUID, end time.Time, reason string) (AdminSetPeriodEndResult, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return AdminSetPeriodEndResult{}, err
	}
	cleanReason, err := cleanText(reason, minReasonRunes, maxReasonRunes, "reason", "请写清原因，5 到 500 个字")
	if err != nil {
		return AdminSetPeriodEndResult{}, err
	}
	end = end.UTC().Truncate(time.Microsecond)

	var out AdminSetPeriodEndResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockForAdmin(ctx, subID)
		if err != nil {
			return err
		}
		now := s.clock()
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, now) != StateLive {
			return errs.Newf(errs.Conflict, "只能改正在生效的订阅；已过期的请用「加时长」救回")
		}
		if !end.After(now) || !end.After(m.CurrentPeriodStart) || end.After(now.AddDate(10, 0, 0)) {
			return errs.Invalid(map[string]string{"period_end": "到期时间须晚于现在，且不超过 10 年"})
		}

		prev := m.CurrentPeriodEnd
		ok, err := s.repo.UpdateSubscription(ctx, m.ID, m.Status, map[string]any{"current_period_end": end})
		if err != nil {
			return subErr(err)
		}
		if !ok {
			return ErrStateChanged
		}

		quotas, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
		if err != nil {
			return subErr(err)
		}
		for _, q := range quotas {
			if q.Period == PeriodCycle && q.PeriodEnd != nil && q.PeriodEnd.Equal(prev) {
				if err := s.repo.UpdateQuota(ctx, q.ID, map[string]any{"period_end": end}); err != nil {
					return subErr(err)
				}
			} else if (q.Period == PeriodDay || q.Period == PeriodMonth) && q.PeriodEnd != nil && q.PeriodEnd.After(end) {
				if err := s.repo.UpdateQuota(ctx, q.ID, map[string]any{"period_end": end}); err != nil {
					return subErr(err)
				}
			}
		}

		s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, m.ID, m.UserID)
		s.notifyRoster(ctx, m.ID)

		from := m.Status
		if err := s.writeEvent(ctx, m.ID, EventPeriodEndSet, &from, m.Status, actorFrom(p), nil, map[string]any{
			"previous_end": prev,
			"period_end":   end,
			"reason":       cleanReason,
		}); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, ActionPeriodEndSet, m.ID, map[string]any{
			"user_id":      m.UserID.String(),
			"previous_end": prev,
			"period_end":   end,
			"reason":       cleanReason,
		}); err != nil {
			return err
		}

		out = AdminSetPeriodEndResult{
			SubscriptionID: m.ID,
			UserID:         m.UserID,
			PreviousEnd:    prev,
			PeriodEnd:      end,
		}
		return nil
	})
	return out, err
}

// AdminSetQuotaLimit 改一份订阅某个配额行（指标 + 周期）的上限；limit 为 nil 表示不限。写订阅事件 quota_limit_set 与审计。
// 只管当期：下一个周期滚动（RollQuotas）按套餐版本重设上限。
func (s *Service) AdminSetQuotaLimit(ctx context.Context, subID uuid.UUID, metric, period string, limit *int64, reason string) error {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return err
	}
	cleanReason, err := cleanText(reason, minReasonRunes, maxReasonRunes, "reason", "请写清原因，5 到 500 个字")
	if err != nil {
		return err
	}
	if limit != nil && (*limit < 0 || *limit > maxQuotaLimitBytes) {
		return errs.Invalid(map[string]string{"limit_bytes": "流量上限须在 0 到 10 TiB 之间，留空表示不限"})
	}
	switch period {
	case PeriodTotal, PeriodCycle, PeriodDay, PeriodMonth:
	default:
		return errs.Invalid(map[string]string{"period": "配额周期不合法"})
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockForAdmin(ctx, subID)
		if err != nil {
			return err
		}
		now := s.clock()
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, now) == StateEnded {
			return errs.Newf(errs.Conflict, "这一份已停用，不能再改")
		}

		quotas, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
		if err != nil {
			return subErr(err)
		}
		var targetID *uuid.UUID
		var beforeLimit *int64
		for _, q := range quotas {
			if q.Metric == metric && q.Period == period {
				id := q.ID
				targetID = &id
				beforeLimit = q.LimitValue
				break
			}
		}
		if targetID == nil {
			return errs.Invalid(map[string]string{"metric": "这一份没有这个配额"})
		}

		if err := s.repo.UpdateQuota(ctx, *targetID, map[string]any{"limit_value": limit}); err != nil {
			return subErr(err)
		}
		s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, m.ID, m.UserID)
		s.notifyRoster(ctx, m.ID)

		from := m.Status
		if err := s.writeEvent(ctx, m.ID, EventQuotaLimitSet, &from, m.Status, actorFrom(p), nil, map[string]any{
			"metric": metric,
			"period": period,
			"before": beforeLimit,
			"after":  limit,
			"reason": cleanReason,
		}); err != nil {
			return err
		}
		return s.writeAudit(ctx, ActionQuotaLimitSet, m.ID, map[string]any{
			"user_id": m.UserID.String(),
			"metric":  metric,
			"period":  period,
			"before":  beforeLimit,
			"after":   limit,
			"reason":  cleanReason,
		})
	})
}
