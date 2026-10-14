// Package handler 是 commission 模块的 HTTP 层：门户我的佣金、提现申请、佣金转余额；后台分销概览、提现审核与打款、分销参数、待复核佣金、按用户单独的佣金设置。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 幂等作用域。
const (
	WithdrawalRequestScope = "commission_withdrawal_request"
	TransferScope          = "commission_transfer_to_balance"
	MarkPaidScope          = "commission_withdrawal_mark_paid"
)

// Service 是 handler 用到的 commission service 方法；*service.Service 实现它。
type Service interface {
	MyCommission(ctx context.Context, userID uuid.UUID) (service.MyCommission, error)
	RequestWithdrawal(ctx context.Context, userID uuid.UUID, amount decimal.Decimal, method, payoutDetail string) (uuid.UUID, error)
	TransferToBalance(ctx context.Context, userID uuid.UUID, amount decimal.Decimal) (uuid.UUID, error)
	AdminOverview(ctx context.Context) (service.Overview, error)
	AdminWithdrawals(ctx context.Context, status string) ([]service.AdminWithdrawal, error)
	ReviewWithdrawal(ctx context.Context, id uuid.UUID, approve bool, reason string) (string, error)
	MarkWithdrawalPaid(ctx context.Context, id uuid.UUID, reference string, paidUSDT *decimal.Decimal) error
	SetConfig(ctx context.Context, u service.ConfigUpdate) error
	AdminReviewEntries(ctx context.Context, cursor string, limit int) (service.ReviewEntryPage, error)
	ReviewEntry(ctx context.Context, id uuid.UUID, approve bool, reason string) (string, error)
	ListOverrides(ctx context.Context) ([]service.UserOverride, error)
	SetOverride(ctx context.Context, in service.OverrideInput) (service.UserOverride, error)
	DeleteOverride(ctx context.Context, userID uuid.UUID) error
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

// RegisterAdmin 注册后台分销路由；审核、打款、改比例、佣金复核都要近期重认证，打款另挂幂等。
// 佣金复核与提现审核一样不挂幂等：只对待复核的条目有效，重放只会得到 409。
// 单独佣金设置的写操作要近期重认证、不挂幂等（设置是幂等覆盖，删除重放回 404）。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/commission/overview", k.RequireAuth, h.overview)
	v1.GET("/withdrawals", k.RequireAuth, h.listWithdrawals)
	v1.POST("/withdrawals/:id/review", k.RequireAuth, k.RecentReauth, h.reviewWithdrawal)
	v1.POST("/withdrawals/:id/paid", k.RequireAuth, k.RecentReauth, k.Idempotency(MarkPaidScope), h.markWithdrawalPaid)
	v1.POST("/commission/config", k.RequireAuth, k.RecentReauth, h.setConfig)
	v1.GET("/commission/entries", k.RequireAuth, h.listReviewEntries)
	v1.POST("/commission/entries/:id/review", k.RequireAuth, k.RecentReauth, h.reviewEntry)
	v1.GET("/commission/overrides", k.RequireAuth, h.listOverrides)
	v1.POST("/commission/overrides", k.RequireAuth, k.RecentReauth, h.setOverride)
	v1.DELETE("/commission/overrides/:user_id", k.RequireAuth, k.RecentReauth, h.deleteOverride)
}
