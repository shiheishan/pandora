package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// RosterNotifier 由 node 模块实现（P5）：用户的账号状态或用户组变了（影响节点名单与线路），在事务提交后调用。
// 实现必须很快返回（推进纪元、排任务），不能回调本模块的写方法。没注册时什么都不做。
type RosterNotifier interface {
	UsersChanged(ctx context.Context, userIDs []uuid.UUID)
}

// UseRosterNotifier 只在启动装配时调用（不并发安全）。
func (s *Service) UseRosterNotifier(n RosterNotifier) { s.roster = n }

// notifyRoster 登记提交后通知名单变化；同一事务里同一用户只通知一次，回滚不发。
func (s *Service) notifyRoster(ctx context.Context, userID uuid.UUID) {
	tx.AfterCommit(ctx, "identity:roster:"+userID.String(), func(ctx context.Context) {
		if s.roster != nil {
			s.roster.UsersChanged(ctx, []uuid.UUID{userID})
		}
	})
}
