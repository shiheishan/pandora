package service

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// NotifyCandidate 是到期类通知扫到的一份订阅。
type NotifyCandidate struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	PlanID         uuid.UUID
	PeriodEnd      time.Time
}

// QuotaNotice 是本期流量用量达到预警线的一行配额：Total = 配额上限 + 挂在这一份上的流量包余量。
type QuotaNotice struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	PlanID         uuid.UUID
	Consumed       int64
	Total          int64
	PeriodStart    time.Time
}

// ExpiringForNotify 取 status=active、current_period_end 在 (after, until] 内的订阅，按 current_period_end、id 升序。
func (s *Service) ExpiringForNotify(ctx context.Context, after, until time.Time) ([]NotifyCandidate, error) {
	rows, err := s.repo.ExpiringForNotify(ctx, after, until)
	if err != nil {
		return nil, subErr(err)
	}
	out := make([]NotifyCandidate, len(rows))
	for i, r := range rows {
		out[i] = NotifyCandidate{
			SubscriptionID: r.SubscriptionID,
			UserID:         r.UserID,
			PlanID:         r.PlanID,
			PeriodEnd:      r.PeriodEnd,
		}
	}
	return out, nil
}

// ExpiredForNotify 取 status=expired、renewal_closed_at 为空、current_period_end 在 (endedAfter, endedUntil] 内的订阅；
// onlyWithoutLive 为 true 时排除名下另有生效订阅（status=active 且 current_period_end > now）的用户。按 current_period_end、id 升序。
func (s *Service) ExpiredForNotify(ctx context.Context, endedAfter, endedUntil time.Time, onlyWithoutLive bool) ([]NotifyCandidate, error) {
	rows, err := s.repo.ExpiredForNotify(ctx, endedAfter, endedUntil, onlyWithoutLive, s.clock())
	if err != nil {
		return nil, subErr(err)
	}
	out := make([]NotifyCandidate, len(rows))
	for i, r := range rows {
		out[i] = NotifyCandidate{
			SubscriptionID: r.SubscriptionID,
			UserID:         r.UserID,
			PlanID:         r.PlanID,
			PeriodEnd:      r.PeriodEnd,
		}
	}
	return out, nil
}

// QuotaUsageForNotify 取生效订阅（status=active 且 current_period_end > now）本期的 traffic.bytes 限量配额行
// （limit_value > 0，period_start <= now 且 (period_end 为空或 > now)），用量 consumed*100 >= Total*minPercent 的那些；按 subscription_id、period_start 升序。
func (s *Service) QuotaUsageForNotify(ctx context.Context, minPercent int) ([]QuotaNotice, error) {
	if minPercent < 1 || minPercent > 100 {
		return nil, errs.Wrap(errs.Internal, fmt.Errorf("minPercent 须在 1..100，得 %d", minPercent))
	}
	rows, err := s.repo.QuotaUsageForNotify(ctx, s.clock(), minPercent)
	if err != nil {
		return nil, subErr(err)
	}
	out := make([]QuotaNotice, len(rows))
	for i, r := range rows {
		out[i] = QuotaNotice{
			SubscriptionID: r.SubscriptionID,
			UserID:         r.UserID,
			PlanID:         r.PlanID,
			Consumed:       r.Consumed,
			Total:          r.Total,
			PeriodStart:    r.PeriodStart,
		}
	}
	return out, nil
}

// LivePlanIDs 取用户名下生效订阅（status=active 且 current_period_end > now）的套餐 id，去重、升序；没有时返回空切片（不是 nil）。
func (s *Service) LivePlanIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	ids, err := s.repo.LivePlanIDs(ctx, userID, s.clock())
	if err != nil {
		return nil, subErr(err)
	}
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	return ids, nil
}
