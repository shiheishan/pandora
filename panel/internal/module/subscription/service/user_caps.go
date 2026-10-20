package service

import (
	"context"

	"github.com/google/uuid"
)

// HeldCountsByUser 是每个用户手里还算「一份」的订阅数（只读、不加锁）：order 下单时按它检查「每人订阅数上限」。
// 口径是 StateAt 不为 StateEnded 的订阅，即生效中的加过期但还能续回的（用户 10-20 定：能续回的也算一份，
// 否则先新开、再续回旧的就能绕过上限）。一份都没有的用户不在结果里（按 0）。
func (s *Service) HeldCountsByUser(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	counts, err := s.repo.HeldCountsByUser(ctx, userIDs)
	return counts, subErr(err)
}
