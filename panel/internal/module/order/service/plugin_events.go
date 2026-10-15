package service

import (
	"context"
	"fmt"

	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
)

// UsePluginEvents 注入插件事件口（plugin 模块实现，见 internal/pkg/pluginevents）。
func (s *Service) UsePluginEvents(p pluginevents.Emitter) { s.events = p }

// emitOrderCreated 订单创建（新购、换套餐、流量包、流量重置；续费与充值单不发）。
func (s *Service) emitOrderCreated(ctx context.Context, o model.Order) error {
	switch o.Kind {
	case KindNew, KindUpgrade, KindAddon, KindTrafficReset:
	default:
		return nil
	}
	total, err := money.ToCents(o.TotalAmount)
	if err != nil {
		return fmt.Errorf("order.created 金额: %w", err)
	}
	return pluginevents.Emit(ctx, s.events, "order.created", "order.created:"+o.ID.String(), map[string]any{
		"event":    "order.created",
		"order_id": o.ID.String(),
		"order_no": o.OrderNo,
		"user_id":  o.UserID.String(),
		"currency": o.Currency,
		"total":    total,
	})
}

// emitOrderPaid 订单支付完成（外部支付结算与 0 元单都发）。dedupe 用订单 id：支付回调会重放，同一笔只通知一次。
// zeroPay 为 true 时（插件事件口径，产品决定）：只有新购、流量包与流量重置的 0 元单发（0 元续费、0 元换套餐不发），
// currency 发 ""、amount 发 0。金额换算失败时返回错误，让业务事务失败。
func (s *Service) emitOrderPaid(ctx context.Context, o model.Order, zeroPay bool) error {
	var currency string
	var amount int64
	if zeroPay {
		if o.Kind != KindNew && o.Kind != KindAddon && o.Kind != KindTrafficReset {
			return nil
		}
	} else {
		cents, err := money.ToCents(o.TotalAmount)
		if err != nil {
			return fmt.Errorf("order.paid 金额: %w", err)
		}
		currency, amount = o.Currency, cents
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
	return pluginevents.Emit(ctx, s.events, "order.paid", "order.paid:"+o.ID.String(), p)
}

// emitSubscriptionProvisioned 外部支付结算开通或续期了订阅（产品决定：只在这条路径发，
// 0 元单、后台开单、礼品卡开通都不发）。dedupe 带订单 id：同一订阅会续费很多次。reason 是订单类型。
func (s *Service) emitSubscriptionProvisioned(ctx context.Context, o model.Order) error {
	sub, ok := provisionedSub(o)
	if !ok {
		return nil
	}
	return pluginevents.Emit(ctx, s.events, "subscription.provisioned", "sub.prov:"+sub+":"+o.ID.String(), map[string]any{
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
// 超时作废在事件里的原因固定是 reservation_expired（插件事件口径，产品决定）；release_reason 与审计不受影响。
func (s *Service) emitOrderCancelled(ctx context.Context, o model.Order, target, reason string) error {
	if reason == ReasonExpired {
		reason = "reservation_expired"
	}
	return pluginevents.Emit(ctx, s.events, "order.cancelled", "order.cancelled:"+o.ID.String(), map[string]any{
		"event":    "order.cancelled",
		"order_id": o.ID.String(),
		"target":   target,
		"reason":   reason,
	})
}
