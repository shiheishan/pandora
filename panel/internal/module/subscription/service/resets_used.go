package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

// 本文件是流量已付费重置次数的读侧：从哪一刻起数（resetSince）与单份订阅的次数（ResetsUsed）。

// resetSince 是一份订阅从哪个时刻起数付费重置次数：套餐类型对应的流量配额行（订阅套餐本周期 cycle 行、
// 一次性套餐有效期内的 total 行，见 trafficPeriod）里周期起点最晚的那一行的起点；没有这行退回订阅的 PeriodStart。
func resetSince(kind string, sub model.Subscription, qs []Quota) time.Time {
	var best *time.Time
	for _, q := range qs {
		if q.Metric != MetricTraffic || q.Period != trafficPeriod(kind) {
			continue
		}
		if best == nil || q.PeriodStart.After(*best) {
			start := q.PeriodStart
			best = &start
		}
	}
	if best != nil {
		return *best
	}
	return sub.CurrentPeriodStart
}

// ResetsUsed 读一份订阅已付费重置的次数（traffic_reset_logs reason=order，同一张单只算一次）：
// 订阅套餐按本周期、一次性套餐按有效期内，order 建流量重置单时拿它与 Plan.ResetLimit 比对是否超限。
// 只读不加锁。
func (s *Service) ResetsUsed(ctx context.Context, subID uuid.UUID) (int, error) {
	m, err := s.loadSub(ctx, subID, nil, repo.NoLock)
	if err != nil {
		return 0, err
	}
	plan, err := s.planOf(ctx, m.PlanID)
	if err != nil {
		return 0, err
	}
	rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{subID}, false)
	if err != nil {
		return 0, subErr(err)
	}
	qs := make([]Quota, 0, len(rows))
	for _, q := range rows {
		qs = append(qs, toQuota(q))
	}
	counts, err := s.repo.PaidResetCounts(ctx, []repo.ResetSince{{SubscriptionID: subID, Since: resetSince(plan.Kind, m, qs)}})
	if err != nil {
		return 0, subErr(err)
	}
	return counts[subID], nil
}
