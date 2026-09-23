// [INPUT]: 依赖 domain/* 各服务（经 Deps 注入）、middleware 的鉴权/限流/严格限流/幂等链、platform/webapp 的 Mount 与 web.PortalApp
// [OUTPUT]: 对外提供 Deps、NewRouter：public 网关的完整 chi 路由表
// [POS]: api/public 的装配点：/ 手写门户、/app/ React 候选、/{prefix}/{token} 订阅分发、/pdnd 安装引导、/v1 用户 API；字面量路由优先于订阅通配
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package public 实现用户门户 API（Public 域）。
package public

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/config"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/token"
	"github.com/aegispanel/aegis/internal/platform/webapp"
	"github.com/aegispanel/aegis/web"
)

type Deps struct {
	Cfg          *config.Config
	Pool         *db.Pool
	Redis        *redis.Client
	Log          *slog.Logger
	Issuer       *token.Issuer
	Envelope     *platformcrypto.Envelope
	Identity     *identity.Service
	Billing      *billing.Service
	Payments     *billing.PaymentService
	Support      *support.Service
	Subscription *subscription.Service
	Realtime     *realtime.Hub
	// Notify 这里只用来读公告；投递由后台扫描器负责
	Notify *notify.Service
	// Content 提供按套餐、平台与客户端版本过滤的已发布知识库。
	Content *content.Service
	// GiftCard 礼品卡兑换。判断「该不该发」在这里，
	// 「怎么发」交回给计费域，避免两套记账逻辑
	GiftCard *giftcard.Service
	// TelegramSender 供 webhook 回消息给用户。绑定成功与否都要回一句，
	// 否则用户对着 Telegram 干等。
	TelegramSender *notify.DynamicTelegramSender
	// Appearance 主题与插槽，用户端渲染要用
	Appearance *appearance.Service
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	// 全局链：顺序有讲究 ——
	// RequestID 最先（后续所有日志都要带它），Recovery 紧随（要能兜住后面所有 panic），
	// Authenticate 在 DomainGuard 之前（先解析出主体才能判断它属于哪个域）。
	r.Use(middleware.RequestID)
	r.Use(middleware.ClientInfo)
	r.Use(middleware.Recovery(d.Log))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.Timeout(25 * time.Second))
	r.Use(middleware.Tenant)
	r.Use(middleware.Authenticate(d.Pool, d.Issuer, d.Log))
	r.Use(middleware.DomainGuard(string(config.DomainPublic), d.Log))

	// 基础限流：IP + 网段两个维度联合，单换 IP 绕不过网段（SEC-002）
	r.Use(middleware.RateLimit(d.Redis, d.Log,
		middleware.ByIP("pub_ip", time.Minute, d.Cfg.RateLimitPerIPPerMinute),
		middleware.ByIPPrefix("pub_net", time.Minute, d.Cfg.RateLimitPerIPPerMinute*8),
	))

	h := &handlers{d: d}

	// 用户门户单页。放在 /v1 之外，与 API 命名空间互不干扰。
	// 同时注册 HEAD：健康探针与 CDN 预检常用 HEAD，只注册 GET 会让它们收到 404。
	r.Get("/", h.portal)
	r.Head("/", h.portal)
	// React 候选门户。字面量 /app 优先于下面的 /{prefix}/{token}，
	// 而订阅前缀是 12 位十六进制（迁移 00018），永远不会等于 app。
	webapp.Mount(r, "/app", web.PortalApp)

	r.Get("/healthz", h.health)
	r.Get("/readyz", h.ready)

	// 订阅分发。
	//
	// 放在 /v1 之外：订阅链接会被用户复制到各种客户端里，甚至截图发出去，
	// 里面出现 /v1/subscribe 这样的字样等于自带指纹。两段随机路径
	// （租户前缀 + token）看上去和一个静态资源没有区别。
	//
	// chi 里字面量路由优先于通配符，因此这条不会抢走 /healthz、/v1/... 的请求。
	r.Get("/{prefix}/{token}", h.subscribe)

	// Pandora NativeCore 一键安装。字面量 /pdnd 优先于上面那条通配订阅路由。
	// 不放在 /v1 下：这些是给 curl | sh 用的，路径越短越不容易抄错。
	r.Route("/pdnd", func(r chi.Router) {
		r.Get("/install.sh", h.pdndInstallScript)
		r.Get("/bin/{name}", h.pdndBinary)
		r.Get("/sha256/{name}", h.pdndChecksum)
	})

	r.Route("/v1", func(r chi.Router) {
		// --- 认证入口：额外收紧限流（SEC-005 撞库防护）---
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(d.Redis, d.Log,
				middleware.ByRoute("auth_route", time.Minute, d.Cfg.RateLimitAuthPerMinute),
				middleware.ByIPPrefix("auth_net", 10*time.Minute, d.Cfg.RateLimitAuthPerMinute*6),
			))
			r.Post("/auth/login", h.login)
			// 快捷登录的消费端。放在免鉴权区 —— 拿着链接来的人还没有身份。
			r.Post("/auth/quick-login", h.quickLogin)
		})

		// Registration mutates anonymous identity state and therefore fails closed
		// when Redis is unavailable. Subject keys are hashed before entering Redis.
		r.With(middleware.RateLimitStrict(d.Redis, d.Log,
			middleware.ByRoute("reg_start_ip", time.Minute, d.Cfg.RateLimitAuthPerMinute),
			middleware.ByIPPrefix("reg_start_net", 10*time.Minute, d.Cfg.RateLimitAuthPerMinute*6),
			middleware.ByTenant("reg_tenant", time.Hour, d.Cfg.RateLimitAuthPerMinute*20),
			middleware.ByJSONFieldHash("reg_email", "email", 10*time.Minute, 5,
				func(v string) string { return strings.ToLower(strings.TrimSpace(v)) }),
			middleware.ByJSONFieldHash("reg_invite", "invite_code", 10*time.Minute, 20,
				func(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }),
		)).Post("/auth/register/start", h.registerStart)

		r.With(middleware.RateLimitStrict(d.Redis, d.Log,
			middleware.ByRoute("reg_complete_ip", time.Minute, d.Cfg.RateLimitAuthPerMinute),
			middleware.ByIPPrefix("reg_complete_net", 10*time.Minute, d.Cfg.RateLimitAuthPerMinute*6),
			middleware.ByTenant("reg_tenant", time.Hour, d.Cfg.RateLimitAuthPerMinute*20),
			middleware.ByJSONFieldHash("reg_token", "registration_token", 10*time.Minute, 6,
				strings.TrimSpace),
		)).Post("/auth/register/complete", h.registerComplete)

		// --- 公开目录 ---
		r.Get("/site-config", h.siteConfig)
		// 外观免鉴权：登录页本身就要按主题渲染，而这里没有任何
		// 用户数据 —— 主题和插槽是站点公开的门面。
		r.Get("/appearance", h.appearance)
		r.Get("/plans", h.listPlans)

		// --- 需登录 ---
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAuth(d.Log))
			r.Use(middleware.RateLimit(d.Redis, d.Log,
				middleware.ByAccount("acct", time.Minute, d.Cfg.RateLimitPerAccountPerMinute),
			))

			r.Post("/auth/logout", h.logout)
			r.Get("/me", h.me)
			r.Get("/me/subscriptions", h.listSubscriptions)
			r.Get("/me/subscriptions/{id}/nodes", h.meSubscriptionNodes)
			// 实时事件流。放在需要登录的分组内 —— 订阅范围就是鉴权边界
			r.Get("/events", h.events)
			r.Get("/me/subscription-links", h.meSubscriptionLinks)
			r.Get("/me/invite", h.myInviteCode)
			r.Post("/coupons/preview", h.previewCoupon)
			r.Get("/me/commission", h.myCommission)
			r.Post("/me/password", h.changePassword)
			r.Post("/me/withdrawals", h.requestWithdrawal)
			r.Get("/me/balance", h.myBalance)
			r.With(middleware.Idempotency(d.Pool, billing.TopupIdempotencyScope, d.Log)).
				Post("/me/topups", h.createTopup)
			r.Get("/me/announcements", h.myAnnouncements)
			r.Get("/content/pages", h.listContentPages)
			r.Get("/content/pages/{slug}", h.getContentPage)
			r.Get("/me/notifications", h.listNotifications)
			r.Post("/me/notifications/read-all", h.markAllNotificationsRead)
			r.Post("/me/notifications/{id}/read", h.markNotificationRead)
			r.Get("/me/notification-preferences", h.getNotificationPreferences)
			r.Put("/me/notification-preferences", h.setNotificationPreference)
			r.Post("/me/subscriptions/{id}/rotate", h.rotateSubscriptionLink)
			r.With(middleware.Idempotency(d.Pool, "subscription_renewal_create", d.Log)).
				Post("/me/subscriptions/{id}/renew", h.createRenewal)

			// 下单要求幂等键：用户网络抖动重发不能变成两张订单
			r.With(middleware.Idempotency(d.Pool, "order_create", d.Log)).
				Post("/orders", h.createOrder)

			// 发起支付：返回收银台跳转地址。
			// 不加网关级幂等 —— 复用在途意图的逻辑在服务层用行锁做，
			// 用户连点两次会拿回同一个收银台而不是报幂等冲突。
			r.Post("/orders/{id}/pay", h.payOrder)
			r.Post("/orders/{id}/cancel", h.cancelOrder)

			// --- 礼品卡兑换 ---
			// 兑换加幂等键：用户网络抖动重发不能变成两次兑换。码本身的行锁
			// 已经能防住重复发放，幂等键让重发拿回同一个结果，
			// 而不是一句让人困惑的「卡密无效」。
			r.Post("/gift-cards/preview", h.previewGiftCard)
			r.With(middleware.Idempotency(d.Pool, "gift_card_redeem", d.Log)).
				Post("/gift-cards/redeem", h.redeemGiftCard)
			r.Get("/me/gift-cards", h.myGiftRedemptions)

			// --- Telegram 绑定 ---
			r.Get("/me/telegram", h.telegramInfo)
			r.Post("/me/telegram/bind-code", h.telegramBindCode)
			r.Delete("/me/telegram", h.telegramUnbind)

			// 订单中心：此前用户下完单就再也看不到它了，
			// 连「上周那单到底付没付成」都只能开工单问。
			r.Get("/orders", h.listMyOrders)
			r.Get("/orders/{id}", h.myOrderDetail)

			// --- 工单（OPS-001）---
			r.Get("/support/categories", h.listTicketCategories)
			r.Get("/support/tickets", h.listTickets)
			r.Get("/support/tickets/{id}", h.getTicket)
			r.With(middleware.Idempotency(
				d.Pool, support.UserReplyIdempotencyScope, d.Log,
			)).Post("/support/tickets/{id}/reply", h.replyTicket)
			r.With(middleware.Idempotency(
				d.Pool, support.UserCloseIdempotencyScope, d.Log,
			)).Post("/support/tickets/{id}/close", h.closeTicket)
			// 撤回：与关闭的区别在 closed_reason，直接影响客服解决率的统计口径
			r.Post("/support/tickets/{id}/withdraw", h.withdrawTicket)

			// --- 用户自助 ---
			// 佣金转余额：提现要审批要等打款，而多数人只是想拿佣金续费
			r.With(middleware.Idempotency(d.Pool, billing.CommissionTransferIdempotencyScope, d.Log)).
				Post("/me/commission/transfer", h.transferCommission)
			// 登录会话：在别人电脑上登录过，回家能把那个会话踢掉，
			// 而不必改密码把所有设备一起踹下线
			r.Get("/me/sessions", h.listMySessions)
			r.Delete("/me/sessions/{id}", h.revokeMySession)
			// 快捷登录：在已登录的设备上生成一条 60 秒的免密链接，
			// 换台设备打开就进来了，不用在手机上敲长密码
			r.Post("/me/quick-login", h.issueQuickLogin)

			// 建单单独收紧限流：这是唯一允许匿名内容进入客服视野的入口，
			// 也是最容易被拿来刷屏的地方（SEC-002）
			r.With(
				middleware.RateLimit(d.Redis, d.Log,
					middleware.ByAccount("ticket_create", time.Hour, 10),
				),
				middleware.Idempotency(d.Pool, support.CreateIdempotencyScope, d.Log),
			).Post("/support/tickets", h.createTicket)
		})

		// --- 支付回调 ---
		// 不走用户认证：调用方是支付渠道，身份由回调签名证明。
		//
		// 易支付用 GET 推送通知，其他渠道多用 POST，两个都接。
		// 这里不叠网关级幂等中间件：那个中间件基于 Idempotency-Key 请求头，
		// 而支付渠道不会发这个头。真正的幂等在 payment_events 的
		// (provider_id, provider_event_id) 唯一约束上（PAY-003）。
		r.Get("/webhooks/payments/{provider}", h.paymentWebhook)
		r.Post("/webhooks/payments/{provider}", h.paymentWebhook)
		// Telegram Bot 的回调。鉴权靠地址里的 secret 段 —— Telegram
		// 只会把更新推给我们设置的那个 URL，地址本身就是凭证。
		r.Post("/webhooks/telegram/{secret}", h.telegramUpdate)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, d.Log, httpx.NotFoundOrForbidden())
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, d.Log, httpx.NotFoundOrForbidden())
	})

	return r
}
