// Package handler 是 commission 模块的 HTTP 层：门户我的佣金、提现申请、佣金转余额；后台分销概览、提现审核与打款、分销参数。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 幂等作用域（沿用旧路由）。
const (
	WithdrawalRequestScope = "commission_withdrawal_request"
	TransferScope          = "commission_transfer_to_balance"
	MarkPaidScope          = "commission_withdrawal_mark_paid"
)

// Service 是 handler 用到的 commission service 方法；*service.Service 实现它。
type Service interface {
	MyCommission(ctx context.Context, userID uuid.UUID) (service.MyCommission, error)
	RequestWithdrawal(ctx context.Context, userID uuid.UUID, amount decimal.Decimal, payoutDetail string) (uuid.UUID, error)
	TransferToBalance(ctx context.Context, userID uuid.UUID, amount decimal.Decimal) (uuid.UUID, error)
	AdminOverview(ctx context.Context) (service.Overview, error)
	AdminWithdrawals(ctx context.Context, status string) ([]service.AdminWithdrawal, error)
	ReviewWithdrawal(ctx context.Context, id uuid.UUID, approve bool, reason string) (string, error)
	MarkWithdrawalPaid(ctx context.Context, id uuid.UUID, reference string) error
	SetConfig(ctx context.Context, u service.ConfigUpdate) error
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 commission 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic 注册门户佣金路由；提现与转余额动钱，网络重试必须拿回同一个结果，挂幂等。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/me/commission", k.RequireAuth, h.myCommission)
	v1.POST("/me/withdrawals", k.RequireAuth, k.Idempotency(WithdrawalRequestScope), h.requestWithdrawal)
	v1.POST("/me/commission/transfer", k.RequireAuth, k.Idempotency(TransferScope), h.transferToBalance)
}

// RegisterAdmin 注册后台分销路由；审核、打款、改比例都要近期重认证，打款另挂幂等。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/commission/overview", k.RequireAuth, h.overview)
	v1.GET("/withdrawals", k.RequireAuth, h.listWithdrawals)
	v1.POST("/withdrawals/:id/review", k.RequireAuth, k.RecentReauth, h.reviewWithdrawal)
	v1.POST("/withdrawals/:id/paid", k.RequireAuth, k.RecentReauth, k.Idempotency(MarkPaidScope), h.markWithdrawalPaid)
	v1.POST("/commission/config", k.RequireAuth, k.RecentReauth, h.setConfig)
}
