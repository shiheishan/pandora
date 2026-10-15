// Package handler 是 catalog 模块的 HTTP 层：后台的套餐、版本、价格、流量包管理，门户的公开目录。
//
// 金额在 JSON 里一律是整数分（与前端约定不变），进 service 前换成 decimal 元，出来再换回分。
// 后台列表里的「在用订阅数」「可服务节点数」「流量包已售份数」属于别的模块，经可选端口取；没装配时为 0。
package handler

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 catalog service 方法；*service.Service 实现它。
type Service interface {
	ListPlans(ctx context.Context) ([]service.PlanSummary, error)
	GetPlan(ctx context.Context, planID uuid.UUID) (service.PlanDetail, error)
	CreatePlan(ctx context.Context, in service.PlanInput) (service.PlanDetail, error)
	UpdatePlan(ctx context.Context, planID uuid.UUID, expected int64, in service.PlanInput) (int64, error)
	ArchivePlan(ctx context.Context, planID uuid.UUID, expected int64) (int64, error)
	CreatePlanComplete(ctx context.Context, in service.WizardCreate) (service.WizardCreated, error)
	UpdatePlanComplete(ctx context.Context, planID uuid.UUID, in service.WizardUpdate) (service.WizardUpdated, error)
	CreateVersion(ctx context.Context, planID uuid.UUID) (service.Version, error)
	UpdateVersion(ctx context.Context, planID, versionID uuid.UUID, expected int64, in service.VersionInput) (int64, error)
	PublishVersion(ctx context.Context, planID, versionID uuid.UUID, expectedPlan, expectedVersion int64) (int64, int64, error)
	CreatePrice(ctx context.Context, planID uuid.UUID, in service.PriceInput) (service.Price, error)
	ArchivePrice(ctx context.Context, planID, priceID uuid.UUID, expected int64) (int64, error)
	ListTrafficPacks(ctx context.Context, status string) ([]service.TrafficPack, error)
	ActiveTrafficPacks(ctx context.Context) ([]service.TrafficPack, error)
	CreateTrafficPack(ctx context.Context, in service.PackInput) (service.TrafficPack, error)
	UpdateTrafficPack(ctx context.Context, packID uuid.UUID, expected *time.Time, in service.PackInput) (service.TrafficPack, error)
	SetTrafficPackStatus(ctx context.Context, packID uuid.UUID, status string, expected *time.Time) (service.TrafficPack, error)
	PlanResetPrices(ctx context.Context, planID uuid.UUID) ([]service.ResetPrice, error)
	ReplacePlanResetPrices(ctx context.Context, planID uuid.UUID, in []service.ResetPrice) ([]service.ResetPrice, error)
	PortalCatalog(ctx context.Context, userID *uuid.UUID) ([]service.OfferPlan, error)
}

// SubscriptionCounter 由 subscription 模块实现：每个套餐在用（未到期）的订阅数，一条 SQL。
type SubscriptionCounter interface {
	LiveSubscriptionCounts(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

// NodeCounter 由 node 模块实现：每个版本绑定的线路里可服务的节点数，一条 SQL。
type NodeCounter interface {
	ServingNodeCounts(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

// PackSales 由 order 模块实现：每个流量包已成交的份数，一条 SQL。
type PackSales interface {
	PackSoldCounts(ctx context.Context, packIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 catalog 模块的 HTTP 入口。
type Handler struct {
	svc   Service
	subs  SubscriptionCounter
	nodes NodeCounter
	sales PackSales
}

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// UseSubscriptionCounter 只在启动装配时调用。
func (h *Handler) UseSubscriptionCounter(c SubscriptionCounter) { h.subs = c }

// UseNodeCounter 只在启动装配时调用。
func (h *Handler) UseNodeCounter(c NodeCounter) { h.nodes = c }

// UsePackSales 只在启动装配时调用。
func (h *Handler) UsePackSales(s PackSales) { h.sales = s }

// RegisterPublic 注册门户路由：目录不要求登录（带了登录态就按用户组过滤可见性）。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, _ route.Kit) {
	v1.GET("/plans", h.portalPlans)
	v1.GET("/traffic-packs", h.portalTrafficPacks)
}

// RegisterAdmin 注册后台路由。改价、发布、上下架等会影响售卖的写接口要近期重认证。
// 节点池绑定（/plans/:id/pools）由 node 模块注册（node/handler/routes_pools.go）。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/plans", k.RequireAuth, h.listPlans)
	v1.GET("/plans/:id", k.RequireAuth, h.getPlan)
	v1.POST("/plans", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_create"), h.createPlan)
	v1.POST("/plans/complete", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_create_complete"), h.createPlanComplete)
	v1.PUT("/plans/:id/complete", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_update_complete"), h.updatePlanComplete)
	v1.PUT("/plans/:id", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_update"), h.updatePlan)
	v1.POST("/plans/:id/archive", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_archive"), h.archivePlan)
	v1.POST("/plans/:id/versions", k.RequireAuth, k.Idempotency("catalog_plan_version_create"), h.createVersion)
	v1.PUT("/plans/:id/versions/:versionID", k.RequireAuth, h.updateVersion)
	v1.POST("/plans/:id/versions/:versionID/publish", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_version_publish"), h.publishVersion)
	v1.POST("/plans/:id/prices", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_price_create"), h.createPrice)
	v1.POST("/plans/:id/prices/:priceID/archive", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_price_archive"), h.archivePrice)

	v1.GET("/traffic-packs", k.RequireAuth, h.listTrafficPacks)
	v1.POST("/traffic-packs", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_traffic_pack_create"), h.createTrafficPack)
	v1.PUT("/traffic-packs/:id", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_traffic_pack_update"), h.updateTrafficPack)
	v1.POST("/traffic-packs/:id/status", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_traffic_pack_status"), h.setTrafficPackStatus)

	v1.GET("/plans/:id/reset-prices", k.RequireAuth, h.getPlanResetPrices)
	v1.PUT("/plans/:id/reset-prices", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_reset_prices"), h.putPlanResetPrices)
}
