package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// listCouponsQuery 是后台券列表的查询参数。
type listCouponsQuery struct {
	Q      string `form:"q" binding:"max=200"`
	Status string `form:"status" binding:"omitempty,oneof=active paused"`
}

// couponRequest 是单张创建与批量生成共用的券规格（JSON 平铺）。
type couponRequest struct {
	Code           string   `json:"code" binding:"max=64"`
	Name           string   `json:"name" binding:"max=200"`
	DiscountType   string   `json:"discount_type" binding:"required,oneof=percent fixed"`
	DiscountValue  int64    `json:"discount_value" binding:"min=1"`
	Currency       string   `json:"currency" binding:"max=8"`
	MaxDiscount    *int64   `json:"max_discount"`
	MinOrder       *int64   `json:"min_order_amount"`
	MaxRedemptions *int     `json:"max_redemptions"`
	MaxPerUser     *int     `json:"max_redemptions_per_user"`
	PlanIDs        []string `json:"applicable_plan_ids" binding:"max=200,dive,uuid"`
	// Intervals 是限定的计费周期（空为不限）；取值与 catalog 价格档的 billing_interval 一致，由 service 校验
	Intervals   []string `json:"applicable_intervals" binding:"max=6,dive,max=16"`
	UserGroupID *string  `json:"user_group_id" binding:"omitempty,uuid"`
	ValidFrom   string   `json:"valid_from" binding:"max=64"`
	ValidUntil  string   `json:"valid_until" binding:"max=64"`
}

// generateCouponsRequest 是批量生成请求（字段与 couponRequest 平铺）。
type generateCouponsRequest struct {
	couponRequest
	Count  int    `json:"count"`
	Prefix string `json:"prefix" binding:"max=16"`
}

// spec 把请求体转成 service.Spec。
func (r couponRequest) spec() (service.Spec, error) {
	var face service.Face
	switch r.DiscountType {
	case service.KindPercent:
		face = service.Face{Kind: service.KindPercent, PercentBP: r.DiscountValue}
	case service.KindFixed:
		face = service.Face{Kind: service.KindFixed, AmountOff: money.FromCents(r.DiscountValue)}
	}

	// 没给币种时留空，由 service 填站点币种
	cur := strings.TrimSpace(r.Currency)

	var maxDisc *decimal.Decimal
	if r.MaxDiscount != nil {
		d := money.FromCents(*r.MaxDiscount)
		maxDisc = &d
	}

	minOrder := decimal.Zero
	if r.MinOrder != nil {
		minOrder = money.FromCents(*r.MinOrder)
	}

	perUser := 1
	if r.MaxPerUser != nil && *r.MaxPerUser > 0 {
		perUser = *r.MaxPerUser
	}

	planIDs := make([]uuid.UUID, 0, len(r.PlanIDs))
	for _, s := range r.PlanIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			return service.Spec{}, errs.Invalid(map[string]string{"applicable_plan_ids": "套餐 id 必须是 uuid"})
		}
		planIDs = append(planIDs, id)
	}

	var groupID *uuid.UUID
	if r.UserGroupID != nil && strings.TrimSpace(*r.UserGroupID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*r.UserGroupID))
		if err != nil {
			return service.Spec{}, errs.Invalid(map[string]string{"user_group_id": "用户组必须是 uuid"})
		}
		groupID = &id
	}

	from, err := parseOptionalRFC3339(r.ValidFrom, "valid_from")
	if err != nil {
		return service.Spec{}, err
	}
	until, err := parseOptionalRFC3339(r.ValidUntil, "valid_until")
	if err != nil {
		return service.Spec{}, err
	}

	return service.Spec{
		Name:           strings.TrimSpace(r.Name),
		Face:           face,
		Currency:       cur,
		MaxDiscount:    maxDisc,
		MinOrder:       minOrder,
		MaxRedemptions: r.MaxRedemptions,
		MaxPerUser:     perUser,
		PlanIDs:        planIDs,
		Intervals:      r.Intervals,
		UserGroupID:    groupID,
		ValidFrom:      from,
		ValidUntil:     until,
	}, nil
}

func parseOptionalRFC3339(v, field string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, errs.Invalid(map[string]string{field: "时间格式不正确"})
	}
	return &t, nil
}

// couponResponse 是后台券列表/详情的一行。
type couponResponse struct {
	ID                    string     `json:"id"`
	Code                  string     `json:"code"`
	Name                  string     `json:"name"`
	DiscountType          string     `json:"discount_type"`
	DiscountValue         int64      `json:"discount_value"`
	Currency              string     `json:"currency"`
	MaxDiscount           *int64     `json:"max_discount"`
	MinOrderAmount        int64      `json:"min_order_amount"`
	MaxRedemptions        *int       `json:"max_redemptions"`
	MaxRedemptionsPerUser int        `json:"max_redemptions_per_user"`
	RedeemedCount         int        `json:"redeemed_count"`
	ApplicablePlanIDs     []string   `json:"applicable_plan_ids"`
	ApplicableIntervals   []string   `json:"applicable_intervals"` // 永不为 null，空为不限
	UserGroupID           *string    `json:"user_group_id"`
	ValidFrom             *time.Time `json:"valid_from"`
	ValidUntil            *time.Time `json:"valid_until"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"created_at"`
	DiscountedTotal       int64      `json:"discounted_total"`
}

// couponFrom 把 service.Coupon 转成响应 DTO。
func couponFrom(c service.Coupon) couponResponse {
	var discType string
	var discVal int64
	switch c.Face.Kind {
	case service.KindPercent:
		discType = service.KindPercent
		discVal = c.Face.PercentBP
	case service.KindFixed:
		discType = service.KindFixed
		discVal = money.MustCents(c.Face.AmountOff)
	}

	var maxDisc *int64
	if c.MaxDiscount != nil {
		n := money.MustCents(*c.MaxDiscount)
		maxDisc = &n
	}

	planIDs := make([]string, len(c.PlanIDs))
	for i, id := range c.PlanIDs {
		planIDs[i] = id.String()
	}

	intervals := c.Intervals
	if intervals == nil {
		intervals = []string{}
	}

	var gid *string
	if c.UserGroupID != nil {
		s := c.UserGroupID.String()
		gid = &s
	}

	return couponResponse{
		ID:                    c.ID.String(),
		Code:                  c.Code,
		Name:                  c.Name,
		DiscountType:          discType,
		DiscountValue:         discVal,
		Currency:              c.Currency,
		MaxDiscount:           maxDisc,
		MinOrderAmount:        money.MustCents(c.MinOrder),
		MaxRedemptions:        c.MaxRedemptions,
		MaxRedemptionsPerUser: c.MaxPerUser,
		RedeemedCount:         c.Redeemed,
		ApplicablePlanIDs:     planIDs,
		ApplicableIntervals:   intervals,
		UserGroupID:           gid,
		ValidFrom:             c.ValidFrom,
		ValidUntil:            c.ValidUntil,
		Status:                c.Status,
		CreatedAt:             c.CreatedAt,
		DiscountedTotal:       money.MustCents(c.DiscountedTotal),
	}
}

// listCouponsResponse 是后台券列表响应。
type listCouponsResponse struct {
	Coupons []couponResponse `json:"coupons"`
	Total   int64            `json:"total"`
}

// redemptionResponse 是一条核销明细。
type redemptionResponse struct {
	Email    string    `json:"email"`
	OrderNo  string    `json:"order_no"`
	Discount int64     `json:"discount"`
	Currency string    `json:"currency"`
	At       time.Time `json:"at"`
	Reverted bool      `json:"reverted"`
}

// redemptionsResponse 是单张券的核销明细列表。
type redemptionsResponse struct {
	Redemptions []redemptionResponse `json:"redemptions"`
}

// redemptionFrom 把 service.Redemption 转成响应 DTO。
func redemptionFrom(r service.Redemption) redemptionResponse {
	return redemptionResponse{
		Email:    r.Email,
		OrderNo:  r.OrderNo,
		Discount: money.MustCents(r.Discount),
		Currency: r.Currency,
		At:       r.At,
		Reverted: r.Released,
	}
}

// createCouponResponse 是单张创建成功响应。
type createCouponResponse struct {
	Code string `json:"code"`
	ID   string `json:"id"`
}

// generateCouponsResponse 是批量生成成功响应。
type generateCouponsResponse struct {
	Codes []string `json:"codes"`
	Count int      `json:"count"`
	Name  string   `json:"name"`
}

// okResponse 是只回 ok 的写操作响应。
type okResponse struct {
	OK bool `json:"ok"`
}

// setStatusRequest 是启用/停用券的请求体。
type setStatusRequest struct {
	Status string `json:"status" binding:"required,max=32"`
}

// previewRequest 是门户优惠码试算请求。
type previewRequest struct {
	PlanID     string `json:"plan_id"`
	PriceID    string `json:"price_id"`
	PackID     string `json:"pack_id"`
	CouponCode string `json:"coupon_code" binding:"max=100"`
}

// previewCouponFace 是试算结果里的券面摘要。
type previewCouponFace struct {
	Code          string `json:"code"`
	DiscountType  string `json:"discount_type"`
	DiscountValue int64  `json:"discount_value"`
}

// previewResponse 是门户试算响应。
type previewResponse struct {
	Subtotal int64              `json:"subtotal"`
	Discount int64              `json:"discount"`
	Payable  int64              `json:"payable"`
	Currency string             `json:"currency"`
	Coupon   *previewCouponFace `json:"coupon"`
}

// previewFrom 把 service.Preview 转成响应 DTO；金额换算失败时由调用方处理。
func previewFrom(p service.Preview) (previewResponse, error) {
	sub, err := money.ToCents(p.Subtotal)
	if err != nil {
		return previewResponse{}, err
	}
	disc, err := money.ToCents(p.Discount)
	if err != nil {
		return previewResponse{}, err
	}
	pay, err := money.ToCents(p.Payable)
	if err != nil {
		return previewResponse{}, err
	}

	var face *previewCouponFace
	if p.Applied != nil {
		var val int64
		switch p.Applied.Face.Kind {
		case service.KindPercent:
			val = p.Applied.Face.PercentBP
		case service.KindFixed:
			val = money.MustCents(p.Applied.Face.AmountOff)
		}
		face = &previewCouponFace{
			Code:          p.Applied.Code,
			DiscountType:  p.Applied.Face.Kind,
			DiscountValue: val,
		}
	}

	return previewResponse{
		Subtotal: sub,
		Discount: disc,
		Payable:  pay,
		Currency: p.Currency,
		Coupon:   face,
	}, nil
}
