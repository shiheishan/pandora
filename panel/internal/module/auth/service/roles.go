package service

import (
	"context"
	"sort"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var errLastAdmin = errs.Newf(errs.Conflict, "必须保留至少一个可登录的有效管理员")

var (
	_ identitysvc.SessionRevoker = (*Service)(nil)
	_ identitysvc.AdminGuard     = (*Service)(nil)
)

// RevokeUser 吊销该用户全部域的会话（exceptSessionID 非空时保留），供 identity 在改密等事务里调用。
func (s *Service) RevokeUser(ctx context.Context, userID uuid.UUID, exceptSessionID string) error {
	return s.revokeSessions(ctx, userID, exceptSessionID, revokeAll)
}

func (s *Service) revokeSessions(ctx context.Context, uid uuid.UUID, except, aud string) error {
	if _, err := s.sessions.revokeUser(ctx, uid, except, aud); err != nil {
		return errs.Wrap(errs.Unavailable, err)
	}
	tx.AfterCommit(ctx, "auth:revoke:"+uid.String()+":"+aud, func(ctx context.Context) {
		if _, err := s.sessions.revokeUser(ctx, uid, except, aud); err != nil {
			plog.From(ctx).Warn("提交后再次吊销会话失败",
				zap.String("user_id", uid.String()), zap.Error(err))
		}
	})
	return nil
}

// EnsureNotLastAdmin 停用用户前检查：该用户是最后一个有效管理员时返回冲突。
func (s *Service) EnsureNotLastAdmin(ctx context.Context, userID uuid.UUID) error {
	if err := s.store.LockAdminSet(ctx); err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	holders, err := s.store.PermanentHolders(ctx, AdminPermission)
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	var others []uuid.UUID
	isHolder := false
	for _, id := range holders {
		if id == userID {
			isHolder = true
			continue
		}
		others = append(others, id)
	}
	if !isHolder {
		return nil // 不是有效管理员，停用它不影响管理员集合
	}
	if len(others) == 0 {
		return errLastAdmin
	}
	users, err := s.users.GetMany(ctx, others)
	if err != nil {
		return err
	}
	active := 0
	for _, id := range others {
		if u, ok := users[id]; ok && u.Status == UserStatusActive {
			active++
		}
	}
	if active == 0 {
		return errLastAdmin
	}
	return nil
}

func (s *Service) activeAdminExists(ctx context.Context) (bool, error) {
	holders, err := s.store.PermanentHolders(ctx, AdminPermission)
	if err != nil {
		return false, errs.Wrap(errs.Internal, err)
	}
	if len(holders) == 0 {
		return false, nil
	}
	users, err := s.users.GetMany(ctx, holders)
	if err != nil {
		return false, err
	}
	for _, id := range holders {
		if u, ok := users[id]; ok && u.Status == UserStatusActive {
			return true, nil
		}
	}
	return false, nil
}

// GrantRole 授予角色（pandora admin grant），并在事务提交后吊销该用户的门户会话。
func (s *Service) GrantRole(ctx context.Context, userID uuid.UUID, roleCode string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		role, ok, err := s.store.RoleByCode(ctx, roleCode)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if !ok {
			return errs.Newf(errs.NotFound, "角色不存在")
		}
		if _, err := s.users.Get(ctx, userID); err != nil {
			return err
		}
		if err := s.store.LockAdminSet(ctx); err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if err := s.store.UpsertUserRole(ctx, userID, role.ID, actorID(ctx)); err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "adminctl.role_granted",
			ResourceType: "user",
			ResourceID:   &userID,
			Data:         map[string]any{"role": roleCode},
		}); err != nil {
			return err
		}
		return s.revokeSessions(ctx, userID, "", revokePublic)
	})
}

// RevokeRole 收回角色（pandora admin revoke）；若用户不再持有任何角色则吊销后台会话。
func (s *Service) RevokeRole(ctx context.Context, userID uuid.UUID, roleCode string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		role, ok, err := s.store.RoleByCode(ctx, roleCode)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if !ok {
			return errs.Newf(errs.NotFound, "角色不存在")
		}
		if err := s.store.LockAdminSet(ctx); err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		deleted, err := s.store.DeleteUserRole(ctx, userID, role.ID)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if !deleted {
			return errs.Newf(errs.NotFound, "该用户并未持有这个角色")
		}
		okAdmin, err := s.activeAdminExists(ctx)
		if err != nil {
			return err
		}
		if !okAdmin {
			return errLastAdmin
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "adminctl.role_revoked",
			ResourceType: "user",
			ResourceID:   &userID,
			Data:         map[string]any{"role": roleCode},
		}); err != nil {
			return err
		}
		still, err := s.store.HasActiveRole(ctx, userID)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if !still {
			return s.revokeSessions(ctx, userID, "", revokeAdmin)
		}
		return nil
	})
}

// ListAdmins 列出持有后台角色的用户（pandora admin list），按邮箱升序。
func (s *Service) ListAdmins(ctx context.Context) ([]AdminSummary, error) {
	rows, err := s.store.RoleHolders(ctx)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	users, err := s.users.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]AdminSummary, 0, len(rows))
	for _, row := range rows {
		var roles []string
		if row.Roles != "" {
			roles = strings.Split(row.Roles, ",")
		}
		email := ""
		status := ""
		if u, ok := users[row.UserID]; ok {
			email = u.Email
			status = u.Status
		}
		out = append(out, AdminSummary{
			UserID:      row.UserID,
			Email:       email,
			Status:      status,
			Roles:       roles,
			Permissions: row.Permissions,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Email < out[j].Email
	})
	return out, nil
}

// Roles 返回全部角色及权限数（pandora admin roles）。
func (s *Service) Roles(ctx context.Context) ([]RoleSummary, error) {
	rows, err := s.store.Roles(ctx)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	out := make([]RoleSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, RoleSummary{
			Code:        row.Code,
			Name:        row.Name,
			Permissions: row.Permissions,
		})
	}
	return out, nil
}

// HasAdmin 报告是否存在至少一名可登录的有效管理员。
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	return s.activeAdminExists(ctx)
}
