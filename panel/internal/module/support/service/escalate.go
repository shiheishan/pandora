package service

import (
	"context"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/support/model"
)

// 编译期断言：工单号供审计资源展示。
func _() {
	var s *Service
	var _ auditsvc.ResourceLabeler = s.TicketLabels
}

// EscalateOverdue 扫描首次响应超时工单并自动升级（后台手动与定时任务共用）。
func (s *Service) EscalateOverdue(ctx context.Context, reply func(int) (idemsvc.Response, error)) (int, error) {
	now := s.now()
	var n int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.EscalateOverdue(ctx, now, EscalateBatch)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			n = 0
			if reply != nil {
				return s.completeIdem(ctx, func() (idemsvc.Response, error) { return reply(0) })
			}
			return nil
		}
		msgs := make([]*model.Message, 0, len(rows))
		for _, r := range rows {
			msgs = append(msgs, &model.Message{
				TicketID:   r.ID,
				AuthorKind: AuthorSystem,
				Body:       "首次响应已超过 SLA，工单自动升级",
			})
		}
		if err := s.repo.InsertMessages(ctx, msgs); err != nil {
			return err
		}
		for _, r := range rows {
			if err := s.writeAudit(ctx, ActionSLAEscalate, ResourceTicket, r.ID, map[string]any{
				"from":          r.BeforeStatus,
				"from_priority": r.BeforePriority,
				"priority":      r.Priority,
			}); err != nil {
				return err
			}
		}
		n = len(rows)
		if reply != nil {
			return s.completeIdem(ctx, func() (idemsvc.Response, error) { return reply(n) })
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// TicketLabels 批量取工单号（审计 resource_type=ticket 的可读名）。
func (s *Service) TicketLabels(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return s.repo.TicketNos(ctx, ids)
}
