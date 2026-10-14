package service

import (
	"context"
	"sort"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

const (
	// ActionClosedByAccountDeletion 是注销账号时停掉订阅的审计动作。
	ActionClosedByAccountDeletion = "subscription.closed_by_account_deletion"
	// RevokedAccountDeleted 是注销账号时吊销凭据的原因。
	RevokedAccountDeleted = "account_deleted"
)

// CloseUserSubscriptions 停掉一个用户的全部订阅（identity 注销账号时在同一事务里调用）：生效中的改成 expired（到期时间晚于现在的截到现在），
// 续费窗口一律关上（之后只能新购——账号也不能再登录），吊销全部有效链接凭据。返回状态或窗口有变化的份数。提交后通知名单。
func (s *Service) CloseUserSubscriptions(ctx context.Context, userID uuid.UUID, reason string) (int, error) {
	var count int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var actor Actor
		if p, err := principal(ctx); err == nil && p.UserID != uuid.Nil {
			actor = actorFrom(p)
		} else {
			actor = Actor{Kind: ActorSystem}
		}

		subs, err := s.repo.SubscriptionsByUsers(ctx, []uuid.UUID{userID})
		if err != nil {
			return subErr(err)
		}
		if len(subs) == 0 {
			return nil
		}

		// 按 id 升序排序，防死锁
		sort.Slice(subs, func(i, j int) bool {
			return subs[i].ID.String() < subs[j].ID.String()
		})

		var lockedSubs []model.Subscription
		var allSubIDs []uuid.UUID
		for _, sub := range subs {
			m, err := s.lockOwned(ctx, sub.ID, &userID)
			if err != nil {
				return err
			}
			lockedSubs = append(lockedSubs, m)
			allSubIDs = append(allSubIDs, m.ID)
		}

		now := s.clock()
		changedSubs := make(map[uuid.UUID]struct{})
		var expiredIDs []uuid.UUID
		var openWindowIDs []uuid.UUID

		for _, m := range lockedSubs {
			if m.Status == StatusActive {
				cols := map[string]any{}
				if m.CurrentPeriodEnd.After(now) && now.After(m.CurrentPeriodStart) {
					cols["current_period_end"] = now
				}
				if err := s.transition(ctx, m, StatusExpired, cols); err != nil {
					return err
				}
				from := StatusActive
				if err := s.writeEvent(ctx, m.ID, EventExpired, &from, StatusExpired, actor, nil, map[string]any{
					"reason":       "account_deleted",
					"previous_end": m.CurrentPeriodEnd,
				}); err != nil {
					return err
				}
				expiredIDs = append(expiredIDs, m.ID)
				changedSubs[m.ID] = struct{}{}
			}
			if m.RenewalClosedAt == nil {
				openWindowIDs = append(openWindowIDs, m.ID)
			}
		}

		closedIDs, err := s.repo.CloseRenewalWindows(ctx, openWindowIDs, now)
		if err != nil {
			return subErr(err)
		}
		for _, id := range closedIDs {
			changedSubs[id] = struct{}{}
			s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, id, userID)
			from := StatusExpired
			if err := s.writeEvent(ctx, id, EventRenewalClosed, &from, StatusExpired, actor, nil, map[string]any{
				"reason": "account_deleted",
			}); err != nil {
				return err
			}
		}

		revokedCreds, err := s.repo.RevokeCredentialsOnClose(ctx, allSubIDs, now, RevokedAccountDeleted)
		if err != nil {
			return subErr(err)
		}
		for _, c := range revokedCreds {
			s.rtChanged(ctx, tableCredentials, realtime.OpUpdate, c.ID, c.UserID)
		}

		n := len(changedSubs)
		if n > 0 {
			if err := s.audit.Write(ctx, auditsvc.Event{
				Action:       ActionClosedByAccountDeletion,
				ResourceType: "user",
				ResourceID:   &userID,
				Data: map[string]any{
					"reason":              reason,
					"subscriptions":       n,
					"revoked_credentials": len(revokedCreds),
				},
			}); err != nil {
				return err
			}
		}

		if len(expiredIDs) > 0 {
			s.notifyRoster(ctx, expiredIDs...)
		}
		count = n
		return nil
	})
	return count, err
}
