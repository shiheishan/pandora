// [INPUT]: 依赖 domain/* 各服务（经 Deps 注入）、middleware 的鉴权/限流/幂等/权限链、platform/webapp 的 Mount 与 web.AdminApp
// [OUTPUT]: 对外提供 Deps、NewRouter：admin 网关的完整 chi 路由表
// [POS]: api/admin 的装配点：根 / 与 /assets/* 经 webapp 下发后台前端、/v1 业务路由与逐路由权限声明都在这里；处理器分散在同包各文件
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package admin 实现管理控制台 API（Admin 域）。
//
// 与 Public 域的关键差异（ARC-002「管理面隔离」）：
//
//	· 独立的令牌签名密钥，Public 域的令牌在这里验签必然失败（EXT-001）；
//	· 每个业务路由都必须声明权限，未声明即默认拒绝（IAM-009）；
//	· 高风险动作额外要求近期重认证（SEC-009）；
//	· 全部写操作留审计（SEC-012），由 adminops 服务层保证。
//
// 生产部署时本网关不应有普通公网入口，必须置于零信任网关之后。
package admin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/token"
	"github.com/aegispanel/aegis/internal/platform/webapp"
	"github.com/aegispanel/aegis/web"
)

type Deps struct {
	Cfg      *config.Config
	Pool     *db.Pool
	Redis    *redis.Client
	Log      *slog.Logger
	Issuer   *token.Issuer
	Identity *identity.Service
	Ops      *adminops.Service
	Support  *support.Service
	Node     *nodefabric.Service
	// Subscription 只用到管理员代用户换订阅链接那一条。
	// 用户自己那条走 public 域，带所有权校验；管理员这条替客服兜底。
	Subscription *subscription.Service
	// Realtime 只用来发布：admin 自己不建 SSE 连接，
	// 但它改的数据要能立刻推到用户那一侧
	Realtime *realtime.Hub
	// Envelope 用于解密审计里的来源 IP（风控画像要看明文）
	Envelope *crypto.Envelope
	// GeoIP 把解密出来的 IP 翻成归属地。可以为 nil：数据文件缺失时
	// 归属地列留空，明细本身照常展示。
	GeoIP *geoip.Resolver
	// Billing 只用到后台作业与提现打款的记账部分
	Billing *billing.Service
	Content *content.Service
	// SMTPProvider 用来在保存邮件设置后让配置缓存立刻失效
	SMTPProvider *notify.DBSMTPProvider
	// Notify 用于通知模板的读写。模板表与渲染本来就在跑，
	// 只是一直没有让人改内容的入口
	Notify *notify.Service
	// GiftCard 礼品卡 / 卡密
	GiftCard *giftcard.Service
	// TelegramSender 用于保存配置后立刻失效缓存
	TelegramSender *notify.DynamicTelegramSender
	// Appearance 主题与前端插槽
	Appearance *appearance.Service
	// Plugin 插件钩子（出站 webhook）
	Plugin *plugin.Service
}

type catalogIdempotencyFactory func(*db.Pool, string, *slog.Logger) func(http.Handler) http.Handler

func registerCatalogPlanUpdate(r chi.Router, d Deps, handler http.HandlerFunc, idempotency catalogIdempotencyFactory) {
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		idempotency(d.Pool, "catalog_plan_update", d.Log),
	).Put("/plans/{id}", handler)
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.ClientInfo)
	r.Use(middleware.Recovery(d.Log))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.Timeout(25 * time.Second))
	r.Use(middleware.Tenant)
	r.Use(middleware.Authenticate(d.Pool, d.Issuer, d.Log))
	// 只接受 admin 域的令牌。拿 Public 域令牌来敲这里，
	// 即使签名侥幸通过也会在这一层被挡下（EXT-001 验收）。
	r.Use(middleware.DomainGuard(string(config.DomainAdmin), d.Log))

	// 管理面的限流比用户面更严：正常管理操作频率远低于用户侧，
	// 高频访问本身就是异常信号。
	r.Use(middleware.RateLimit(d.Redis, d.Log,
		middleware.ByIP("adm_ip", time.Minute, 240),
	))

	h := &handlers{d: d}

	// 管理控制台前端：入口 / 与 /assets/*，只接 GET/HEAD
	webapp.Mount(r, web.AdminApp)
	r.Get("/healthz", h.health)
	r.Get("/readyz", h.ready)

	r.Route("/v1", func(r chi.Router) {
		// 登录入口：不需要已登录，但限流最严（管理员口令是权限的根）
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(d.Redis, d.Log,
				middleware.ByRoute("adm_auth", time.Minute, d.Cfg.RateLimitAuthPerMinute),
			))
			r.Post("/auth/login", h.login)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAuth(d.Log))

			// 实时事件流会暴露整个租户的运营变更，必须先登录并具备通知读取权限。
			r.With(middleware.RequirePermission("ops.notification.read", d.Log)).
				Get("/events", h.events)

			r.Post("/auth/logout", h.logout)
			r.Get("/me", h.me)
			r.Post("/me/password", h.changePassword)

			// 重认证。必须留在「已登录但不要求近期重认证」这一层 ——
			// 放进 RequireRecentReauth 后面就成了死锁：想重认证得先有
			// 近期重认证。限流按认证入口的标准来，它同样是在验口令。
			r.With(middleware.RateLimit(d.Redis, d.Log,
				middleware.ByAccount("adm_reauth", time.Minute, d.Cfg.RateLimitAuthPerMinute),
				middleware.ByIP("adm_reauth_ip", time.Minute, d.Cfg.RateLimitAuthPerMinute),
			)).Post("/auth/reauth", h.reauth)

			// --- 仪表盘 ---
			r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
				Get("/overview", h.overview)
			r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
				Get("/revenue/timeseries", h.revenueTimeseries)
			r.With(middleware.RequirePermission("billing.ledger.read", d.Log)).
				Get("/revenue/adjustments", h.revenueAdjustments)
			r.With(
				middleware.RequirePermission("metering.read", d.Log),
				middleware.RequirePermission("node.read", d.Log),
			).Get("/dashboard/traffic/nodes", h.dashboardNodeTraffic)
			r.With(
				middleware.RequirePermission("metering.read", d.Log),
				middleware.RequirePermission("iam.user.read", d.Log),
			).Get("/dashboard/traffic/users", h.dashboardUserTraffic)
			r.With(middleware.RequirePermission("ops.notification.read", d.Log)).
				Get("/dashboard/backlog/notifications", h.dashboardNotificationBacklog)
			r.With(
				middleware.RequirePermission("billing.adjustment.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "revenue_adjustment_create", d.Log),
			).Post("/revenue/adjustments", h.createRevenueAdjustment)
			r.With(
				middleware.RequirePermission("billing.adjustment.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "revenue_adjustment_reverse", d.Log),
			).Post("/revenue/adjustments/{id}/reverse", h.reverseRevenueAdjustment)

			// --- 外观：主题与插槽 ---
			// 自定义 CSS 和插槽 HTML 会渲染进用户端，写权限按高危对待。
			r.With(middleware.RequirePermission("platform.appearance.read", d.Log)).
				Get("/themes", h.listThemes)
			r.With(
				middleware.RequirePermission("platform.appearance.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "appearance_theme_save", d.Log),
			).Post("/themes", h.saveTheme)
			r.With(
				middleware.RequirePermission("platform.appearance.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Post("/themes/{code}/activate", h.activateTheme)
			r.With(
				middleware.RequirePermission("platform.appearance.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Delete("/themes/{code}", h.deleteTheme)
			r.With(middleware.RequirePermission("platform.appearance.read", d.Log)).
				Get("/slots", h.listSlots)
			r.With(
				middleware.RequirePermission("platform.appearance.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "appearance_slot_save", d.Log),
			).Post("/slots/{key}", h.saveSlot)

			// --- 插件钩子 ---
			// 钩子地址决定面板会去连什么，改它等同于改一条出站规则，
			// 所以写操作要重认证。
			r.With(middleware.RequirePermission("platform.plugin.read", d.Log)).
				Get("/plugin-hooks", h.listHooks)
			r.With(
				middleware.RequirePermission("platform.plugin.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "plugin_hook_save", d.Log),
			).Post("/plugin-hooks", h.saveHook)
			r.With(
				middleware.RequirePermission("platform.plugin.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Delete("/plugin-hooks/{code}", h.deleteHook)
			r.With(middleware.RequirePermission("platform.plugin.read", d.Log)).
				Get("/plugin-hooks/{code}/deliveries", h.hookDeliveries)
			r.With(
				middleware.RequirePermission("platform.plugin.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Post("/plugin-hooks/{code}/test", h.testHook)

			// --- Telegram ---
			// Bot Token 走信封加密，读接口只回「配没配」不回明文。
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/settings/telegram", h.getTelegramSettings)
			r.With(
				middleware.RequirePermission("platform.settings.write", d.Log),
			).Post("/settings/telegram", h.setTelegramSettings)
			r.With(middleware.RequirePermission("billing.provider.write", d.Log)).
				Post("/settings/telegram/test", h.testTelegram)

			// --- 用户批量运营 ---
			// 预览只要读权限：它是「发之前先看清影响面」，鼓励多用。
			// 导出会带走一份用户名单，生成账号会造出能登录的凭证，
			// 群发的信收不回来 —— 这三个都要写权限 + 近期重认证。
			r.With(middleware.RequirePermission("iam.user.read", d.Log)).
				Post("/users/bulk/preview", h.previewBulkUsers)
			r.With(
				middleware.RequirePermission("iam.user.read", d.Log),
			).Get("/users/bulk/export", h.exportUsers)
			r.With(
				middleware.RequirePermission("iam.user.write", d.Log),
				middleware.Idempotency(d.Pool, "user_bulk_generate", d.Log),
			).Post("/users/bulk/generate", h.generateUsers)
			r.With(
				middleware.RequirePermission("ops.notification.write", d.Log),
				middleware.Idempotency(d.Pool, "user_bulk_mail", d.Log),
			).Post("/users/bulk/mail", h.sendBulkMail)

			// --- 流量重置（XBD 的 traffic-reset）---
			// 手动重置直接改变用户可用额度，所以要写权限 + 近期重认证；
			// 查日志只要读权限 —— 客服排查「我流量怎么变了」时用得上。
			r.With(middleware.RequirePermission("metering.reset.read", d.Log)).
				Get("/traffic-resets", h.listTrafficResets)
			r.With(middleware.RequirePermission("metering.reset.read", d.Log)).
				Get("/traffic-resets/stats", h.trafficResetStats)
			r.With(middleware.RequirePermission("metering.reset.read", d.Log)).
				Get("/users/{id}/traffic-resets", h.userTrafficResetHistory)
			r.With(
				middleware.RequirePermission("metering.reset.write", d.Log),
				middleware.Idempotency(d.Pool, "traffic_manual_reset", d.Log),
			).Post("/users/{id}/traffic-reset", h.manualResetTraffic)

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
				Get("/gift-cards/codes/export", h.exportGiftCodes)
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

			// --- 用户 ---
			r.With(middleware.RequirePermission("iam.user.read", d.Log)).
				Get("/users", h.listUsers)
			r.With(middleware.RequirePermission("iam.user.read", d.Log)).
				Get("/users/{id}", h.getUser)
			// 改用户状态是高风险动作：要写权限 + 近期重认证
			r.With(
				middleware.RequirePermission("iam.user.write", d.Log),
			).Post("/users/{id}/status", h.setUserStatus)
			r.With(
				// 改别人的密码属于「出事没法补救」那一类，保留重认证。
				middleware.RequireRecentReauth(d.Log),
				middleware.RequirePermission("iam.user.write", d.Log),
			).Post("/users/{id}/reset-password", h.resetUserPassword)
			r.With(
				// 换掉别人的订阅链接会让他的客户端立刻断，同样保留重认证。
				middleware.RequireRecentReauth(d.Log),
				middleware.RequirePermission("iam.user.write", d.Log),
			).Post("/subscriptions/{id}/rotate", h.rotateSubscriptionLink)

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

			// --- 套餐 ---
			r.With(middleware.RequirePermission("catalog.read", d.Log)).
				Get("/plans", h.listPlans)
			r.With(middleware.RequirePermission("catalog.read", d.Log)).
				Get("/plans/{id}", h.getPlan)
			r.With(
				middleware.RequirePermission("catalog.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_plan_create", d.Log),
			).Post("/plans", h.createPlan)
			// 向导一次就能建价格、绑线路、发布版本（D-C-2），门槛与单独的
			// 发布 / 改价接口相同：catalog.publish + 近期重认证。
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				// 独立的幂等域：和 /plans 共用一个 scope 的话，两个接口的
				// 幂等键会互相撞 —— 同一个 key 在这边建过整套餐，在那边
				// 就会被当成重放。
				middleware.Idempotency(d.Pool, "catalog_plan_create_complete", d.Log),
			).Post("/plans/complete", h.createPlanComplete)
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_plan_update_complete", d.Log),
			).Put("/plans/{id}/complete", h.updatePlanComplete)
			registerCatalogPlanUpdate(r, d, h.updatePlan, middleware.Idempotency)
			r.With(
				middleware.RequirePermission("catalog.write", d.Log),
				middleware.Idempotency(d.Pool, "catalog_plan_version_create", d.Log),
			).Post("/plans/{id}/versions", h.createPlanVersion)
			r.With(middleware.RequirePermission("catalog.write", d.Log)).
				Put("/plans/{id}/versions/{versionID}", h.updatePlanVersion)
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_plan_version_publish", d.Log),
			).Post("/plans/{id}/versions/{versionID}/publish", h.publishPlanVersion)
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_price_create", d.Log),
			).Post("/plans/{id}/prices", h.createPlanPrice)
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_price_archive", d.Log),
			).Post("/plans/{id}/prices/{priceID}/archive", h.archivePlanPrice)
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_plan_archive", d.Log),
			).Post("/plans/{id}/archive", h.archivePlan)

			// --- 支付渠道 ---
			r.With(middleware.RequirePermission("billing.payment.read", d.Log)).
				Get("/payment-providers", h.listProviders)
			r.With(
				middleware.RequirePermission("billing.provider.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Post("/payment-providers/{code}/toggle", h.toggleProvider)

			// --- 审计 ---
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/audit", h.listAudit)
			// 系统状态：备份跑没跑、数据库多大。备份原先是彻底的盲区 ——
			// 每天都在正常跑，但面板上看不到；定时器哪天坏了同样没人发现。
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/system/status", h.systemStatus)

			// --- 风控画像 ---
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/users/{id}/profile", h.userProfile)
			// 全站访问明细。挂同一个权限：它展示的是解密后的明文 IP，
			// 和用户画像是同一级别的敏感数据。
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/access-log", h.accessLogList)
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/stats/timeseries", h.statsTimeseries)
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/ip-clusters", h.ipClusters)

			// 优惠券
			r.With(middleware.RequirePermission("marketing.coupon.read", d.Log)).
				Get("/coupons", h.listCoupons)
			r.With(
				middleware.RequirePermission("marketing.coupon.write", d.Log),
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

			// 邮件设置
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/settings/mail", h.getMailSettings)
			r.With(
				middleware.RequirePermission("platform.settings.write", d.Log),
			).Post("/settings/mail", h.setMailSettings)
			r.With(middleware.RequirePermission("billing.provider.write", d.Log)).
				Post("/settings/mail/test", h.testMailSettings)

			// 通知模板：内容可改，但 code 不能新建 ——
			// code 是代码里的常量，后台建一个没人调用的模板只会误导人。
			// 测试发送要求近期重认证：它会往任意地址真发信。
			r.With(middleware.RequirePermission("ops.notification.read", d.Log)).
				Get("/mail/templates", h.listMailTemplates)
			r.With(
				middleware.RequirePermission("platform.settings.write", d.Log),
			).Post("/mail/templates", h.saveMailTemplate)
			r.With(
				middleware.RequirePermission("platform.settings.write", d.Log),
			).Post("/mail/templates/reset", h.resetMailTemplate)
			r.With(
				middleware.RequirePermission("billing.provider.write", d.Log),
			).Post("/mail/templates/test", h.testMailTemplate)

			// 节点分组：节点与套餐之间的连接层
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/node-pools", h.listNodePools)
			r.With(middleware.RequirePermission("node.provision", d.Log)).
				Post("/node-pools", h.createNodePool)
			r.With(middleware.RequirePermission("node.provision", d.Log)).
				Post("/node-pools/{id}", h.updateNodePool)
			r.With(middleware.RequirePermission("node.provision", d.Log)).
				Delete("/node-pools/{id}", h.deleteNodePool)
			r.With(middleware.RequirePermission("catalog.read", d.Log)).
				Get("/plans/{id}/pools", h.planPools)
			r.With(
				middleware.RequirePermission("catalog.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "catalog_plan_pools_update", d.Log),
			).Post("/plans/{id}/pools", h.setPlanPools)

			// 余额人工调账。挂在 billing.provider.write 下 ——
			// 能凭空加钱的权限不该跟「看看用户资料」是同一级
			r.With(
				middleware.RequirePermission("billing.provider.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "admin_user_balance_adjust", d.Log),
			).Post("/users/{id}/balance", h.adjustBalance)

			// 公告。运营角色不依赖套餐目录权限；写操作要求近期重认证与幂等键。
			r.With(middleware.RequirePermission("ops.announcement.write", d.Log)).
				Get("/announcements", h.listAnnouncements)
			r.With(
				middleware.RequirePermission("ops.announcement.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "announcement_save", d.Log),
			).
				Post("/announcements", h.saveAnnouncement)
			r.With(
				middleware.RequirePermission("ops.announcement.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "announcement_save", d.Log),
			).
				Post("/announcements/{id}", h.saveAnnouncement)
			r.With(
				middleware.RequirePermission("ops.announcement.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "announcement_withdraw", d.Log),
			).
				Post("/announcements/{id}/withdraw", h.withdrawAnnouncement)

			// 版本化知识库/自定义页面。所有写操作都要求近期重认证与幂等键，
			// 正文只作为纯文本源存储和传输，不进入可信 HTML 边界。
			r.With(middleware.RequirePermission("ops.content.write", d.Log)).
				Get("/content-pages", h.listContentPages)
			r.With(middleware.RequirePermission("ops.content.write", d.Log)).
				Get("/content-pages/{id}", h.getContentPage)
			r.With(
				middleware.RequirePermission("ops.content.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "content_page_version_create", d.Log),
			).Post("/content-pages", h.publishContentVersion)
			r.With(
				middleware.RequirePermission("ops.content.write", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "content_page_archive", d.Log),
			).Post("/content-pages/{id}/archive", h.archiveContentPage)

			// 用户分组：套餐可见、专属价格、优惠券限定、公告定向都靠它
			r.With(middleware.RequirePermission("iam.user.read", d.Log)).
				Get("/user-groups", h.listUserGroups)
			r.With(middleware.RequirePermission("iam.user.write", d.Log)).
				Post("/user-groups", h.saveUserGroup)
			r.With(middleware.RequirePermission("iam.user.write", d.Log)).
				Post("/user-groups/{id}", h.saveUserGroup)
			r.With(middleware.RequirePermission("iam.user.write", d.Log)).
				Delete("/user-groups/{id}", h.deleteUserGroup)
			r.With(middleware.RequirePermission("iam.user.write", d.Log)).
				Post("/users/{id}/group", h.assignUserGroup)

			// --- 节点（NODE / AGT）---
			// --- 设备数限制 ---
			r.With(middleware.RequirePermission("iam.user.read", d.Log)).
				Get("/devices", h.listOnlineDevices)
			r.With(middleware.RequirePermission("iam.user.write", d.Log)).
				Post("/subscriptions/{id}/device-limit", h.setDeviceLimit)
			// 切换模式会影响所有人能否连上，与改支付渠道同级，要求近期重认证
			r.With(
				middleware.RequirePermission("iam.user.write", d.Log),
			).Post("/settings/device-limit", h.setDeviceMode)

			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/nodes", h.nodeList)
			r.With(
				middleware.RequirePermission("node.provision", d.Log),
				middleware.Idempotency(d.Pool, "node_create", d.Log),
			).Post("/nodes", h.createAdminNode)
			r.With(middleware.RequirePermission("node.write", d.Log)).
				Patch("/nodes/{id}", h.patchAdminNode)
			r.With(
				middleware.RequirePermission("node.provision", d.Log),
				middleware.Idempotency(d.Pool, "node_copy", d.Log),
			).Post("/nodes/{id}/copy", h.copyAdminNode)
			r.With(
				middleware.RequirePermission("node.provision", d.Log),
				middleware.Idempotency(d.Pool, "node_server_move", d.Log),
			).Post("/nodes/{id}/move", h.moveAdminNode)
			r.With(
				middleware.RequirePermission("node.write", d.Log),
			).Put("/nodes/order", h.reorderAdminNodes)
			r.With(
				middleware.RequirePermission("node.lifecycle", d.Log),
				middleware.Idempotency(d.Pool, "node_status_batch", d.Log),
			).Post("/nodes/status:batch", h.batchAdminNodeStatus)
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/node-protocol-schemas", h.nodeProtocolSchemas)

			// --- Server 物理宿主 ---
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/servers", h.serverList)
			r.With(
				middleware.RequirePermission("node.write", d.Log),
			).Post("/servers", h.serverCreate)
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/servers/{id}", h.serverGet)
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/servers/{id}/nodes", h.serverNodes)
			r.With(middleware.RequirePermission("node.write", d.Log)).
				Patch("/servers/{id}", h.serverPatch)
			r.With(
				middleware.RequirePermission("node.lifecycle", d.Log),
			).Post("/servers/{id}/status", h.serverSetStatus)
			r.With(
				middleware.RequirePermission("node.lifecycle", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Delete("/servers/{id}", h.serverDelete)
			r.With(
				middleware.RequirePermission("node.provision", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "node_bootstrap_token_issue", d.Log),
			).Post("/nodes/bootstrap-token", h.nodeIssueToken)

			r.With(
				middleware.RequirePermission("node.provision", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "server_bootstrap_token_issue", d.Log),
			).Post("/servers/{id}/bootstrap-token", h.serverIssueToken)

			// REALITY 密钥对生成。只读权限就能调：它不碰任何现存数据，
			// 生成一对没人用的密钥本身不构成风险，而把它锁在写权限后面
			// 只会让「先生成看看」这个自然动作变得别扭。
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Post("/nodes/reality-keypair", h.nodeRealityKeypair)

			// 删除节点。要 lifecycle 权限 + 近期重认证：它会连带删掉这个节点
			// 全部的指标与流量上报（外键是 CASCADE），撤不回来。
			r.With(
				middleware.RequirePermission("node.lifecycle", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Delete("/nodes/{id}", h.nodeDelete)
			r.With(
				middleware.RequirePermission("node.lifecycle", d.Log),
				middleware.Idempotency(d.Pool, "node_legacy_status", d.Log),
			).
				Post("/nodes/{id}/status", h.nodeSetStatus)

			// --- 节点批量操作 ---
			// 批量改状态的处理器早就写好了，却一直没接进路由 —— 后台点不到。
			// retired 就是这个模型里的「删除」：节点有历史，不做物理删除。
			r.With(
				middleware.RequirePermission("node.lifecycle", d.Log),
				middleware.Idempotency(d.Pool, "node_batch_status", d.Log),
			).Post("/nodes/batch/status", h.batchAdminNodeStatus)
			// 没有批量移动：单节点移动要求节点停用且不带任何 agent 资产
			// （身份、指标、任务、流量上报、有效令牌…），也就是只有从没用过的
			// 草稿节点能移。批量化一个「几乎总是被拒绝」的操作没有意义，
			// 而复制那套守卫必然和单节点路径分叉。
			r.With(
				middleware.RequirePermission("node.identity.revoke", d.Log),
				middleware.RequireRecentReauth(d.Log),
			).Post("/nodes/{id}/revoke-identity", h.nodeRevokeIdentity)
			r.With(
				middleware.RequirePermission("node.config.publish", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "node_config_publish", d.Log),
			).Post("/nodes/config/publish", h.nodePublishConfig)
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/nodes/{id}/metrics", h.nodeMetrics)
			r.With(middleware.RequirePermission("node.write", d.Log)).
				Post("/nodes/{id}/protocol", h.nodeSetProtocol)
			r.With(middleware.RequirePermission("node.read", d.Log)).
				Get("/nodes/{id}/routing", h.nodeGetRouting)
			r.With(middleware.RequirePermission("node.config.publish", d.Log)).
				Put("/nodes/{id}/routing", h.nodeSetRouting)
			r.With(
				middleware.RequirePermission("node.provision", d.Log),
				middleware.RequireRecentReauth(d.Log),
				middleware.Idempotency(d.Pool, "node_server_token_issue", d.Log),
			).Post("/nodes/{id}/server-token", h.nodeIssueServerToken)

			// --- 工单（OPS-001）---
			// 读写分权：ops.ticket.read 能看队列，处理与指派要 ops.ticket.write
			r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
				Get("/tickets/assignees", h.ticketAssignees)
			r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
				Get("/tickets", h.ticketQueue)
			r.With(middleware.RequirePermission("ops.ticket.read", d.Log)).
				Get("/tickets/{id}", h.ticketDetail)
			r.With(
				middleware.RequirePermission("ops.ticket.write", d.Log),
				middleware.Idempotency(d.Pool, support.AgentReplyIdempotencyScope, d.Log),
			).
				Post("/tickets/{id}/reply", h.ticketReply)
			r.With(
				middleware.RequirePermission("ops.ticket.write", d.Log),
				middleware.Idempotency(d.Pool, support.AssignIdempotencyScope, d.Log),
			).
				Post("/tickets/{id}/assign", h.ticketAssign)
			r.With(
				middleware.RequirePermission("ops.ticket.write", d.Log),
				middleware.Idempotency(d.Pool, support.StatusIdempotencyScope, d.Log),
			).
				Post("/tickets/{id}/status", h.ticketStatus)
			r.With(
				middleware.RequirePermission("ops.ticket.write", d.Log),
				middleware.Idempotency(d.Pool, support.EscalateIdempotencyScope, d.Log),
			).
				Post("/tickets/escalate", h.ticketEscalate)

			// --- 降级开关 ---
			r.With(middleware.RequirePermission("security.audit.read", d.Log)).
				Get("/switches", h.listSwitches)
			r.With(
				middleware.RequirePermission("platform.settings.write", d.Log),
			).Post("/switches/{code}", h.setSwitch)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, d.Log, httpx.NotFoundOrForbidden())
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, d.Log, httpx.NotFoundOrForbidden())
	})

	return r
}
