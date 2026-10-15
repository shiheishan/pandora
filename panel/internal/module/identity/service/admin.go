package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// ListUsers 后台用户列表（总数与一页），按注册时间倒序。
func (s *Service) ListUsers(ctx context.Context, f UserFilter, limit, offset int) ([]UserRow, int64, error) {
	q := repo.UserListQuery{GroupID: f.GroupID, NoGroup: f.NoGroup}
	query := strings.TrimSpace(f.Query)
	if query != "" {
		q.Pattern = repo.LikePattern(query)
		if id, err := uuid.Parse(query); err == nil {
			q.ExactID = &id
		}
	}
	if len(f.Statuses) > 0 {
		for _, st := range f.Statuses {
			if st != StatusActive && st != StatusSuspended && st != StatusBanned && st != StatusDeleted {
				return nil, 0, errs.Invalid(map[string]string{"status": "状态只能是 active、suspended、banned、deleted"})
			}
		}
		q.Statuses = f.Statuses
	}
	if f.SubState != "" {
		if f.SubState != "active" && f.SubState != "expired" && f.SubState != "none" {
			return nil, 0, errs.Invalid(map[string]string{"sub_state": "订阅状态只能是 active、expired 或 none"})
		}
		subFilter, subIDs, subExclude, err := s.subIDs(ctx, SubFilter{State: f.SubState})
		if err != nil {
			return nil, 0, err
		}
		q.SubFilter = subFilter
		q.SubIDs = subIDs
		q.SubExclude = subExclude
	}
	total, err := s.repo.CountUsers(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.repo.ListUsers(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]UserRow, 0, len(rows))
	for _, r := range rows {
		gn := ""
		if r.GroupName != nil {
			gn = *r.GroupName
		}
		out = append(out, UserRow{
			ID:          r.ID,
			Email:       r.Email,
			DisplayName: r.DisplayName,
			Status:      r.Status,
			RiskLevel:   r.RiskLevel,
			GroupID:     r.UserGroupID,
			GroupName:   gn,
			CreatedAt:   r.CreatedAt,
			LastLoginAt: r.LastLoginAt,
		})
	}
	if err := s.enrichRows(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// UserDetail 后台用户详情里 identity 负责的部分；不存在回 404。
func (s *Service) UserDetail(ctx context.Context, id uuid.UUID) (UserDetail, error) {
	rows, err := s.repo.ListUsers(ctx, repo.UserListQuery{ExactID: &id}, 1, 0)
	if err != nil {
		return UserDetail{}, err
	}
	if len(rows) == 0 {
		return UserDetail{}, errs.New(errs.NotFound)
	}
	r := rows[0]
	gn := ""
	if r.GroupName != nil {
		gn = *r.GroupName
	}
	row := UserRow{
		ID:          r.ID,
		Email:       r.Email,
		DisplayName: r.DisplayName,
		Status:      r.Status,
		RiskLevel:   r.RiskLevel,
		GroupID:     r.UserGroupID,
		GroupName:   gn,
		CreatedAt:   r.CreatedAt,
		LastLoginAt: r.LastLoginAt,
	}
	d := UserDetail{
		UserRow:       row,
		EmailVerified: r.EmailVerifiedAt != nil,
	}
	if d.IsAdmin, err = s.isStaff(ctx, id); err != nil {
		return UserDetail{}, err
	}
	if ref, ok, err := s.repo.Referrer(ctx, id); err != nil {
		return UserDetail{}, err
	} else if ok {
		d.Referrer = &UserRef{ID: ref.ID, Email: ref.Email}
	}
	d.ReferralCount, err = s.repo.ReferralCount(ctx, id)
	if err != nil {
		return UserDetail{}, err
	}
	d.AdminNote = r.AdminNote
	d.DeletedAt = r.DeletedAt
	if err := s.enrichDetail(ctx, &d); err != nil {
		return UserDetail{}, err
	}
	return d, nil
}

// SetStatus 停用、封禁或恢复一个账号：不能改自己；有「最后一个管理员」保护，停用与封禁同事务吊销全部会话。
func (s *Service) SetStatus(ctx context.Context, userID uuid.UUID, status, reason string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	if status != StatusActive && status != StatusSuspended && status != StatusBanned {
		return errs.Invalid(map[string]string{"status": "状态只能是 active、suspended、banned"})
	}
	reason, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(reason) > 500 {
		return errs.Invalid(map[string]string{"reason": "原因最多 500 字"})
	}
	if status != StatusActive && reason == "" {
		return errs.Invalid(map[string]string{"reason": "停用或封禁必须填写原因"})
	}
	if userID == p.UserID {
		return errs.Newf(errs.Conflict, "不能变更自己的账号状态")
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		u, ok, err := s.repo.LockUser(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if u.Status == StatusDeleted {
			return errAccountDeleted()
		}
		if u.Status == status {
			return nil
		}
		if status != StatusActive {
			guard, err := s.needAdminGuard()
			if err != nil {
				return err
			}
			if err := guard.EnsureNotLastAdmin(ctx, userID); err != nil {
				return err
			}
		}
		if err := s.repo.SetUserStatus(ctx, userID, status); err != nil {
			return err
		}
		s.notifyRoster(ctx, userID)
		if status != StatusActive {
			revoker, err := s.needRevoker()
			if err != nil {
				return err
			}
			if err := revoker.RevokeUser(ctx, userID, ""); err != nil {
				return err
			}
		}
		data := map[string]any{
			"before":           u.Status,
			"status":           status,
			"sessions_revoked": status != StatusActive,
		}
		if reason != "" {
			data["reason"] = reason
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionStatusChange,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data:         data,
		})
	})
}

// AdminResetPassword 管理员替用户设新口令：不能改自己（自己走改密）；吊销该用户全部会话；新口令不回显不记录。
func (s *Service) AdminResetPassword(ctx context.Context, userID uuid.UUID, newPassword, reason string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	if userID == p.UserID {
		return errs.Newf(errs.BadRequest, "改自己的密码请用「修改密码」，那里会先验证当前密码")
	}
	reason, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(reason) > 500 {
		return errs.Invalid(map[string]string{"reason": "原因最多 500 字"})
	}
	staff, err := s.isStaff(ctx, userID)
	if err != nil {
		return err
	}
	if err := validatePassword(newPassword, staff); err != nil {
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
			return errs.New(errs.NotFound)
		}
		if u.Status == StatusDeleted {
			return errAccountDeleted()
		}
		// 事务里再查一次：算口令前后对方可能刚被设为管理员
		staff2, err := s.isStaff(ctx, userID)
		if err != nil {
			return err
		}
		if err := validatePassword(newPassword, staff2); err != nil {
			return err
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
		data := map[string]any{
			"target_email":     u.Email,
			"sessions_revoked": true,
		}
		if reason != "" {
			data["reason"] = reason
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionPasswordResetByAdmin,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data:         data,
		})
	})
}

// AssignGroup 把用户放进某个分组（groupID 为 nil 则移出）；用户或分组不存在回 422，分组已停用回 409（用户本来就在这个组里时不拦）。
func (s *Service) AssignGroup(ctx context.Context, userID uuid.UUID, groupID *uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		u, ok, err := s.repo.LockUser(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.ValidationFailed, "用户或分组不存在")
		}
		if u.Status == StatusDeleted {
			return errAccountDeleted()
		}
		// 停用的组不收新成员；本来就在这个组里再提交一次同一个组不算新成员（幂等，不报错）
		if groupID != nil && (u.UserGroupID == nil || *u.UserGroupID != *groupID) {
			st, ok, err := s.repo.GroupStatus(ctx, *groupID)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Newf(errs.ValidationFailed, "用户或分组不存在")
			}
			if st == GroupDisabled {
				return errs.Newf(errs.Conflict, "这个分组已停用，不能再加入新成员")
			}
		}
		ok, err = s.repo.SetUserGroup(ctx, userID, groupID)
		if err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return errs.Newf(errs.ValidationFailed, "用户或分组不存在")
			}
			return err
		}
		if !ok {
			return errs.Newf(errs.ValidationFailed, "用户或分组不存在")
		}
		s.notifyRoster(ctx, userID)
		gid := ""
		if groupID != nil {
			gid = groupID.String()
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionGroupChanged,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data:         map[string]any{"group_id": gid},
		})
	})
}
