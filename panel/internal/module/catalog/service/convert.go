package service

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/google/uuid"
)

func planFromModel(m model.Plan, groups []uuid.UUID) Plan {
	if groups == nil {
		groups = []uuid.UUID{}
	}
	return Plan{
		ID:               m.ID,
		Code:             m.Code,
		Name:             m.Name,
		Description:      m.Description,
		Kind:             m.Kind,
		Status:           m.Status,
		CurrentVersionID: m.CurrentVersionID,
		Visibility:       m.Visibility,
		VisibleGroupIDs:  groups,
		AllowNewPurchase: m.AllowNewPurchase,
		AllowRenewal:     m.AllowRenewal,
		AllowUpgrade:     m.AllowUpgrade,
		Recommended:      m.Recommended,
		ResetPrice:       m.ResetPrice,
		ResetLimit:       m.ResetLimit,
		RowVersion:       m.RowVersion,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

func priceFromModel(m model.Price) Price {
	return Price{
		ID:              m.ID,
		PlanID:          m.PlanID,
		PlanKind:        m.PlanKind,
		UnitAmount:      m.UnitAmount,
		BillingInterval: m.BillingInterval,
		IntervalCount:   m.IntervalCount,
		ValidityDays:    m.ValidityDays,
		Status:          m.Status,
		RowVersion:      m.RowVersion,
		CreatedAt:       m.CreatedAt,
	}
}

// tierRank 给价格周期排序：月付在年付前，同一周期按 interval_count；一次性价排最后（同一套餐里不会混着出现）。
func tierRank(interval string, count int) int {
	switch interval {
	case IntervalMonth:
		return count
	case IntervalYear:
		return 12 * count
	}
	return 1 << 20
}

// sortPricesByTier 把价格按周期从短到长排，同档按 id。
func sortPricesByTier(ps []Price) {
	sort.SliceStable(ps, func(i, j int) bool {
		ri, rj := tierRank(ps[i].BillingInterval, ps[i].IntervalCount), tierRank(ps[j].BillingInterval, ps[j].IntervalCount)
		if ri != rj {
			return ri < rj
		}
		return ps[i].ID.String() < ps[j].ID.String()
	})
}

// trafficQuotas 按套餐类型生成版本的流量配额：订阅按周期（cycle），一次性是总量（total）；不限流量时没有配额行。
func trafficQuotas(kind string, trafficBytes *int64) []Quota {
	if trafficBytes == nil {
		return []Quota{}
	}
	period := PeriodCycle
	if kind == KindOneTime {
		period = PeriodTotal
	}
	b := *trafficBytes
	return []Quota{{Metric: MetricTraffic, Limit: &b, Unit: UnitBytes, Period: period}}
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
		ID:           m.ID,
		PlanID:       m.PlanID,
		Version:      m.Version,
		Status:       m.Status,
		FrozenAt:     m.FrozenAt,
		MaxDevices:   m.MaxDevices,
		ThrottleKbps: m.ThrottleKbps,
		RowVersion:   m.RowVersion,
		CreatedBy:    m.CreatedBy,
		CreatedAt:    m.CreatedAt,
		Entitlements: outEnts,
		Quotas:       outQuotas,
	}
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
		out = append(out, planFromModel(r, groups))
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
