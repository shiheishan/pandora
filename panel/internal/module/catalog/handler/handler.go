// Package handler 是 catalog 模块的 HTTP 层：后台的套餐（分块编辑）、门户的公开目录。
//
// 金额在 JSON 里一律是整数分（money.FromCents / money.MustCents 换算），流量一律字节。
// 后台列表与详情里的「在用订阅数」「可服务节点数」属于别的模块，经可选端口取；没装配时计数为 0。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 catalog service 方法；*service.Service 实现它。
type Service interface {
	ListPlans(ctx context.Context, filter service.PlanFilter) ([]service.PlanSummary, error)
	GetPlan(ctx context.Context, planID uuid.UUID) (service.PlanDetail, error)
	CreatePlan(ctx context.Context, in service.PlanCreate) (service.PlanDetail, error)
	UpdateBasic(ctx context.Context, planID uuid.UUID, in service.BasicUpdate) (service.PlanDetail, error)
	UpdateSales(ctx context.Context, planID uuid.UUID, in service.SalesUpdate) (service.PlanDetail, error)
	UpdateReset(ctx context.Context, planID uuid.UUID, in service.ResetUpdate) (service.PlanDetail, error)
	UpdatePrices(ctx context.Context, planID uuid.UUID, in service.PricesUpdate) (service.PlanDetail, error)
	SetPlanPools(ctx context.Context, planID uuid.UUID, in service.PoolsUpdate) (service.PlanDetail, error)
	UpdateUsage(ctx context.Context, planID uuid.UUID, in service.UsageUpdate) (service.PlanDetail, error)
	PublishVersion(ctx context.Context, planID, versionID uuid.UUID, expected int64) (service.PlanDetail, error)
	ArchivePlan(ctx context.Context, planID uuid.UUID, expected int64) (service.PlanDetail, error)
	PortalCatalog(ctx context.Context, userID *uuid.UUID) ([]service.OfferPlan, error)
}

// SubscriptionCounter 由 subscription 模块实现：每个套餐在用（未到期）的订阅数，一条 SQL。
type SubscriptionCounter interface {
	LiveSubscriptionCounts(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

// NodeCounter 由 node 模块实现：每个套餐绑定线路里可服务的节点数，一条 SQL。
type NodeCounter interface {
	ServingNodeCounts(ctx context.Context, planIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 catalog 模块的 HTTP 入口。
type Handler struct {
	svc   Service
	subs  SubscriptionCounter
	nodes NodeCounter
}

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// UseSubscriptionCounter 只在启动装配时调用。
func (h *Handler) UseSubscriptionCounter(c SubscriptionCounter) { h.subs = c }

// UseNodeCounter 只在启动装配时调用。
func (h *Handler) UseNodeCounter(c NodeCounter) { h.nodes = c }

// RegisterPublic 注册门户路由：公开目录不要求登录（带了登录态就按用户组过滤可见性）。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, _ route.Kit) {
	v1.GET("/plans", h.portalPlans)
}

// RegisterAdmin 注册后台路由。套餐按块编辑：基本信息、销售、流量重置、价格、线路、用量各自一个接口，
// 都带 expected_row_version 做乐观锁；影响售卖的写接口要近期重认证。
// `GET /plans/:id/pools`（绑池选择器）由 node 模块注册。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/plans", k.RequireAuth, h.listPlans)
	v1.GET("/plans/:id", k.RequireAuth, h.getPlan)
	v1.POST("/plans", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_create"), h.createPlan)
	v1.PUT("/plans/:id/basic", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_basic"), h.updateBasic)
	v1.PUT("/plans/:id/sales", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_sales"), h.updateSales)
	v1.PUT("/plans/:id/reset", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_reset"), h.updateReset)
	v1.PUT("/plans/:id/prices", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_prices"), h.updatePrices)
	v1.PUT("/plans/:id/pools", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_pools"), h.updatePools)
	v1.PUT("/plans/:id/usage", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_usage"), h.updateUsage)
	v1.POST("/plans/:id/versions/:versionID/publish", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_version_publish"), h.publishVersion)
	v1.POST("/plans/:id/archive", k.RequireAuth, k.RecentReauth, k.Idempotency("catalog_plan_archive"), h.archivePlan)
}

// counts 一次批量取列表或单个套餐的在用订阅数与可服务节点数；端口没装配时计数为 0。
func (h *Handler) counts(ctx context.Context, planIDs []uuid.UUID) (subs, nodes map[uuid.UUID]int64, err error) {
	subs = map[uuid.UUID]int64{}
	nodes = map[uuid.UUID]int64{}
	if len(planIDs) == 0 {
		return subs, nodes, nil
	}
	if h.subs != nil {
		m, err := h.subs.LiveSubscriptionCounts(ctx, planIDs)
		if err != nil {
			return nil, nil, err
		}
		if m != nil {
			subs = m
		}
	}
	if h.nodes != nil {
		m, err := h.nodes.ServingNodeCounts(ctx, planIDs)
		if err != nil {
			return nil, nil, err
		}
		if m != nil {
			nodes = m
		}
	}
	return subs, nodes, nil
}
