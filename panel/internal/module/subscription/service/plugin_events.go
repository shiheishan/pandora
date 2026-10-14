package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
)

// UsePluginEvents 注入插件事件口（plugin 模块实现，见 internal/pkg/pluginevents）。
func (s *Service) UsePluginEvents(p pluginevents.Emitter) { s.events = p }

// emitExpired 对一批刚改成 expired 的订阅发 subscription.expired 插件事件。
func (s *Service) emitExpired(ctx context.Context, subs []model.Subscription, changed map[uuid.UUID]struct{}) error {
	if s.events == nil || len(changed) == 0 {
		return nil
	}
	planIDs := make([]uuid.UUID, 0)
	seen := make(map[uuid.UUID]struct{})
	for _, m := range subs {
		if _, ok := changed[m.ID]; !ok {
			continue
		}
		if _, ok := seen[m.PlanID]; ok {
			continue
		}
		seen[m.PlanID] = struct{}{}
		planIDs = append(planIDs, m.PlanID)
	}
	plans, err := s.catalog.PlansByIDs(ctx, planIDs)
	if err != nil {
		return err
	}
	for _, m := range subs {
		if _, ok := changed[m.ID]; !ok {
			continue
		}
		planName := ""
		if p, ok := plans[m.PlanID]; ok {
			planName = p.Name
		}
		key := fmt.Sprintf("sub.expired:%s:%d", m.ID, m.CurrentPeriodEnd.Unix())
		if err := pluginevents.Emit(ctx, s.events, "subscription.expired", key, map[string]any{
			"event":           "subscription.expired",
			"subscription_id": m.ID.String(),
			"user_id":         m.UserID.String(),
			"plan":            planName,
			"expired_at":      m.CurrentPeriodEnd.UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	return nil
}
