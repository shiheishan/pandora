package handler

import (
	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// portalPrice 是门户展示的一档在售价；unit_amount 是整数分。
type portalPrice struct {
	ID              string `json:"id"`
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	UnitAmount      int64  `json:"unit_amount"`
	ValidityDays    *int   `json:"validity_days"`
}

// portalReset 是门户展示的流量重置价；整个 reset 在套餐没开重置时为 null。
type portalReset struct {
	Price int64 `json:"price"`
	Limit *int  `json:"limit"`
}

// portalPlan 是用户端目录里的一项：当前发布版本的用量与这次能用的价格。
type portalPlan struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Kind        string  `json:"kind"`
	Recommended bool    `json:"recommended"`
	// AllowNewPurchase 为假：不接新购（另买一份、一次性叠加），已持有的照 AllowRenewal 续；门户选购页据此不列
	AllowNewPurchase bool          `json:"allow_new_purchase"`
	AllowRenewal     bool          `json:"allow_renewal"`
	TrafficBytes     *int64        `json:"traffic_bytes"`
	MaxDevices       *int          `json:"max_devices"`
	ThrottleKbps     *int          `json:"throttle_kbps"`
	Prices           []portalPrice `json:"prices"`
	Reset            *portalReset  `json:"reset"`
}

type portalPlansResponse struct {
	Plans []portalPlan `json:"plans"`
}

// portalPlanFrom 组装用户端目录里的一项。
func portalPlanFrom(op service.OfferPlan) portalPlan {
	prices := make([]portalPrice, len(op.Prices))
	for i, p := range op.Prices {
		prices[i] = portalPrice{
			ID:              p.ID.String(),
			BillingInterval: p.BillingInterval,
			IntervalCount:   p.IntervalCount,
			UnitAmount:      money.MustCents(p.UnitAmount),
			ValidityDays:    p.ValidityDays,
		}
	}
	return portalPlan{
		ID:               op.Plan.ID.String(),
		Name:             op.Plan.Name,
		Description:      op.Plan.Description,
		Kind:             op.Plan.Kind,
		Recommended:      op.Plan.Recommended,
		AllowNewPurchase: op.Plan.AllowNewPurchase,
		AllowRenewal:     op.Plan.AllowRenewal,
		TrafficBytes:     op.Version.TrafficLimit(),
		MaxDevices:       op.Version.MaxDevices,
		ThrottleKbps:     op.Version.ThrottleKbps,
		Prices:           prices,
		Reset:            portalResetFrom(op.Plan.ResetPrice, op.Plan.ResetLimit),
	}
}

// portalResetFrom 把套餐的流量重置价格与次数上限转成响应；没开重置（价格为 nil）时整个 reset 为 null。
func portalResetFrom(price *decimal.Decimal, limit *int) *portalReset {
	if price == nil {
		return nil
	}
	return &portalReset{Price: money.MustCents(*price), Limit: limit}
}

func portalPlansFrom(offers []service.OfferPlan) []portalPlan {
	out := make([]portalPlan, len(offers))
	for i, op := range offers {
		out[i] = portalPlanFrom(op)
	}
	return out
}
