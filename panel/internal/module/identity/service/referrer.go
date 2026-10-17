package service

import (
	"context"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// AdminSetReferrer 后台改某人的邀请人；referrerID 为 nil 表示清除。不能设成自己或自己的下线（含下级的下级）；
// 只影响以后订单的计佣（commission 结算时读当前邀请人），已记的佣金不动。写审计。
func (s *Service) AdminSetReferrer(ctx context.Context, refereeID uuid.UUID, referrerID *uuid.UUID, reason string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	r, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(r) < 5 || utf8.RuneCountInString(r) > 500 {
		return errs.Invalid(map[string]string{"reason": "请写清原因，5 到 500 个字"})
	}
	if referrerID != nil && *referrerID == refereeID {
		return errs.Invalid(map[string]string{"referrer_id": "不能把邀请人设成自己"})
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockReferralGraph(ctx); err != nil {
			return err
		}
		u, ok, err := s.repo.LockUser(ctx, refereeID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if u.Status == StatusDeleted {
			return errAccountDeleted()
		}

		if referrerID != nil {
			ref, ok, err := s.repo.UserByID(ctx, *referrerID)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Invalid(map[string]string{"referrer_id": "邀请人不存在"})
			}
			if ref.Status == StatusDeleted {
				return errs.Invalid(map[string]string{"referrer_id": "邀请人已注销"})
			}
			cyc, err := s.repo.ReferrerChainContains(ctx, *referrerID, refereeID, referrerChainMaxDepth)
			if err != nil {
				return err
			}
			if cyc {
				return errs.Invalid(map[string]string{"referrer_id": "不能设成他自己邀请来的人（含下级的下级）"})
			}
		}

		cur, has, err := s.repo.Referrer(ctx, refereeID)
		if err != nil {
			return err
		}
		var prev *uuid.UUID
		if has {
			prev = &cur.ID
		}

		same := (prev == nil && referrerID == nil) || (prev != nil && referrerID != nil && *prev == *referrerID)
		if same {
			return nil
		}

		change := model.ReferralChange{
			RefereeUserID:          refereeID,
			ReferrerUserID:         referrerID,
			PreviousReferrerUserID: prev,
			ChangedBy:              p.UserID,
			Reason:                 r,
		}
		if err := s.repo.InsertReferralChange(ctx, &change); err != nil {
			return err
		}

		var beforeVal any
		if prev != nil {
			beforeVal = prev.String()
		}
		var afterVal any
		if referrerID != nil {
			afterVal = referrerID.String()
		}

		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionReferrerChanged,
			ResourceType: ResourceUser,
			ResourceID:   &refereeID,
			Data: map[string]any{
				"before": beforeVal,
				"after":  afterVal,
				"reason": r,
			},
		})
	})
}
