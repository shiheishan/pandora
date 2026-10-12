package handler

import (
	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// portalPlan 与旧 billing.CatalogPlan JSON 一致。
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
	QuotaResetDay      *int16        `json:"quota_reset_day"`
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
	IntervalCount int16  `json:"interval_count"`
	TrialDays     int16  `json:"trial_days"`
}

// portalPack 与旧 billing.TrafficPack JSON 一致。
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
	var resetDay *int16
	if ver.QuotaResetDay != nil {
		d := int16(*ver.QuotaResetDay)
		resetDay = &d
	}
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
			IntervalCount: int16(pr.IntervalCount),
			TrialDays:     int16(pr.TrialDays),
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
		QuotaResetDay:      resetDay,
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
