// Package handler 是 promo 模块的 HTTP 层：后台优惠券管理、单个用户的会员折扣与门户优惠码试算。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 promo service 方法；*service.Service 实现它。
type Service interface {
	CreateCoupon(ctx context.Context, code string, spec service.Spec) (uuid.UUID, string, error)
	GenerateCoupons(ctx context.Context, prefix string, count int, spec service.Spec) ([]string, error)
	SetStatus(ctx context.Context, id uuid.UUID, status string) error
	ListCoupons(ctx context.Context, f service.ListFilter, limit, offset int) ([]service.Coupon, int64, error)
	Redemptions(ctx context.Context, id uuid.UUID) ([]service.Redemption, error)
	Preview(ctx context.Context, userID uuid.UUID, code string, item service.PreviewItem) (service.Preview, error)
	MemberDiscountOf(ctx context.Context, userID uuid.UUID) (service.MemberDiscountView, error)
	SetMemberDiscount(ctx context.Context, userID uuid.UUID, percent int) (service.MemberDiscountView, error)
	ClearMemberDiscount(ctx context.Context, userID uuid.UUID) error
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 promo 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterAdmin 注册后台优惠券路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/coupons", k.RequireAuth, h.listCoupons)
	v1.GET("/coupons/:id/redemptions", k.RequireAuth, h.couponRedemptions)
	v1.POST("/coupons", k.RequireAuth, k.RecentReauth, h.createCoupon)
	v1.POST("/coupons/batch", k.RequireAuth, k.RecentReauth, k.Idempotency("coupon_batch_generate"), h.generateCoupons)
	v1.POST("/coupons/:id/status", k.RequireAuth, k.RecentReauth, h.setCouponStatus)
	// 单个用户的会员折扣（xb-order #33）：改钱的口径，写操作要近期重认证并写审计
	v1.GET("/member-discounts/:user_id", k.RequireAuth, h.getMemberDiscount)
	v1.PUT("/member-discounts/:user_id", k.RequireAuth, k.RecentReauth, h.setMemberDiscount)
	v1.DELETE("/member-discounts/:user_id", k.RequireAuth, k.RecentReauth, h.clearMemberDiscount)
}

// RegisterPublic 注册门户优惠码试算路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/coupons/preview", k.RequireAuth, h.previewCoupon)
}
