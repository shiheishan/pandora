package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// DeleteUserResult 是注销的结果。
type DeleteUserResult struct {
	ShortID                 string // DeletedShortID(id)，界面显示「已注销 #<ShortID>」
	TerminatedSubscriptions int
}

// AdminSetEmail 后台改用户邮箱：规整成小写并校验；撞号 409；已注销 409；同值直接返回；改后邮箱验证时间清空；写审计。
func (s *Service) AdminSetEmail(ctx context.Context, userID uuid.UUID, email, reason string) error {
	if _, err := principal(ctx); err != nil {
		return err
	}

	r, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(r) < 5 || utf8.RuneCountInString(r) > 500 {
		return errs.Invalid(map[string]string{"reason": "请写清原因，5 到 500 个字"})
	}

	email = normalizeEmail(email)
	if !ValidEmail(email) {
		return errs.Invalid(map[string]string{"email": "邮箱格式不正确"})
	}
	if strings.HasSuffix(email, "@deleted.invalid") {
		return errs.Invalid(map[string]string{"email": "邮箱格式不正确"})
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
		if strings.EqualFold(u.Email, email) {
			return nil
		}
		if err := s.repo.SetUserEmail(ctx, userID, email); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "这个邮箱已被别的账号使用")
			}
			return err
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionEmailChanged,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"before": u.Email,
				"after":  email,
				"reason": r,
			},
		})
	})
}

// AdminSetNote 后台改显示名与备注：nil 表示不改；去首尾空白后为空串则存 NULL；显示名最多 64 字、备注最多 2000 字；写审计（只记改了哪些字段与长度，不记备注全文）。
func (s *Service) AdminSetNote(ctx context.Context, userID uuid.UUID, displayName, adminNote *string) error {
	if _, err := principal(ctx); err != nil {
		return err
	}

	if displayName == nil && adminNote == nil {
		return errs.Invalid(map[string]string{"display_name": "没有要改的内容"})
	}

	var dn, note *string
	var fields []string

	if displayName != nil {
		v, ok := cleanText(*displayName)
		if !ok {
			return errs.Invalid(map[string]string{"display_name": "包含不允许的字符"})
		}
		if utf8.RuneCountInString(v) > 64 {
			return errs.Invalid(map[string]string{"display_name": "显示名最多 64 个字"})
		}
		if v == "" {
			dn = nil
		} else {
			dn = &v
		}
		fields = append(fields, "display_name")
	}

	var noteLen *int
	if adminNote != nil {
		v, ok := cleanText(*adminNote)
		if !ok {
			return errs.Invalid(map[string]string{"admin_note": "包含不允许的字符"})
		}
		if utf8.RuneCountInString(v) > 2000 {
			return errs.Invalid(map[string]string{"admin_note": "备注最多 2000 个字"})
		}
		if v == "" {
			note = nil
			noteLen = nil
		} else {
			note = &v
			n := utf8.RuneCountInString(v)
			noteLen = &n
		}
		fields = append(fields, "admin_note")
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

		finalDN := u.DisplayName
		if displayName != nil {
			finalDN = dn
		}
		finalNote := u.AdminNote
		if adminNote != nil {
			finalNote = note
		}

		if err := s.repo.SetUserNote(ctx, userID, finalDN, finalNote); err != nil {
			return err
		}

		data := map[string]any{"fields": fields}
		if adminNote != nil && noteLen != nil {
			data["admin_note_length"] = *noteLen
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionNoteChanged,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data:         data,
		})
	})
}

// DeleteUser 注销并匿名化用户：停掉全部订阅、解绑 Telegram、吊销全部会话，邮箱换成不可登录的占位值、清掉显示名与备注、删口令、停用他的邀请码、作废找回密码验证码。
// 不可撤销；订单、账本、佣金、审计、邀请关系原样保留。返回停掉的订阅份数。
func (s *Service) DeleteUser(ctx context.Context, userID uuid.UUID, reason string) (DeleteUserResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return DeleteUserResult{}, err
	}

	r, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(r) < 5 || utf8.RuneCountInString(r) > 500 {
		return DeleteUserResult{}, errs.Invalid(map[string]string{"reason": "请写清原因，5 到 500 个字"})
	}
	if userID == p.UserID {
		return DeleteUserResult{}, errs.Newf(errs.Conflict, "不能注销自己的账号")
	}

	var result DeleteUserResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
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

		staff, err := s.isStaff(ctx, userID)
		if err != nil {
			return err
		}
		if staff {
			return errs.Newf(errs.Conflict, "管理员账号不能注销，先在命令行取消管理员")
		}

		closer, err := s.needCloser()
		if err != nil {
			return err
		}
		n, err := closer.CloseUserSubscriptions(ctx, userID, r)
		if err != nil {
			return err
		}

		tg, err := s.needTelegramUnbinder()
		if err != nil {
			return err
		}
		if err := tg.UnbindTelegram(ctx, userID); err != nil {
			if errs.As(err).Code != errs.NotFound {
				return err
			}
		}

		revoker, err := s.needRevoker()
		if err != nil {
			return err
		}
		if err := revoker.RevokeUser(ctx, userID, ""); err != nil {
			return err
		}

		target := s.targetHash(u.Email)
		if err := s.repo.ExpireCodes(ctx, PurposePasswordReset, target); err != nil {
			return err
		}

		now := s.now()
		if err := s.repo.AnonymizeUser(ctx, userID, DeletedEmail(userID), now); err != nil {
			return err
		}
		if err := s.repo.DeletePassword(ctx, userID); err != nil {
			return err
		}
		invites, err := s.repo.DisableOwnerInvites(ctx, userID)
		if err != nil {
			return err
		}

		s.notifyRoster(ctx, userID)

		if err := s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionDeleted,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"reason":                   r,
				"short_id":                 DeletedShortID(userID),
				"terminated_subscriptions": n,
				"disabled_invites":         invites,
			},
		}); err != nil {
			return err
		}

		result = DeleteUserResult{
			ShortID:                 DeletedShortID(userID),
			TerminatedSubscriptions: n,
		}
		return nil
	})
	if err != nil {
		return DeleteUserResult{}, err
	}
	return result, nil
}
