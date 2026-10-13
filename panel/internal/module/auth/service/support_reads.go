package service

import (
	"context"

	"github.com/google/uuid"
)

// ActiveAdminIDs 返回可登录的管理员（在名单里且账号 active）；工单的可指派客服目录用。
func (s *Service) ActiveAdminIDs(ctx context.Context) ([]uuid.UUID, error) {
	return s.activeAdmins(ctx, uuid.Nil)
}

// IsActiveAdmin 报告该用户此刻是不是可登录的管理员；工单指派前校验用。
func (s *Service) IsActiveAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	return s.stillAdmin(ctx, userID)
}
