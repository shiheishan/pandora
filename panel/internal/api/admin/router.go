// [INPUT]: 依赖 domain/* 各服务（经 Deps 注入）、middleware 的鉴权/限流/幂等/权限链、platform/webapp 的 Mount 与 web.AdminApp
// [OUTPUT]: 对外提供 Deps、NewRouter：admin 网关的完整 chi 路由表
// [POS]: api/admin 的装配点：全局中间件链、根 / 与 /assets/* 经 webapp 下发后台前端、/v1 登录与已登录分组；分组内的业务路由由 router_<模块>.go 的 register*Routes 按序注册
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
		// admin.writes 关闭时整棵 /v1 只读（切开关、登录、重认证、改密码除外）
		r.Use(middleware.AdminWritesGate(d.Pool, d.Log))

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

			// 业务路由按模块分在 router_<模块>.go，逐路由的权限、重认证与幂等
			// scope 声明在那里。调用顺序就是拆分前的注册顺序，不要重排
			registerDashboardRoutes(r, d, h)
			registerThemeRoutes(r, d, h)
			registerPluginHookRoutes(r, d, h)
			registerTelegramRoutes(r, d, h)
			registerUserBulkRoutes(r, d, h)
			registerTrafficResetRoutes(r, d, h)
			registerGiftCardRoutes(r, d, h)
			registerLatePaymentRoutes(r, d, h)
			registerUserRoutes(r, d, h)
			registerOrderRoutes(r, d, h)
			registerPlanRoutes(r, d, h)
			registerTrafficPackRoutes(r, d, h)
			registerPaymentProviderRoutes(r, d, h)
			registerAuditRoutes(r, d, h)
			registerRiskRoutes(r, d, h)
			registerCouponRoutes(r, d, h)
			registerCommissionRoutes(r, d, h)
			registerMailSettingsRoutes(r, d, h)
			registerMailTemplateRoutes(r, d, h)
			registerNodePoolRoutes(r, d, h)
			registerBalanceAdjustRoutes(r, d, h)
			registerAnnouncementRoutes(r, d, h)
			registerContentPageRoutes(r, d, h)
			registerUserGroupRoutes(r, d, h)
			registerDeviceLimitRoutes(r, d, h)
			registerNodeRoutes(r, d, h)
			registerTicketRoutes(r, d, h)
			registerSwitchRoutes(r, d, h)
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
