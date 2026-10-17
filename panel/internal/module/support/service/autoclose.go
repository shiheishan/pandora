package service

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// AutoCloseBatch 是一轮自动关闭最多处理的工单数，剩下的下一轮接着关。
const AutoCloseBatch = 200

// AutoCloseScheduled 是定时任务入口（没有登录主体，审计记 system）：后台开了自动关闭时，
// 把「等待用户」且超过设置时长没有更新的工单关掉，返回关掉的张数；没开返回 0。
func (s *Service) AutoCloseScheduled(ctx context.Context) (int, error) {
	rules, err := s.rules(ctx)
	if err != nil {
		return 0, err
	}
	if !rules.AutoClose || rules.AutoCloseHours <= 0 {
		return 0, nil
	}

	if !CanTransition(StatusPendingUser, StatusClosed) {
		return 0, errs.Newf(errs.Internal, "工单状态机不允许 pending_user -> closed")
	}

	now := s.now()
	before := now.Add(-time.Duration(rules.AutoCloseHours) * time.Hour)

	var closedCount int
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		candidates, err := s.repo.LockAutoCloseCandidates(ctx, before, AutoCloseBatch)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}

		type closedItem struct {
			candidateIndex int
			msg            *model.Message
		}
		var closedItems []closedItem
		var msgs []*model.Message

		closeCols := map[string]any{
			"status":        StatusClosed,
			"closed_at":     now,
			"closed_reason": ClosedAuto,
			"closed_note":   nil,
		}

		body := fmt.Sprintf("客服回复后 %d 小时内没有新的回复，工单已自动关闭；如还有问题请新建工单", rules.AutoCloseHours)

		for i, c := range candidates {
			ok, err := s.repo.UpdateTicket(ctx, c.ID, StatusPendingUser, closeCols)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			m := &model.Message{
				TicketID:   c.ID,
				AuthorKind: AuthorSystem,
				Body:       body,
			}
			msgs = append(msgs, m)
			closedItems = append(closedItems, closedItem{
				candidateIndex: i,
				msg:            m,
			})
		}

		if len(msgs) == 0 {
			return nil
		}

		if err := s.repo.InsertMessages(ctx, msgs); err != nil {
			return err
		}

		for _, item := range closedItems {
			c := candidates[item.candidateIndex]
			s.ticketChanged(ctx, realtime.OpUpdate, c.ID, c.UserID)
			s.messagesAdded(ctx, c.UserID, []*model.Message{item.msg})
			if err := s.writeAudit(ctx, ActionAutoClose, ResourceTicket, c.ID, map[string]any{
				"from":          StatusPendingUser,
				"to":            StatusClosed,
				"closed_reason": ClosedAuto,
				"hours":         rules.AutoCloseHours,
			}); err != nil {
				return err
			}
		}

		closedCount = len(closedItems)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return closedCount, nil
}
