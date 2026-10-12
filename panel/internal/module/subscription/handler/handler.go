// Package handler 是 subscription 模块的 HTTP 层：门户「我的套餐」、改名、流量包与转移，后台加时长、加流量、
// 手动重置流量、改设备数与重置日志。只做参数绑定与校验、调 service、写响应。
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

// 后台写操作的幂等作用域（沿用旧名）。
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
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 subscription 模块的 HTTP 入口。
type Handler struct {
	svc Service
	lim *ratelimit.Limiter // nil 时不挂限流（单测用）
}

// New 构造 Handler。
func New(svc Service, lim *ratelimit.Limiter) *Handler { return &Handler{svc: svc, lim: lim} }

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

// RegisterPublic 注册门户路由。/me/subscriptions/:id/nodes 归 P5，订阅链接归 P4。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/me/subscriptions", k.RequireAuth, h.mySubscriptions)
	// 改名不影响节点下发，按账号每分钟 10 次
	v1.PATCH("/me/subscriptions/:id", k.RequireAuth,
		h.limit(ratelimit.Dimension{Rule: ratelimit.PerMinute("sub_rename", renamePerMinute), Key: byAccount}),
		h.renameSubscription)
	v1.GET("/me/traffic-packs", k.RequireAuth, h.myTrafficPacks)
	// 不要幂等键：重复调用第二次没有东西可转，moved_bytes=0
	v1.POST("/me/traffic-packs/transfer", k.RequireAuth, h.transferTrafficPacks)
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
