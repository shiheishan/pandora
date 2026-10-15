// Package handler 是 subscription 模块的 HTTP 层：订阅链接拉取、链接列表与换链接、线路预览、按日用量、后台换链接，
// 门户「我的套餐」、改名、流量包与转移，后台加时长、加流量、手动重置流量、改设备数与重置日志，
// 以及订阅域名分组与探测（门户票据、探测页、上报、公开探测接口）。只做参数绑定与校验、调 service、写响应。
package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// 后台写操作的幂等作用域。
const (
	ExtendIdempotencyScope       = "subscription_admin_extend"
	TrafficGrantIdempotencyScope = "subscription_admin_traffic_grant"
	TrafficResetIdempotencyScope = "traffic_manual_reset"
)

// renamePerMinute 是门户改名按账号每分钟的次数上限。
const renamePerMinute = 10

// Service 是 handler 用到的 subscription service 方法；*service.Service 实现它。
type Service interface {
	MySubscriptions(ctx context.Context) (service.MySubscriptions, error)
	SetMyLabel(ctx context.Context, subID uuid.UUID, label *string) (service.LabelResult, error)
	MyTrafficPacks(ctx context.Context) (service.PackBalance, error)
	MyTransferPacks(ctx context.Context, from *uuid.UUID, to uuid.UUID) (int64, error)
	AdminExtend(ctx context.Context, subID uuid.UUID, days int, reason string) (service.AdminExtendResult, error)
	AdminGrantTraffic(ctx context.Context, subID uuid.UUID, bytes int64, reason string) (service.AdminTrafficGrantResult, error)
	AdminResetTraffic(ctx context.Context, subID uuid.UUID, note string) (int64, error)
	AdminSetDeviceLimit(ctx context.Context, subID uuid.UUID, limit *int) error
	ListTrafficResets(ctx context.Context, f service.ResetLogFilter) ([]service.ResetLogView, int64, error)
	TrafficResetStats(ctx context.Context) (service.ResetStats, error)
	Pull(ctx context.Context, req service.PullRequest) (service.PullResult, error)
	MyLinks(ctx context.Context) ([]service.Link, error)
	MyRotate(ctx context.Context, subID uuid.UUID) (string, error)
	MyNodes(ctx context.Context, subID uuid.UUID) ([]service.NodePreview, error)
	MyUsage(ctx context.Context, subID uuid.UUID, days int) (service.DailyUsage, error)
	AdminRotate(ctx context.Context, subID uuid.UUID, reason string) (service.AdminRotateResult, error)
	AdminSetPeriodEnd(ctx context.Context, subID uuid.UUID, end time.Time, reason string) (service.AdminSetPeriodEndResult, error)
	AdminSetQuotaLimit(ctx context.Context, subID uuid.UUID, metric, period string, limit *int64, reason string) error
	TemplateService
	DomainService
}

var _ Service = (*service.Service)(nil)
var (
	_ route.Module     = (*Handler)(nil)
	_ route.RootModule = (*Handler)(nil)
)

// Handler 是 subscription 模块的 HTTP 入口。
type Handler struct {
	svc Service
	lim *ratelimit.Limiter // nil 时不挂限流（单测用）
	// pullPerMinPerIP 是订阅拉取按来源 IP 每分钟的上限（配置 ratelimit.ip_per_min）；网段上限是它的 8 倍
	pullPerMinPerIP int
}

// New 构造 Handler；pullPerMinPerIP 见 Handler。
func New(svc Service, lim *ratelimit.Limiter, pullPerMinPerIP int) *Handler {
	return &Handler{svc: svc, lim: lim, pullPerMinPerIP: pullPerMinPerIP}
}

// limit 挂限流；没有 Limiter 时放行。
func (h *Handler) limit(dims ...ratelimit.Dimension) gin.HandlerFunc {
	if h.lim == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return h.lim.Middleware(dims...)
}

// byAccount 以登录主体的用户 ID 为限流键。
func byAccount(c *gin.Context) (string, bool) {
	p, ok := authn.FromGin(c)
	if !ok {
		return "", false
	}
	return p.UserID.String(), true
}

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/me/subscriptions", k.RequireAuth, h.mySubscriptions)
	// 改名不影响节点下发，按账号每分钟 10 次
	v1.PATCH("/me/subscriptions/:id", k.RequireAuth,
		h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_rename", renamePerMinute), Key: byAccount}),
		h.renameSubscription)
	v1.GET("/me/traffic-packs", k.RequireAuth, h.myTrafficPacks)
	// 不要幂等键：重复调用第二次没有东西可转，moved_bytes=0
	v1.POST("/me/traffic-packs/transfer", k.RequireAuth, h.transferTrafficPacks)
	v1.GET("/me/subscription-links", k.RequireAuth, h.mySubscriptionLinks)
	// 换链接按份限频：每份间隔 10 分钟、每份每天 5 次，加按账号每天 20 次（rotateLimit）
	v1.POST("/me/subscriptions/:id/rotate", k.RequireAuth, h.rotateLimit(), h.rotateSubscriptionLink)
	v1.GET("/me/subscriptions/:id/nodes", k.RequireAuth, h.mySubscriptionNodes)
	v1.GET("/me/subscriptions/:id/usage", k.RequireAuth, h.mySubscriptionUsage)
	// 订阅域名探测：门户登录后领票据（按账号每小时 6 次），隐藏 iframe 打开探测页，探测页凭票据上报（票据鉴权，按 IP 限流）
	v1.POST("/sub-probe/ticket", k.RequireAuth,
		h.limit(ratelimit.Dimension{Rule: ratelimit.PerWindow("sub_probe_ticket", probeTicketsPerHour, time.Hour), Key: byAccount}),
		h.probeTicket)
	v1.GET("/sub-probe/frame", h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_probe_frame_ip", probeFramePerMinute), Key: ratelimit.ByIP}), h.probeFrame)
	v1.POST("/sub-probe/report", h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_probe_report_ip", probeReportPerMinute), Key: ratelimit.ByIP}), h.probeReport)
	// 公开探测接口：订阅域名上浏览器跨源 GET/HEAD，只回 204；CORS 头先于限流写（429 也带）
	subProbeLimit := h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_probe_ip", subProbePerMinute), Key: ratelimit.ByIP})
	v1.GET("/sub-probe", h.probeCORS, subProbeLimit, h.subProbe)
	v1.HEAD("/sub-probe", h.probeCORS, subProbeLimit, h.subProbe)
}

// RegisterAdmin 注册后台路由。加时长、加流量每执行一次多一份，要近期重认证与独立的幂等作用域。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/subscriptions/:id/extend", k.RequireAuth, k.RecentReauth, k.Idempotency(ExtendIdempotencyScope), h.adminExtend)
	v1.POST("/subscriptions/:id/traffic-pack", k.RequireAuth, k.RecentReauth, k.Idempotency(TrafficGrantIdempotencyScope), h.adminGrantTraffic)
	v1.POST("/subscriptions/:id/traffic-reset", k.RequireAuth, k.RecentReauth, k.Idempotency(TrafficResetIdempotencyScope), h.adminResetTraffic)
	v1.POST("/subscriptions/:id/device-limit", k.RequireAuth, h.adminSetDeviceLimit)
	v1.GET("/traffic-resets", k.RequireAuth, h.listTrafficResets)
	v1.GET("/traffic-resets/stats", k.RequireAuth, h.trafficResetStats)
	v1.GET("/users/:id/traffic-resets", k.RequireAuth, h.userTrafficResets)
	// 换掉别人的链接会让他所有设备断开：要近期重认证；不是「多一份」的操作，不挂幂等
	v1.POST("/subscriptions/:id/rotate", k.RequireAuth, k.RecentReauth, h.adminRotateLink)
	v1.POST("/subscriptions/:id/period-end", k.RequireAuth, k.RecentReauth, h.adminSetPeriodEnd)
	v1.POST("/subscriptions/:id/quota", k.RequireAuth, k.RecentReauth, h.adminSetQuotaLimit)
	// 改模板影响所有用户的订阅，保存与恢复缺省要近期重认证；预览只渲染示例节点、不写库
	v1.GET("/subscription-templates", k.RequireAuth, h.adminTemplates)
	v1.GET("/subscription-templates/:format", k.RequireAuth, h.adminTemplate)
	v1.PUT("/subscription-templates/:format", k.RequireAuth, k.RecentReauth, h.adminSaveTemplate)
	v1.DELETE("/subscription-templates/:format", k.RequireAuth, k.RecentReauth, h.adminResetTemplate)
	v1.POST("/subscription-templates/:format/preview", k.RequireAuth, h.adminPreviewTemplate)
	// 订阅域名分组：整份替换，影响之后新生成的所有订阅链接；保存要近期重认证（审计在 service）
	v1.GET("/subscription-domains", k.RequireAuth, h.adminDomains)
	v1.PUT("/subscription-domains", k.RequireAuth, k.RecentReauth, h.adminSaveDomains)
}

// RegisterRoot 注册订阅链接拉取 GET /:prefix/:token：公开、不在 /v1 下（链接里出现 /v1/subscribe 这样的字样等于自带指纹）。
// 前缀写在每条链接里、不是秘密，所以按来源 IP 与网段限流（限流键 pub_ip、pub_net），挡住拿前缀乱试令牌对库的放大。
func (h *Handler) RegisterRoot(r gin.IRoutes) {
	r.GET("/:prefix/:token", h.limit(
		ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_pull_ip", h.pullPerMinPerIP), Key: ratelimit.ByIP},
		ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_pull_net", 8*h.pullPerMinPerIP), Key: ratelimit.ByIPPrefix},
	), h.subscribe)
}

// utc 把时间转成 UTC 输出。
func utc(t time.Time) time.Time { return t.UTC() }

// utcPtr 把可空时间转成 UTC 输出。
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
