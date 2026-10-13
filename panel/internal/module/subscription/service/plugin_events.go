package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// PluginEvents 是插件钩子的出站事件口，由 plugin 模块实现、装配时注入。
// Emit 在调用方的事务里（ctx 带事务时）写一条待投递记录，事件与业务变更同生共死；
// payload 是插件收到的 JSON 正文。dedupeKey 相同的事件只投递一次。
type PluginEvents interface {
	Emit(ctx context.Context, event, dedupeKey string, payload any) error
}

// UsePluginEvents 注入插件事件口。
func (s *Service) UsePluginEvents(p PluginEvents) { s.events = p }

// PluginEventsWired 报告事件口是否已注入（装配完成后检查）。
func (s *Service) PluginEventsWired() bool { return s.events != nil }

// emitPlugin 发一个插件事件；没注入事件口时什么都不做。
func (s *Service) emitPlugin(ctx context.Context, event, dedupeKey string, payload map[string]any) error {
	if s.events == nil {
		return nil
	}
	return s.events.Emit(ctx, event, dedupeKey, payload)
}

// emitProvisioned 订阅开通或续期（只在由订单驱动时发，与旧代码一致：卡密兑换不发）。
// dedupe 带订单 id：同一份订阅会被续很多次，只按订阅去重的话第二次续费就发不出去了。
// reason 是订单种类：new（开通）、renewal（续期）、upgrade（换套餐）。
func (s *Service) emitProvisioned(ctx context.Context, subID, userID uuid.UUID, orderID *uuid.UUID, reason string) error {
	if orderID == nil {
		return nil
	}
	return s.emitPlugin(ctx, "subscription.provisioned",
		"sub.prov:"+subID.String()+":"+orderID.String(),
		map[string]any{
			"event":           "subscription.provisioned",
			"subscription_id": subID.String(),
			"user_id":         userID.String(),
			"order_id":        orderID.String(),
			"reason":          reason,
		})
}

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
		if err := s.emitPlugin(ctx, "subscription.expired", key, map[string]any{
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
