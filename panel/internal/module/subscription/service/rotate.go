package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// rotateReason 是换链接时写在旧凭据上的吊销原因。
const rotateReason = "rotated"

// MyRotate 是门户换链接：只给生效中的本人订阅做（不生效回 ErrRotateWhileExpired），旧链接立即失效、节点身份一起换，返回新链接。
func (s *Service) MyRotate(ctx context.Context, subID uuid.UUID) (string, error) {
	p, err := principal(ctx)
	if err != nil {
		return "", err
	}
	d, err := s.needDelivery()
	if err != nil {
		return "", err
	}
	prefix, err := s.pathPrefix(ctx, d)
	if err != nil {
		return "", err
	}
	var token string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		sub, err := s.lockOwned(ctx, subID, &p.UserID)
		if err != nil {
			return err
		}
		if StateAt(sub.Status, sub.CurrentPeriodEnd, sub.RenewalClosedAt, s.clock()) != StateLive {
			return ErrRotateWhileExpired
		}
		if err := s.rotateLocked(ctx, sub, actorFrom(p), "user"); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, ActionLinkRotated, sub.ID, map[string]any{
			"old_revoked":           true,
			"node_password_rotated": true,
		}); err != nil {
			return err
		}
		cred, ok, err := s.repo.ActiveCredential(ctx, sub.ID)
		if err != nil {
			return subErr(err)
		}
		if !ok {
			return errs.Wrap(errs.Internal, errors.New("subscription: 换链接后没有有效凭据"))
		}
		plain, err := s.sealer.Open(cred.TokenEncrypted, CredentialAAD(sub.ID))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		token = string(plain)
		return nil
	})
	if err != nil {
		return "", err
	}
	base, err := s.linkBase(ctx, d)
	if err != nil {
		return "", err
	}
	return linkURL(base, prefix, token), nil
}

// AdminRotate 是后台替用户换链接（不看状态）；reason 去首尾空白后 5–200 字，不含 NUL。返回里没有令牌与链接。
func (s *Service) AdminRotate(ctx context.Context, subID uuid.UUID, reason string) (AdminRotateResult, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return AdminRotateResult{}, err
	}
	reason, err = cleanText(reason, 5, 200, "reason", "请写清为什么要换这条订阅链接，5–200 个字")
	if err != nil {
		return AdminRotateResult{}, err
	}
	// 邮箱在同一事务里读：提交后再读，读失败时链接已经换了、后台却报失败，管理员重试就会再换一次
	var email string
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		sub, err := s.lockForAdmin(ctx, subID)
		if err != nil {
			return err
		}
		emails, err := s.users.Emails(ctx, []uuid.UUID{sub.UserID})
		if err != nil {
			return err
		}
		email = emails[sub.UserID]
		if err := s.rotateLocked(ctx, sub, actorFrom(p), "admin"); err != nil {
			return err
		}
		return s.writeAudit(ctx, ActionLinkRotatedByAdmin, sub.ID, map[string]any{
			"reason":                reason,
			"user_id":               sub.UserID.String(),
			"old_revoked":           true,
			"node_password_rotated": true,
		})
	})
	if err != nil {
		return AdminRotateResult{}, err
	}
	return AdminRotateResult{SubscriptionID: subID, UserEmail: email}, nil
}

// rotateLocked 在调用方事务里换掉一份（已锁住的）订阅的链接：吊销有效凭据（立即失效，不留宽限）、签发新凭据、
// 换 proxy_uuid、写 link_rotated 事件，并登记提交后的名单通知。
func (s *Service) rotateLocked(ctx context.Context, sub model.Subscription, actor Actor, source string) error {
	now := s.clock()
	// 节点身份（proxy_uuid）会一起换，宽限期内旧链接仍能把新的节点身份交给泄露方，故 grace 传 nil、立即失效。
	revoked, err := s.repo.RevokeActiveCredentials(ctx, sub.ID, rotateReason, now, nil)
	if err != nil {
		return subErr(err)
	}
	for _, credID := range revoked {
		s.rtChanged(ctx, tableCredentials, realtime.OpUpdate, credID, sub.UserID)
	}
	if err := s.issueCredential(ctx, sub); err != nil {
		return err
	}
	id, err := s.repo.RotateProxyUUID(ctx, sub.ID)
	if err != nil {
		return subErr(err)
	}
	if id == uuid.Nil {
		return ErrNotFound
	}
	s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, sub.ID, sub.UserID)
	from := sub.Status
	if err := s.writeEvent(ctx, sub.ID, EventLinkRotated, &from, sub.Status, actor, nil, map[string]any{"source": source}); err != nil {
		return err
	}
	s.notifyRoster(ctx, sub.ID)
	return nil
}
