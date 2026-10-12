package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// errTargetOutranksActor 是越级管理的拒绝：目标账号持有操作者没有的权限。
var errTargetOutranksActor = errs.Newf(errs.Forbidden, "目标账号持有你没有的权限，不能由你重置密码或变更状态")

// IsStaff 报告用户是否持有后台角色绑定（含已过期的绑定）；identity 用它定口令策略、拦门户找回密码。实现 identity.StaffGuard。
func (s *Service) IsStaff(ctx context.Context, userID uuid.UUID) (bool, error) {
	ok, err := s.store.HasAnyRole(ctx, userID)
	if err != nil {
		return false, errs.Wrap(errs.Internal, err)
	}
	return ok, nil
}

// EnsureCanManage 是越级闸：target 持有 actor 没有的生效权限时返回 403。在调用方事务里实时查库。实现 identity.StaffGuard。
func (s *Service) EnsureCanManage(ctx context.Context, actorID, targetID uuid.UUID) error {
	ok, err := s.store.Covers(ctx, actorID, targetID)
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	if !ok {
		return errTargetOutranksActor
	}
	return nil
}
