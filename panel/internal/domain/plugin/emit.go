package plugin

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// 各业务用的便利函数。
//
// 都做成自由函数而不是 Service 的方法：调用点在 billing、identity、support
// 这些包里，它们只需要往队列里插一行，不需要持有一个能发 HTTP 的服务实例。
// 少一个依赖，也就少一条 billing 能意外发起网络请求的路径。
//
// 每个函数都吃调用方的 tx —— 事件必须和业务变更同生共死。订单回滚了却已经
// 通知了插件，插件那边就会有一笔面板里不存在的订单，而这种不一致没有任何
// 自动机制能修。
//
// 所有函数都返回 error 并且应当被检查：Emit 只是一条 INSERT，它失败通常
// 意味着连接或事务本身出了问题，那时候业务也不该假装成功。

// EmitOrderCreated 订单创建。
func EmitOrderCreated(ctx context.Context, tx pgx.Tx, tenantID, orderID, orderNo,
	userID, currency string, total int64) error {
	return Emit(ctx, tx, tenantID, "order.created", "order.created:"+orderID,
		map[string]any{
			"event":    "order.created",
			"order_id": orderID, "order_no": orderNo,
			"user_id": userID, "currency": currency, "total": total,
		})
}

// EmitOrderPaid 订单支付完成。
//
// dedupe 用订单号而不是加时间：支付回调会重放，同一笔订单只该通知一次。
func EmitOrderPaid(ctx context.Context, tx pgx.Tx, tenantID, orderID, userID,
	kind, currency string, amount int64, subscriptionID string) error {
	p := map[string]any{
		"event":    "order.paid",
		"order_id": orderID, "user_id": userID, "kind": kind,
		"currency": currency, "amount": amount,
	}
	if subscriptionID != "" {
		p["subscription_id"] = subscriptionID
	}
	return Emit(ctx, tx, tenantID, "order.paid", "order.paid:"+orderID, p)
}

// EmitSubscriptionProvisioned 订阅开通或续期。
//
// dedupe 带上订单号：同一条订阅会被续费很多次，只按订阅 ID 去重的话
// 第二次续费就发不出去了。
func EmitSubscriptionProvisioned(ctx context.Context, tx pgx.Tx, tenantID,
	subscriptionID, userID, orderID, reason string) error {
	return Emit(ctx, tx, tenantID, "subscription.provisioned",
		"sub.prov:"+subscriptionID+":"+orderID,
		map[string]any{
			"event":           "subscription.provisioned",
			"subscription_id": subscriptionID, "user_id": userID,
			"order_id": orderID, "reason": reason,
		})
}

// EmitOrderCancelled 订单被取消或作废。
func EmitOrderCancelled(ctx context.Context, tx pgx.Tx, tenantID, orderID,
	target, reason string) error {
	return Emit(ctx, tx, tenantID, "order.cancelled", "order.cancelled:"+orderID,
		map[string]any{
			"event": "order.cancelled", "order_id": orderID,
			"target": target, "reason": reason,
		})
}

// EmitUserRegistered 用户完成注册。
func EmitUserRegistered(ctx context.Context, tx pgx.Tx, tenantID, userID, email string) error {
	return Emit(ctx, tx, tenantID, "user.registered", "user.registered:"+userID,
		map[string]any{
			"event": "user.registered", "user_id": userID, "email": email,
		})
}

// EmitTicketCreated 用户提交工单。
func EmitTicketCreated(ctx context.Context, tx pgx.Tx, tenantID, ticketID, ticketNo,
	userID, category, priority, subject string) error {
	return Emit(ctx, tx, tenantID, "ticket.created", "ticket.created:"+ticketID,
		map[string]any{
			"event": "ticket.created", "ticket_id": ticketID, "ticket_no": ticketNo,
			"user_id": userID, "category": category, "priority": priority,
			"subject": subject,
		})
}

// EmitGiftCardRedeemed 礼品卡兑换。
func EmitGiftCardRedeemed(ctx context.Context, tx pgx.Tx, tenantID, codeID, userID,
	template, cardType string, granted any) error {
	return Emit(ctx, tx, tenantID, "giftcard.redeemed", "giftcard.redeemed:"+codeID,
		map[string]any{
			"event": "giftcard.redeemed", "code_id": codeID, "user_id": userID,
			"template": template, "type": cardType, "granted": granted,
		})
}

// EmitSubscriptionExpiring 订阅即将到期。
//
// dedupeKey 由调用方给：到期提醒按「还剩几天」分档，每档只发一次，
// 这个分档规则在通知那边，不该在这里再写一遍。
func EmitSubscriptionExpiring(ctx context.Context, tx pgx.Tx, tenantID, dedupeKey,
	subscriptionID, userID, plan, expiresAt string, daysLeft int) error {
	return Emit(ctx, tx, tenantID, "subscription.expiring", dedupeKey,
		map[string]any{
			"event": "subscription.expiring", "subscription_id": subscriptionID,
			"user_id": userID, "plan": plan, "expires_at": expiresAt,
			"days_left": daysLeft,
		})
}

// EmitTrafficExhausted 流量接近或已经用尽。
func EmitTrafficExhausted(ctx context.Context, tx pgx.Tx, tenantID, dedupeKey,
	subscriptionID, userID, plan string, consumed, total int64, percent int) error {
	return Emit(ctx, tx, tenantID, "traffic.exhausted", dedupeKey,
		map[string]any{
			"event": "traffic.exhausted", "subscription_id": subscriptionID,
			"user_id": userID, "plan": plan,
			"consumed_bytes": consumed, "total_bytes": total, "percent": percent,
		})
}
