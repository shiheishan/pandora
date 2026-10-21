package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
	"github.com/google/uuid"
)

// DeleteUserResult 是注销的结果。
type DeleteUserResult struct {
	ShortID                 string // DeletedShortID(id)，界面显示「已注销 #<ShortID>」
	TerminatedSubscriptions int
}

// AdminSetEmail 后台改用户邮箱：规整成小写并校验；撞号 409；已注销 409；同值直接返回；改后邮箱验证时间清空；写审计。
// 登录标识换了，旧邮箱上的凭据一并作废（与改口令同口径）：作废发往旧邮箱的找回码，吊销全部会话并推进凭据纪元
// （邮件登录链接按纪元失效）。否则「旧邮箱被盗、请管理员改邮箱」时，攻击者还能用旧邮箱收到的码或链接接管账号。
func (s *Service) AdminSetEmail(ctx context.Context, userID uuid.UUID, email, reason string) error {
	if _, err := principal(ctx); err != nil {
		return err
	}

	r, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(r) < 5 || utf8.RuneCountInString(r) > 500 {
		return errs.Invalid(map[string]string{"reason": "请写清原因，5 到 500 个字"})
	}

	email = normalizeEmail(email)
	if !ValidEmail(email) || reservedEmail(email) {
		return errEmailInvalid()
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
		if err := s.repo.ExpireCodes(ctx, PurposePasswordReset, s.targetHash(u.Email)); err != nil {
			return err
		}
		revoker, err := s.needRevoker()
		if err != nil {
			return err
		}
		if err := revoker.RevokeUser(ctx, userID, ""); err != nil {
			return err
		}
		// 审计只追加、注销后抹不掉：不记明文邮箱，只记打码值；要对照是不是同一个邮箱，比 HMAC
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionEmailChanged,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"before":           MaskEmail(u.Email),
				"after":            MaskEmail(email),
				"before_hmac":      s.auditEmailHMAC(u.Email),
				"after_hmac":       s.auditEmailHMAC(email),
				"sessions_revoked": true,
				"reason":           r,
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

// DeleteUser 注销并匿名化用户：取消全部待付订单（经 order，作废在途支付意图）、停掉全部订阅、作废名下未打款的佣金并驳回申请中的提现（经 commission；有已批准未打款的提现时 409 拒绝、整笔回滚）、解绑 Telegram、吊销全部会话，邮箱换成不可登录的占位值、清掉显示名与备注、删口令、停用他的邀请码、作废找回密码验证码。
// 不可撤销；订单、账本、佣金与提现记录、审计、邀请关系原样保留（作废是追加冲回分录、改状态，不删行）。同事务发插件事件 user.deleted（只带用户 id 与时间）。返回停掉的订阅份数。
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
		// 锁序：先用户行（FOR NO KEY UPDATE），再由 CloseUserSubscriptions 按 id 升序锁订阅行，与注册发试用、
		// subscription 各写路径一致（subscription 从不显式锁 users 行）。改邮箱这一列有唯一索引，UPDATE 会把行锁升到
		// FOR UPDATE；放在订阅都停完之后再做，用户自己操作订阅（先锁订阅、再插 actor 指向他的事件行）不会与这里互等。
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

		// 先拿佣金咨询锁（与转余额、提现申请同一把）：下面取消待付单会锁他的余额科目，转余额是拿着这把锁再锁余额，
		// 先拿它才不互等（详见 commission 的 VoidForDeletedUser）
		voider, err := s.needCommissionVoider()
		if err != nil {
			return err
		}
		if err := voider.LockForDeletion(ctx, userID); err != nil {
			return err
		}

		// 先取消待付订单、再停订阅：付款结算是先锁订单再动订阅，同序才不互等；结算抢先提交的单，
		// 这里重读后不再是待付，它开出的订阅随后由 CloseUserSubscriptions 一并停掉。
		canceller, err := s.needOrderCanceller()
		if err != nil {
			return err
		}
		cancelled, err := canceller.CancelUserPending(ctx, userID)
		if err != nil {
			return err
		}

		closer, err := s.needCloser()
		if err != nil {
			return err
		}
		n, err := closer.CloseUserSubscriptions(ctx, userID, r)
		if err != nil {
			return err
		}

		// 作废名下未打款的佣金、驳回申请中的提现（经 commission；有已批准未打款的提现回 409，整笔回滚）。放在停完订阅之后：作废要锁平台收入科目，
		// 结算是先锁库存、券、订阅再锁平台收入，同序才不互等；放在改占位邮箱之前：那时用户行还只是 FOR NO KEY UPDATE，
		// 与结算计提插条目时外键加的 KEY SHARE 相容，拿着平台收入的结算不会反过来等这里的用户行。
		voided, err := voider.VoidForDeletedUser(ctx, userID)
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
				"reason":                     r,
				"short_id":                   DeletedShortID(userID),
				"terminated_subscriptions":   n,
				"cancelled_orders":           cancelled,
				"disabled_invites":           invites,
				"voided_commission_entries":  voided.VoidedEntries,
				"voided_commission_pending":  voided.VoidedPending.StringFixed(2),
				"forfeited_commission":       voided.Forfeited.StringFixed(2),
				"rejected_withdrawals":       voided.RejectedWithdrawals,
				"rejected_withdrawal_amount": voided.RejectedAmount.StringFixed(2),
			},
		}); err != nil {
			return err
		}
		// 插件事件只带用户 id 与注销时间：账号已匿名化，不再外发邮箱等个人信息
		if err := pluginevents.Emit(ctx, s.events, "user.deleted", "user.deleted:"+userID.String(), map[string]any{
			"event": "user.deleted", "user_id": userID.String(), "deleted_at": now.UTC().Format(time.RFC3339),
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
