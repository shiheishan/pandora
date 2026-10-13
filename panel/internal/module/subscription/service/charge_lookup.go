package service

import (
	"context"

	"github.com/google/uuid"
)

// SubscriptionIDsByNodeUIDs 批量把节点协议里的用户号（node_uid）换成订阅 id（一条 SQL）；找不到的 uid 不在结果里。
// 节点上报在线 IP 时用：node 的在线表按订阅记。
func (s *Service) SubscriptionIDsByNodeUIDs(ctx context.Context, uids []int64) (map[int64]uuid.UUID, error) {
	if len(uids) == 0 {
		return map[int64]uuid.UUID{}, nil
	}
	out, err := s.repo.SubscriptionIDsByNodeUIDs(ctx, uids)
	if err != nil {
		return nil, subErr(err)
	}
	return out, nil
}
