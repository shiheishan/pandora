package service

import (
	"context"
	"encoding/json"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// encodeHighlights 把卖点列表编成 jsonb 文本。
func encodeHighlights(highlights []string) (string, error) {
	if highlights == nil {
		highlights = []string{}
	}
	b, err := json.Marshal(highlights)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeHighlights(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return []string{}, nil
	}
	return out, nil
}

func planFromModel(m model.Plan, groups []uuid.UUID) (Plan, error) {
	hl, err := decodeHighlights(m.Highlights)
	if err != nil {
		return Plan{}, err
	}
	if groups == nil {
		groups = []uuid.UUID{}
	}
	return Plan{
		ID:                   m.ID,
		Code:                 m.Code,
		Name:                 m.Name,
		Description:          m.Description,
		Status:               m.Status,
		CurrentVersionID:     m.CurrentVersionID,
		Visibility:           m.Visibility,
		VisibleGroupIDs:      groups,
		VisibleFrom:          m.VisibleFrom,
		VisibleUntil:         m.VisibleUntil,
		AllowNewPurchase:     m.AllowNewPurchase,
		AllowRenewal:         m.AllowRenewal,
		AllowUpgrade:         m.AllowUpgrade,
		PurchaseLimitPerUser: m.PurchaseLimitPerUser,
		StockTotal:           m.StockTotal,
		StockReserved:        m.StockReserved,
		StockSold:            m.StockSold,
		Highlights:           hl,
		Recommended:          m.Recommended,
		SortOrder:            m.SortOrder,
		RowVersion:           m.RowVersion,
		CreatedAt:            m.CreatedAt,
		UpdatedAt:            m.UpdatedAt,
	}, nil
}

func priceFromModel(m model.Price) Price {
	cur := money.Currency(m.Currency)
	return Price{
		ID:              m.ID,
		PlanID:          m.PlanID,
		Currency:        cur,
		UnitAmount:      m.UnitAmount,
		BillingInterval: m.BillingInterval,
		IntervalCount:   m.IntervalCount,
		Status:          m.Status,
		UserGroupID:     m.UserGroupID,
		ValidFrom:       m.ValidFrom,
		ValidUntil:      m.ValidUntil,
		RowVersion:      m.RowVersion,
		CreatedAt:       m.CreatedAt,
	}
}

func packFromModel(m model.TrafficPack) TrafficPack {
	return TrafficPack{
		ID:           m.ID,
		Name:         m.Name,
		TrafficBytes: m.TrafficBytes,
		Currency:     money.Currency(m.Currency),
		UnitAmount:   m.UnitAmount,
		Recommended:  m.Recommended,
		Status:       m.Status,
		SortOrder:    m.SortOrder,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

func versionFromModel(m model.PlanVersion, ents []model.Entitlement, quotas []model.QuotaDefinition) Version {
	outEnts := make([]Entitlement, 0, len(ents))
	for _, e := range ents {
		outEnts = append(outEnts, Entitlement{
			Code:  e.Code,
			Value: json.RawMessage(e.Value),
		})
	}
	outQuotas := make([]Quota, 0, len(quotas))
	for _, q := range quotas {
		outQuotas = append(outQuotas, Quota{
			Metric: q.Metric,
			Limit:  q.LimitValue,
			Unit:   q.Unit,
			Period: q.Period,
		})
	}
	return Version{
		ID:                   m.ID,
		PlanID:               m.PlanID,
		Version:              m.Version,
		Status:               m.Status,
		FrozenAt:             m.FrozenAt,
		QuotaResetStrategy:   m.QuotaResetStrategy,
		QuotaResetDay:        m.QuotaResetDay,
		RenewalExtendsPeriod: m.RenewalExtendsPeriod,
		RenewalResetsQuota:   m.RenewalResetsQuota,
		RenewalKeepsAddons:   m.RenewalKeepsAddons,
		MaxDevices:           m.MaxDevices,
		MaxConcurrent:        m.MaxConcurrent,
		DeviceReleaseHours:   m.DeviceReleaseHours,
		ThrottleKbps:         m.ThrottleKbps,
		Notes:                m.Notes,
		RowVersion:           m.RowVersion,
		CreatedBy:            m.CreatedBy,
		CreatedAt:            m.CreatedAt,
		Entitlements:         outEnts,
		Quotas:               outQuotas,
	}
}

func entitlementModels(ents []Entitlement) []model.Entitlement {
	out := make([]model.Entitlement, len(ents))
	for i, e := range ents {
		out[i] = model.Entitlement{
			Code:  e.Code,
			Value: string(e.Value),
		}
	}
	return out
}

func quotaModels(quotas []Quota) []model.QuotaDefinition {
	out := make([]model.QuotaDefinition, len(quotas))
	for i, q := range quotas {
		out[i] = model.QuotaDefinition{
			Metric:     q.Metric,
			LimitValue: q.Limit,
			Unit:       q.Unit,
			Period:     q.Period,
		}
	}
	return out
}

// plansWithGroups 批量把 model.Plan 转成 Plan 并挂上可见组。
func (s *Service) plansWithGroups(ctx context.Context, rows []model.Plan) ([]Plan, error) {
	if len(rows) == 0 {
		return []Plan{}, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	groupMap, err := s.repo.VisibleGroups(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Plan, 0, len(rows))
	for _, r := range rows {
		groups := groupMap[r.ID]
		if groups == nil {
			groups = []uuid.UUID{}
		}
		p, err := planFromModel(r, groups)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// versionsWithChildren 批量读版本的权益与配额并转成 Version。
func (s *Service) versionsWithChildren(ctx context.Context, vrows []model.PlanVersion) ([]Version, error) {
	if len(vrows) == 0 {
		return []Version{}, nil
	}
	ids := make([]uuid.UUID, len(vrows))
	for i, v := range vrows {
		ids[i] = v.ID
	}
	entMap, err := s.repo.Entitlements(ctx, ids)
	if err != nil {
		return nil, err
	}
	quotaMap, err := s.repo.Quotas(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(vrows))
	for _, v := range vrows {
		ents := entMap[v.ID]
		if ents == nil {
			ents = []model.Entitlement{}
		}
		quotas := quotaMap[v.ID]
		if quotas == nil {
			quotas = []model.QuotaDefinition{}
		}
		out = append(out, versionFromModel(v, ents, quotas))
	}
	return out, nil
}
