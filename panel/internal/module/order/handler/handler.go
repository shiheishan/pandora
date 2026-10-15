// Package handler 是 order 模块的 HTTP 层：门户报价、下单（含换套餐）、支付、取消、查单与我的订单；
// 后台订单列表、详情、取消、标记已支付、查单与人工开单。
package handler

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/service"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// Service 是 handler 用到的 order service 方法；*service.Service 实现它。
type Service interface {
	Quote(ctx context.Context, in service.QuoteInput) (service.QuoteResult, error)
	CreateNewOrder(ctx context.Context, in service.NewOrderInput) (service.Created, error)
	CreateRenewalOrder(ctx context.Context, in service.RenewalOrderInput) (service.Created, error)
	CreatePackOrder(ctx context.Context, in service.PackOrderInput) (service.Created, error)
	CreatePlanChangeOrder(ctx context.Context, in service.PlanChangeOrderInput) (service.Created, error)
	ManualPreview(ctx context.Context, in service.ManualPreviewInput) (service.ManualPreview, error)
	CreateManualOrder(ctx context.Context, in service.ManualOrderInput) (service.Created, error)
	CreateTopup(ctx context.Context, in service.TopupInput) (service.TopupCreated, error)
	Pay(ctx context.Context, in service.PayInput) (paymentsvc.IntentResult, error)
	Query(ctx context.Context, orderID uuid.UUID) (service.QueryResult, error)
	AdminQuery(ctx context.Context, orderID uuid.UUID) (service.QueryResult, error)
	MarkPaid(ctx context.Context, in service.MarkPaidInput) (service.MarkPaidResult, error)
	Cancel(ctx context.Context, in service.CancelInput) (service.CancelResult, error)
	MyOrders(ctx context.Context, f service.ListFilter) (service.MyOrderList, error)
	MyOrder(ctx context.Context, id uuid.UUID) (service.MyOrderDetail, error)
	AdminOrders(ctx context.Context, f service.ListFilter) (service.AdminOrderList, error)
	AdminOrder(ctx context.Context, id uuid.UUID) (service.AdminOrderDetail, error)
	AdminOrderPayments(ctx context.Context, id uuid.UUID) (paymentsvc.OrderHistory, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 幂等作用域（流量包、新购与后台人工单共用 order_create）。
const (
	ScopeOrderCreate      = "order_create"
	ScopeRenewalCreate    = "subscription_renewal_create"
	ScopePlanChangeCreate = "subscription_change_plan_create"
	ScopeTopupCreate      = "balance_topup_create"
	ScopeAdminCancel      = "admin_order_cancel"
	ScopeAdminMarkPaid    = "admin_order_mark_paid"
	ScopeAdminQuery       = "admin_order_query"
)

// SwitchCheckout 是下单与支付的功能开关；取消、查单、我的订单不受它管。
const SwitchCheckout = "billing.checkout"

// PayPerMinute 是门户发起支付每个账号每分钟的上限（加密货币渠道每次都要去渠道建发票）。
const PayPerMinute = 10

// QueryPerMinute 是门户查单每个账号每分钟的上限（每查一次都要打渠道接口）。
const QueryPerMinute = 6

// 列表分页口径。
const (
	portalListDefault = 20
	adminListDefault  = 25
	listMax           = 100
)

// Handler 是 order 模块的 HTTP 入口。
type Handler struct {
	svc Service
	lim *ratelimit.Limiter
}

// New 构造 Handler；lim 为 nil 时查单不限流（只用于测试）。
func New(svc Service, lim *ratelimit.Limiter) *Handler { return &Handler{svc: svc, lim: lim} }

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	checkout := k.Switch(SwitchCheckout)
	v1.POST("/orders", k.RequireAuth, checkout, k.Idempotency(ScopeOrderCreate), h.createOrder)
	v1.POST("/me/subscriptions/:id/renew", k.RequireAuth, checkout, k.Idempotency(ScopeRenewalCreate), h.createRenewal)
	// 换套餐有自己的幂等域；金额走统一报价的 action=change
	v1.POST("/me/subscriptions/:id/change-plan", k.RequireAuth, checkout, k.Idempotency(ScopePlanChangeCreate), h.createPlanChange)
	v1.POST("/me/traffic-pack-orders", k.RequireAuth, checkout, k.Idempotency(ScopeOrderCreate), h.createPackOrder)
	v1.POST("/me/topups", k.RequireAuth, checkout, k.Idempotency(ScopeTopupCreate), h.createTopup)
	v1.POST("/me/checkout/quote", k.RequireAuth, checkout, h.quote)
	// 发起支付不加幂等键：在途意图由 payment 复用，连点两次拿回同一个收银台；按账号每分钟 10 次
	v1.POST("/orders/:id/pay", k.RequireAuth, checkout, h.pay)
	v1.POST("/orders/:id/cancel", k.RequireAuth, h.cancel)
	// 查单不挂 checkout 开关（开关只停新支付）；按账号每分钟 6 次
	v1.POST("/orders/:id/query", k.RequireAuth, h.query)
	v1.GET("/orders", k.RequireAuth, h.myOrders)
	v1.GET("/orders/:id", k.RequireAuth, h.myOrder)
}

// RegisterAdmin 注册后台路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/orders", k.RequireAuth, h.adminOrders)
	// 人工单动真金白银或真权益（线下已收款直接记收入）：与标记已支付同门槛，重认证加幂等键；
	// 作用域与新购同为 order_create（人工单本来就是一次建单）。预览只读，不要重认证与幂等键
	v1.POST("/orders/manual", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeOrderCreate), h.createManualOrder)
	v1.POST("/orders/manual/preview", k.RequireAuth, h.previewManualOrder)
	v1.GET("/orders/:id", k.RequireAuth, h.adminOrder)
	v1.GET("/orders/:id/payments", k.RequireAuth, h.adminOrderPayments)
	// 取消与查单不要重认证（取消只退资源，查单造不出钱）
	v1.POST("/orders/:id/cancel", k.RequireAuth, k.Idempotency(ScopeAdminCancel), h.adminCancel)
	v1.POST("/orders/:id/mark-paid", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeAdminMarkPaid), h.markPaid)
	v1.POST("/orders/:id/query", k.RequireAuth, k.Idempotency(ScopeAdminQuery), h.adminQuery)
}

// pathID 解析路径里的 id；格式不对按不存在处理（不告诉试探者哪种格式是对的）。
func pathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}
