package service

import (
	"context"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// ActionPasswordResetByCLI 是命令行重置管理员口令的审计动作。
const ActionPasswordResetByCLI = "admin.password_reset"

// ResetStaffPassword 是 pandora admin reset-password：在服务器本地重置后台人员的口令并吊销其全部会话。
// 没有登录主体，审计记 system；只对后台人员生效（普通用户走找回密码），不改账号状态、不改管理员名单。
func (s *Service) ResetStaffPassword(ctx context.Context, userID uuid.UUID, newPassword string) error {
	if err := validatePassword(newPassword, true); err != nil {
		return err
	}
	release, err := s.hasher.acquire(ctx, authn.AudienceAdmin)
	if err != nil {
		return err
	}
	phc, err := hashPHC(newPassword)
	release()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		u, ok, err := s.repo.LockUser(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.NotFound, "账号不存在")
		}
		staff, err := s.isStaff(ctx, userID)
		if err != nil {
			return err
		}
		if !staff {
			return errs.Newf(errs.BadRequest, "该账号不是后台人员；普通用户请走找回密码")
		}
		if err := s.repo.UpsertPassword(ctx, userID, phc); err != nil {
			return err
		}
		revoker, err := s.needRevoker()
		if err != nil {
			return err
		}
		if err := revoker.RevokeUser(ctx, userID, ""); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionPasswordResetByCLI,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data:         map[string]any{"target_email": u.Email, "sessions_revoked": true},
		})
	})
}
