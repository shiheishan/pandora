// Package handler 是 payment 模块的 HTTP 层：门户支付方式与渠道回调、后台渠道与挂账。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 payment service 方法；*service.Service 实现它。
type Service interface {
	PaymentMethods(ctx context.Context) ([]service.PaymentMethod, error)
	HandleWebhook(ctx context.Context, code string, in provider.Inbound) (provider.AckOutput, error)
	ListProviders(ctx context.Context) ([]service.ProviderView, error)
	CreateProvider(ctx context.Context, in service.CreateProviderInput) (service.ProviderWriteResult, error)
	UpdateProvider(ctx context.Context, code string, in service.ProviderSettings) (service.ProviderWriteResult, error)
	ToggleProvider(ctx context.Context, code string, enabled, acceptingNew bool) error
	ListLatePayments(ctx context.Context, status string, limit, offset int) (service.LateList, error)
	ApplyLatePayment(ctx context.Context, caseID uuid.UUID, reason string) (uuid.UUID, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 payment 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// CreateProviderScope 是新建渠道的幂等作用域。
const CreateProviderScope = "payment_provider_create"

// UpdateProviderScope 是编辑渠道的幂等作用域。
const UpdateProviderScope = "payment_provider_update"

// ApplyLateScope 是挂账转入余额的幂等作用域。
const ApplyLateScope = "late_payment_apply"

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/payment-methods", k.RequireAuth, h.paymentMethods)
	v1.GET("/webhooks/payments/:provider", h.paymentWebhook)
	v1.POST("/webhooks/payments/:provider", h.paymentWebhook)
}

// RegisterAdmin 注册后台路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/payment-providers", k.RequireAuth, h.listProviders)
	v1.POST("/payment-providers/:code/toggle", k.RequireAuth, k.RecentReauth, h.toggleProvider)
	v1.POST("/payment-providers", k.RequireAuth, k.RecentReauth, k.Idempotency(CreateProviderScope), h.createProvider)
	v1.PUT("/payment-providers/:code", k.RequireAuth, k.RecentReauth, k.Idempotency(UpdateProviderScope), h.updateProvider)
	v1.GET("/late-payments", k.RequireAuth, h.listLatePayments)
	v1.POST("/late-payments/:id/apply-to-balance", k.RequireAuth, k.RecentReauth, k.Idempotency(ApplyLateScope), h.applyLatePayment)
}
