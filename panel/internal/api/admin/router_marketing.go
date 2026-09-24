// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerGiftCardRoutes、registerCouponRoutes、registerCommissionRoutes
// [POS]: api/admin 路由表的「礼品卡与批次导出、优惠券、分销与提现」段，由 NewRouter 按原注册顺序调用；处理器在 giftcard.go / coupon.go / coupon_batch.go / commission.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

func registerGiftCardRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 礼品卡 / 卡密 ---
	// 生码是高风险操作：一次能造出几千张能换真钱的凭证，
	// 所以要写权限 + 近期重认证。停用单个码不需要重认证 ——
	// 那是发现异常时的止血动作，越快越好。
	r.With(middleware.RequirePermission("marketing.giftcard.read", d.Log)).
		Get("/gift-cards", h.listGiftTemplates)
	r.With(middleware.RequirePermission("marketing.giftcard.read", d.Log)).
		Get("/gift-cards/stats", h.giftCardStats)
	r.With(middleware.RequirePermission("marketing.giftcard.read", d.Log)).
		Get("/gift-cards/codes", h.listGiftCodes)
	r.With(middleware.RequirePermission("marketing.giftcard.read", d.Log)).
		Get("/gift-cards/batches", h.listGiftBatches)
	// 明文卡码的唯一出口：每批只能导出一次，写权限 + 近期重认证 + 幂等键
	// （同一个键的重试原样拿回同一份 CSV）。旧的 GET codes/export 可以
	// 被只读权限无限次导出，已下线。
	r.With(
		middleware.RequirePermission("marketing.giftcard.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "giftcard_batch_export", d.Log),
	).Post("/gift-cards/batches/{id}/export", h.exportGiftBatch)
	r.With(middleware.RequirePermission("marketing.giftcard.read", d.Log)).
		Get("/gift-cards/usages", h.listGiftUsages)
	// 改模板奖励会同时改变全部未兑换码的价值，和生码同级要求重认证。
	r.With(
		middleware.RequirePermission("marketing.giftcard.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/gift-cards", h.saveGiftTemplate)
	r.With(
		middleware.RequirePermission("marketing.giftcard.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "giftcard_codes_generate", d.Log),
	).Post("/gift-cards/{id}/codes", h.generateGiftCodes)
	r.With(middleware.RequirePermission("marketing.giftcard.write", d.Log)).
		Post("/gift-cards/codes/{id}/toggle", h.toggleGiftCode)
}

func registerCouponRoutes(r chi.Router, d Deps, h *handlers) {
	// 优惠券
	r.With(middleware.RequirePermission("marketing.coupon.read", d.Log)).
		Get("/coupons", h.listCoupons)
	r.With(
		middleware.RequirePermission("marketing.coupon.read", d.Log),
		middleware.RequirePermission("billing.order.read", d.Log),
	).
		Get("/coupons/{id}/redemptions", h.couponRedemptions)
	r.With(
		middleware.RequirePermission("marketing.coupon.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).
		Post("/coupons", h.createCoupon)
	// 一次最多上千张：网络重试不能变成两批券。
	r.With(
		middleware.RequirePermission("marketing.coupon.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "coupon_batch_generate", d.Log),
	).
		Post("/coupons/batch", h.generateCoupons)
	r.With(
		middleware.RequirePermission("marketing.coupon.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).
		Post("/coupons/{id}/status", h.setCouponStatus)
}

func registerCommissionRoutes(r chi.Router, d Deps, h *handlers) {
	// 分销佣金：权限码用营销域自己的 marketing.commission.* /
	// marketing.withdrawal.approve，不再借用订单与支付渠道的权限。
	r.With(middleware.RequirePermission("marketing.commission.read", d.Log)).
		Get("/commission/overview", h.commissionOverview)
	r.With(middleware.RequirePermission("marketing.commission.read", d.Log)).
		Get("/withdrawals", h.listWithdrawals)
	r.With(
		middleware.RequirePermission("marketing.withdrawal.approve", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/withdrawals/{id}/review", h.reviewWithdrawal)
	r.With(
		middleware.RequirePermission("marketing.withdrawal.approve", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "commission_withdrawal_mark_paid", d.Log),
	).Post("/withdrawals/{id}/paid", h.markWithdrawalPaid)
	// 改返佣比例直接改变之后每一笔订单的支出，要重认证。
	r.With(
		middleware.RequirePermission("marketing.commission.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/commission/config", h.setCommissionConfig)
}
