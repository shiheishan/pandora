package service

import (
	"context"

	"github.com/google/uuid"
)

// VersionsInUse 返回有生效订阅（status = 'active' 且 current_period_end > now）的套餐版本集合（只含为 true 的键）。
// 输入为空时不查库，直接返回空 map。由 node 模块消费。
func (s *Service) VersionsInUse(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if len(versionIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	ids, err := s.repo.ActivePlanVersionIDs(ctx, versionIDs, s.clock())
	if err != nil {
		return nil, subErr(err)
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
