package service

import (
	"context"
	"sort"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 管理员授予与取消的审计动作。
const (
	ActionAdminGranted = "adminctl.admin_granted"
	ActionAdminRevoked = "adminctl.admin_revoked"
)

var errLastAdmin = errs.Newf(errs.Conflict, "必须保留至少一个可登录的有效管理员")

var (
	_ identitysvc.SessionRevoker = (*Service)(nil)
	_ identitysvc.AdminGuard     = (*Service)(nil)
	_ identitysvc.StaffGuard     = (*Service)(nil)
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
			// 事务里已吊销过一次；这次补扫失败只记错误，不另排重试
			plog.From(ctx).Error("提交后再次吊销会话失败",
				zap.String("user_id", uid.String()), zap.Error(err))
		}
	})
	return nil
}

// IsStaff 报告用户是否在管理员名单里（不看账号状态）；identity 用它定 12 位口令规则、拦门户找回密码。实现 identity.StaffGuard。
func (s *Service) IsStaff(ctx context.Context, userID uuid.UUID) (bool, error) {
	ok, err := s.store.IsAdmin(ctx, userID)
	if err != nil {
		return false, errs.Wrap(errs.Internal, err)
	}
	return ok, nil
}

// activeAdmins 返回名单里账号 active 的管理员 ID（except 除外）。
func (s *Service) activeAdmins(ctx context.Context, except uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.store.Admins(ctx)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if r.UserID != except {
			ids = append(ids, r.UserID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	users, err := s.users.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, id := range ids {
		if u, ok := users[id]; ok && u.Status == UserStatusActive {
			out = append(out, id)
		}
	}
	return out, nil
}

// EnsureNotLastAdmin 停用或封禁用户前在调用方事务里检查：该用户是管理员、且除他之外没有可登录的管理员时返回冲突。实现 identity.AdminGuard。
func (s *Service) EnsureNotLastAdmin(ctx context.Context, userID uuid.UUID) error {
	if err := s.store.LockAdminSet(ctx); err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	admin, err := s.store.IsAdmin(ctx, userID)
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	if !admin {
		return nil // 不是管理员，停用它不影响管理员集合
	}
	others, err := s.activeAdmins(ctx, userID)
	if err != nil {
		return err
	}
	if len(others) == 0 {
		return errLastAdmin
	}
	return nil
}

// GrantAdmin 把用户设为管理员（pandora admin grant / create）。已是管理员时什么都不做、不写审计；
// 新授予时同事务吊销其门户会话并推进凭据纪元（管理员不能登门户）。
func (s *Service) GrantAdmin(ctx context.Context, userID uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if _, err := s.users.Get(ctx, userID); err != nil {
			return err
		}
		if err := s.store.LockAdminSet(ctx); err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		inserted, err := s.store.InsertAdmin(ctx, userID, actorID(ctx))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if !inserted {
			return nil
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionAdminGranted,
			ResourceType: "user",
			ResourceID:   &userID,
		}); err != nil {
			return err
		}
		return s.revokeSessions(ctx, userID, "", revokePublic)
	})
}

// RevokeAdmin 取消用户的管理员身份（pandora admin revoke）。本来就不是管理员时什么都不做、不写审计；
// 取消后没有可登录的管理员时整体回滚；成功时同事务吊销其后台会话（提交后再补扫一遍）。
func (s *Service) RevokeAdmin(ctx context.Context, userID uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.store.LockAdminSet(ctx); err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		deleted, err := s.store.DeleteAdmin(ctx, userID)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if !deleted {
			return nil
		}
		left, err := s.activeAdmins(ctx, userID)
		if err != nil {
			return err
		}
		if len(left) == 0 {
			return errLastAdmin
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionAdminRevoked,
			ResourceType: "user",
			ResourceID:   &userID,
		}); err != nil {
			return err
		}
		return s.revokeSessions(ctx, userID, "", revokeAdmin)
	})
}

// ListAdmins 列出管理员名单（pandora admin list），按邮箱升序。
func (s *Service) ListAdmins(ctx context.Context) ([]AdminSummary, error) {
	rows, err := s.store.Admins(ctx)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	users := map[uuid.UUID]identitysvc.User{}
	if len(ids) > 0 {
		if users, err = s.users.GetMany(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := make([]AdminSummary, 0, len(rows))
	for _, r := range rows {
		a := AdminSummary{UserID: r.UserID, CreatedAt: r.CreatedAt}
		if u, ok := users[r.UserID]; ok {
			a.Email = u.Email
			a.Status = u.Status
		}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out, nil
}

// HasAdmin 报告是否存在至少一名可登录的有效管理员（在名单里且账号 active）。
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	ids, err := s.activeAdmins(ctx, uuid.Nil)
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}
