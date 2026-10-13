package service

import (
	"context"

	"github.com/google/uuid"
)

// LivePlanIDs 返回用户名下生效中（StateLive）订阅的套餐 id，去重；没有返回空切片。一条 SQL。
// content 门户按「限定套餐」过滤文章用。
func (s *Service) LivePlanIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.repo.SubscriptionsByUsers(ctx, []uuid.UUID{userID})
	if err != nil {
		return nil, subErr(err)
	}
	now := s.clock()
	seen := make(map[uuid.UUID]struct{}, len(rows))
	out := make([]uuid.UUID, 0, len(rows))
	for _, m := range rows {
		if toSubscription(m).StateAt(now) != StateLive {
			continue
		}
		if _, ok := seen[m.PlanID]; ok {
			continue
		}
		seen[m.PlanID] = struct{}{}
		out = append(out, m.PlanID)
	}
	return out, nil
}
