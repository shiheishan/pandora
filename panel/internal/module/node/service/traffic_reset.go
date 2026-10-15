package service

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// ResetNodeTraffic 后台手动重置节点本周期流量：清零用量与超限标记，审计 node.traffic_reset（原因、清零前用量、是否超限）；
// 原来超限的恢复下发（推配置与名单纪元）。节点不存在回 404。
func (s *Service) ResetNodeTraffic(ctx context.Context, id uuid.UUID, reason string) error {
	validatedReason, err := nodeValidateReason(reason)
	if err != nil {
		return err
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		before, found, err := s.repo.ResetNodeUsage(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}

		auditData := map[string]any{
			"reason":            validatedReason,
			"used_bytes_before": before.UsedBytes,
			"exceeded_before":   before.Exceeded,
		}
		if err := s.nodeAudit(ctx, "node.traffic_reset", id, auditData); err != nil {
			return err
		}

		s.nodeChanged(ctx, realtime.OpUpdate, id)

		if before.Exceeded {
			s.MarkConfigChanged(ctx)
			s.MarkUsersChanged(ctx)
		}
		return nil
	})
}

// ResetDueNodeTraffic 由定时任务调用：设了重置日的节点，本周期起点（站点时区）晚于 traffic_reset_at 的清零；返回清零个数。
// 有原来超限的推配置与名单纪元。读站点时区失败返回错误（任务下次再跑）。
func (s *Service) ResetDueNodeTraffic(ctx context.Context) (int, error) {
	loc, err := s.siteLoc(ctx)
	if err != nil {
		return 0, err
	}

	var resetCount int
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		candidates, err := s.repo.NodeResetCandidates(ctx)
		if err != nil {
			return err
		}

		now := s.now()
		var due []repo.NodeResetDue
		for _, c := range candidates {
			b := trafficPeriodStart(c.ResetDay, now, loc)
			if c.ResetAt.Before(b) {
				due = append(due, repo.NodeResetDue{
					ID:     c.ID,
					Before: b,
				})
			}
		}

		if len(due) == 0 {
			resetCount = 0
			return nil
		}

		reset, unblocked, err := s.repo.ResetNodesUsageIfBefore(ctx, due)
		if err != nil {
			return err
		}

		if len(reset) > 0 {
			s.nodesChanged(ctx, reset)
			plog.From(ctx).Info("节点流量按重置日清零", zap.Int("count", len(reset)))
		}

		if len(unblocked) > 0 {
			s.MarkConfigChanged(ctx)
			s.MarkUsersChanged(ctx)
		}

		resetCount = len(reset)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return resetCount, nil
}
