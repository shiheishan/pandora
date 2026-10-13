package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// livenessWindow 是巡检每轮回看的宽度：比 30 秒的周期宽，相邻两轮有重叠，周期抖动时不漏（重叠处可能推两次）。
const livenessWindow = 40 * time.Second

// beatFlipsOnline 报告这一拍心跳是否把服务器从「离线」拉回「在线」：之前从没心跳，或上一次心跳距这一拍已达 NodeStaleAfter。
func beatFlipsOnline(prev *time.Time, at time.Time) bool {
	if prev == nil {
		return true
	}
	return at.Sub(*prev) >= NodeStaleAfter
}

// PatrolLiveness 给心跳刚跨过 NodeStaleAfter 或 nodeDeliveryFreshWindow 的服务器各推一条 servers UPDATE，返回推了几台（去重后）。
// 由周期任务 node:liveness 每 30 秒调；不开事务，推送立即发出。
func (s *Service) PatrolLiveness(ctx context.Context) (int, error) {
	now := s.now()
	seen := make(map[uuid.UUID]struct{})
	order := make([]uuid.UUID, 0)
	for _, w := range []time.Duration{NodeStaleAfter, nodeDeliveryFreshWindow} {
		ids, err := s.repo.ServersBeatBetween(ctx, now.Add(-w-livenessWindow), now.Add(-w))
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			order = append(order, id)
		}
	}
	for _, id := range order {
		s.serverChanged(ctx, realtime.OpUpdate, id)
	}
	return len(order), nil
}
