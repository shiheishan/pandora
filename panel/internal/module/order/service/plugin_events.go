package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
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

// emitOrderCreated 订单创建（新购、换套餐、流量包；续费与充值单不发，与旧代码一致）。
func (s *Service) emitOrderCreated(ctx context.Context, o model.Order) error {
	switch o.Kind {
	case KindNew, KindUpgrade, KindAddon:
	default:
		return nil
	}
	return s.emitPlugin(ctx, "order.created", "order.created:"+o.ID.String(), map[string]any{
		"event":    "order.created",
		"order_id": o.ID.String(),
		"order_no": o.OrderNo,
		"user_id":  o.UserID.String(),
		"currency": o.Currency,
		"total":    money.MustCents(o.TotalAmount),
	})
}

// emitOrderPaid 订单支付完成（外部支付结算与 0 元单都发）。dedupe 用订单 id：支付回调会重放，同一笔只通知一次。
// zeroPay 为 true 时 currency 发 ""、amount 发 0（与旧代码 0 元单口径一致）。
func (s *Service) emitOrderPaid(ctx context.Context, o model.Order, zeroPay bool) error {
	currency, amount := o.Currency, money.MustCents(o.TotalAmount)
	if zeroPay {
		currency, amount = "", 0
	}
	p := map[string]any{
		"event":    "order.paid",
		"order_id": o.ID.String(),
		"user_id":  o.UserID.String(),
		"kind":     o.Kind,
		"currency": currency,
		"amount":   amount,
	}
	if sub, ok := provisionedSub(o); ok {
		p["subscription_id"] = sub
	}
	return s.emitPlugin(ctx, "order.paid", "order.paid:"+o.ID.String(), p)
}

// emitSubscriptionProvisioned 外部支付结算开通或续期了订阅（与旧代码一致：只在这条路径发，
// 0 元单、后台开单、礼品卡开通都不发）。dedupe 带订单 id：同一订阅会续费很多次。reason 是订单类型。
func (s *Service) emitSubscriptionProvisioned(ctx context.Context, o model.Order) error {
	sub, ok := provisionedSub(o)
	if !ok {
		return nil
	}
	return s.emitPlugin(ctx, "subscription.provisioned", "sub.prov:"+sub+":"+o.ID.String(), map[string]any{
		"event":           "subscription.provisioned",
		"subscription_id": sub,
		"user_id":         o.UserID.String(),
		"order_id":        o.ID.String(),
		"reason":          o.Kind,
	})
}

// provisionedSub 返回履约后订单落到的订阅 id；只有新购、续费、换套餐单有。
func provisionedSub(o model.Order) (string, bool) {
	if (o.Kind == KindNew || o.Kind == KindRenewal || o.Kind == KindUpgrade) && o.SubscriptionID != nil {
		return o.SubscriptionID.String(), true
	}
	return "", false
}

// emitOrderCancelled 订单取消或超时作废。target 是目标状态（cancelled / expired）。
func (s *Service) emitOrderCancelled(ctx context.Context, o model.Order, target, reason string) error {
	return s.emitPlugin(ctx, "order.cancelled", "order.cancelled:"+o.ID.String(), map[string]any{
		"event":    "order.cancelled",
		"order_id": o.ID.String(),
		"target":   target,
		"reason":   reason,
	})
}
