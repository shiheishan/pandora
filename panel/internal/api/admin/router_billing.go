// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerLatePaymentRoutes、registerOrderRoutes、registerPaymentProviderRoutes、registerBalanceAdjustRoutes
// [POS]: api/admin 路由表的「挂账转余额、订单与人工单、支付渠道、余额人工调账」段，由 NewRouter 按原注册顺序调用；人工单的幂等 scope 取 billing.CheckoutIdempotencyScope
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/middleware"
)

func registerLatePaymentRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 挂账（用户取消订单之后才到账的钱）---
	// 写入路径一直都在，但此前没有任何读取出口，钱进了 suspense 科目
	// 就没人看得见了。转入余额动的是真钱，所以和调账同级：
	// 要写权限、要近期重认证、要幂等键。
	r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
		Get("/late-payments", h.listLatePayments)
	r.With(
		middleware.RequirePermission("billing.adjustment.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "late_payment_apply", d.Log),
	).Post("/late-payments/{id}/apply-to-balance", h.applyLatePayment)
}

func registerOrderRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 订单 ---
	r.With(middleware.RequirePermission("billing.order.read", d.Log)).
		Get("/orders", h.listOrders)
	r.With(middleware.RequirePermission("billing.order.read", d.Log)).
		Get("/orders/{id}", h.getOrder)
	r.With(middleware.RequirePermission("billing.payment.read", d.Log)).
		Get("/orders/{id}/payments", h.getOrderPayments)
	r.With(
		middleware.RequirePermission("billing.order.write", d.Log),
		middleware.Idempotency(d.Pool, "admin_order_cancel", d.Log),
	).Post("/orders/{id}/cancel", h.cancelOrder)

	// 人工单与线下收款（XBD-015）。两者都动真金白银或真权益，
	// 所以和取消订单同级：写权限 + 近期重认证 + 幂等键。
	r.With(
		middleware.RequirePermission("billing.order.write", d.Log),
		// 作用域必须与 CreateOrder 校验时用的一致：人工单本来就是一次建单，
		// 换个名字只会让声明校验过不去。
		middleware.Idempotency(d.Pool, billing.CheckoutIdempotencyScope, d.Log),
	).Post("/orders/manual", h.createManualOrder)
	r.With(
		middleware.RequirePermission("billing.order.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "admin_order_mark_paid", d.Log),
	).Post("/orders/{id}/mark-paid", h.markOrderPaid)
}

func registerPaymentProviderRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 支付渠道 ---
	r.With(middleware.RequirePermission("billing.payment.read", d.Log)).
		Get("/payment-providers", h.listProviders)
	r.With(
		middleware.RequirePermission("billing.provider.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/payment-providers/{code}/toggle", h.toggleProvider)
}

func registerBalanceAdjustRoutes(r chi.Router, d Deps, h *handlers) {
	// 余额人工调账。挂在 billing.provider.write 下 ——
	// 能凭空加钱的权限不该跟「看看用户资料」是同一级
	r.With(
		middleware.RequirePermission("billing.provider.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "admin_user_balance_adjust", d.Log),
	).Post("/users/{id}/balance", h.adjustBalance)
}
