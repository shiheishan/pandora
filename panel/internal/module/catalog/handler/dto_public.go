package handler

import (
	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// portalPlan 是门户套餐目录里的一项。
type portalPlan struct {
	ID                 string        `json:"id"`
	Code               string        `json:"code"`
	Name               string        `json:"name"`
	Description        *string       `json:"description"`
	Version            *int          `json:"version"`
	MaxDevices         *int          `json:"max_devices"`
	ThrottleKbps       *int          `json:"throttle_kbps"`
	Highlights         []string      `json:"highlights"`
	Recommended        bool          `json:"recommended"`
	QuotaResetStrategy string        `json:"quota_reset_strategy"`
	QuotaResetDay      *int          `json:"quota_reset_day"`
	AllowRenewal       bool          `json:"allow_renewal"`
	AllowUpgrade       bool          `json:"allow_upgrade"`
	Quotas             []portalQuota `json:"quotas"`
	Prices             []portalPrice `json:"prices"`
}

type portalQuota struct {
	Metric string `json:"metric"`
	Limit  *int64 `json:"limit"`
	Unit   string `json:"unit"`
	Period string `json:"period"`
}

type portalPrice struct {
	ID            string `json:"id"`
	Currency      string `json:"currency"`
	UnitAmount    int64  `json:"unit_amount"`
	Interval      string `json:"billing_interval"`
	IntervalCount int    `json:"interval_count"`
}

// portalPack 是门户可买的一个流量包。
type portalPack struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TrafficBytes int64  `json:"traffic_bytes"`
	Currency     string `json:"currency"`
	UnitAmount   int64  `json:"unit_amount"`
	Recommended  bool   `json:"recommended"`
}

type portalPlansResponse struct {
	Plans []portalPlan `json:"plans"`
}

type portalTrafficPacksResponse struct {
	Packs []portalPack `json:"packs"`
}

func portalPlanFrom(op service.OfferPlan) portalPlan {
	pl := op.Plan
	ver := op.Version
	v := ver.Version
	hl := pl.Highlights
	if hl == nil {
		hl = []string{}
	}
	quotas := make([]portalQuota, len(ver.Quotas))
	for i, q := range ver.Quotas {
		quotas[i] = portalQuota{Metric: q.Metric, Limit: q.Limit, Unit: q.Unit, Period: q.Period}
	}
	prices := make([]portalPrice, len(op.Prices))
	for i, pr := range op.Prices {
		prices[i] = portalPrice{
			ID:            pr.ID.String(),
			Currency:      string(pr.Currency),
			UnitAmount:    money.MustCents(pr.UnitAmount),
			Interval:      pr.BillingInterval,
			IntervalCount: pr.IntervalCount,
		}
	}
	return portalPlan{
		ID:                 pl.ID.String(),
		Code:               pl.Code,
		Name:               pl.Name,
		Description:        pl.Description,
		Version:            &v,
		MaxDevices:         ver.MaxDevices,
		ThrottleKbps:       ver.ThrottleKbps,
		Highlights:         hl,
		Recommended:        pl.Recommended,
		QuotaResetStrategy: ver.QuotaResetStrategy,
		QuotaResetDay:      ver.QuotaResetDay,
		AllowRenewal:       pl.AllowRenewal,
		AllowUpgrade:       pl.AllowUpgrade,
		Quotas:             quotas,
		Prices:             prices,
	}
}

func portalPackFrom(p service.TrafficPack) portalPack {
	return portalPack{
		ID:           p.ID.String(),
		Name:         p.Name,
		TrafficBytes: p.TrafficBytes,
		Currency:     string(p.Currency),
		UnitAmount:   money.MustCents(p.UnitAmount),
		Recommended:  p.Recommended,
	}
}

func portalPlansFrom(offers []service.OfferPlan) []portalPlan {
	out := make([]portalPlan, len(offers))
	for i, op := range offers {
		out[i] = portalPlanFrom(op)
	}
	return out
}

func portalPacksFrom(packs []service.TrafficPack) []portalPack {
	out := make([]portalPack, len(packs))
	for i, p := range packs {
		out[i] = portalPackFrom(p)
	}
	return out
}
