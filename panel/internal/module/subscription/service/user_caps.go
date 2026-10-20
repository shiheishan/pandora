package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LiveSubscriptionCounts 是每个用户此刻生效中的订阅份数（只读、不加锁）：order 下单时按它检查「每人订阅数上限」。
// 生效口径与 StateLive 相同；没有生效订阅的用户不在结果里（按 0）。
func (s *Service) LiveSubscriptionCounts(ctx context.Context, userIDs []uuid.UUID, now time.Time) (map[uuid.UUID]int, error) {
	counts, err := s.repo.LiveSubscriptionCounts(ctx, userIDs, now)
	return counts, subErr(err)
}
