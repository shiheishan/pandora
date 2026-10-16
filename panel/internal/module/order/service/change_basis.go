package service

// 换套餐剩余价值的输入（折算基数）：subscription 给出每份订阅本周期挂了订单的事件（按先后），本模块读这些订单的付费与
// 计费周期；订阅套餐按付费单接出 PaidSpan，一次性套餐按周期全长与 total 流量配额。
// 报价与后台开单预览一次要折算多份，loadBases 按批读：本周期订单 1 次、订单与订单行各 1 次，套餐类型 1 次。

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// paidKinds 是参与剩余价值基数的订单种类（买下订阅时长或叠加的那几种）。
var paidKinds = map[string]bool{
	KindNew: true, KindRenewal: true, KindUpgrade: true, KindStack: true,
}

// basisSource 是一份要折算的订阅：Kind 是它套餐的类型（catalogsvc.KindSubscription / KindOneTime），Quotas 是它的配额。
type basisSource struct {
	Sub    subsvc.Subscription
	Kind   string
	Quotas []subsvc.Quota
}

// periodOrders 是一批订阅本周期的付费来源订单与每张单的第一条套餐行（按创建先后）。
type periodOrders struct {
	ids    map[uuid.UUID][]uuid.UUID
	orders map[uuid.UUID]model.Order
	items  map[uuid.UUID]model.Item
}

// loadPeriodOrders 一次读出 subIDs 本周期的订单与订单行。
func (s *Service) loadPeriodOrders(ctx context.Context, subIDs []uuid.UUID) (periodOrders, error) {
	po := periodOrders{orders: map[uuid.UUID]model.Order{}, items: map[uuid.UUID]model.Item{}}
	ids, err := s.subs.PeriodOrderIDsBySubscriptions(ctx, subIDs)
	if err != nil {
		return po, err
	}
	po.ids = ids
	var all []uuid.UUID
	for _, id := range subIDs {
		all = append(all, ids[id]...)
	}
	orders, err := s.repo.OrdersByIDs(ctx, all)
	if err != nil {
		return po, err
	}
	for _, o := range orders {
		po.orders[o.ID] = o
	}
	items, err := s.repo.ItemsByOrders(ctx, all)
	if err != nil {
		return po, err
	}
	for _, it := range items {
		if it.PlanID == nil {
			continue
		}
		if _, seen := po.items[it.OrderID]; !seen {
			po.items[it.OrderID] = it
		}
	}
	return po, nil
}

// basisOf 组一份订阅的折算输入。
func basisOf(src basisSource, orderIDs []uuid.UUID, po periodOrders) prorationBasis {
	sub := src.Sub
	oneTime := src.Kind == catalogsvc.KindOneTime
	b := prorationBasis{
		PeriodStart: sub.PeriodStart,
		PeriodEnd:   sub.PeriodEnd,
		Value:       decimal.Zero,
		OneTime:     oneTime,
	}
	paidUntil := sub.PeriodStart
	for _, id := range orderIDs {
		o, ok := po.orders[id]
		if !ok || !paidKinds[o.Kind] || o.Status != StatusFulfilled {
			continue
		}
		it, ok := po.items[id]
		if !ok {
			continue
		}
		b.Value = b.Value.Add(decimal.Max(o.SubtotalAmount.Sub(o.DiscountAmount).Sub(o.WaivedAmount), decimal.Zero))
		if !oneTime && it.SnapshotInterval != nil && it.SnapshotIntervalCount != nil {
			paidUntil = subsvc.AddInterval(paidUntil, *it.SnapshotInterval, *it.SnapshotIntervalCount)
		}
	}
	if oneTime {
		b.PaidSpan = sub.PeriodEnd.Sub(sub.PeriodStart)
		b.Permanent = subsvc.IsPermanent(sub.PeriodEnd)
		for _, q := range src.Quotas {
			if q.Metric != catalogsvc.MetricTraffic || q.Period != catalogsvc.PeriodTotal || q.Limit == nil {
				continue
			}
			b.Traffic = append(b.Traffic, trafficAllowance{Cap: *q.Limit, Consumed: q.Consumed})
		}
		return b
	}
	b.PaidSpan = paidUntil.Sub(sub.PeriodStart)
	return b
}

// loadBases 批量读折算输入（订阅 id → 输入）。
func (s *Service) loadBases(ctx context.Context, srcs []basisSource) (map[uuid.UUID]prorationBasis, error) {
	bases := make(map[uuid.UUID]prorationBasis, len(srcs))
	if len(srcs) == 0 {
		return bases, nil
	}
	subIDs := make([]uuid.UUID, 0, len(srcs))
	for _, src := range srcs {
		subIDs = append(subIDs, src.Sub.ID)
	}
	po, err := s.loadPeriodOrders(ctx, subIDs)
	if err != nil {
		return nil, err
	}
	for _, src := range srcs {
		bases[src.Sub.ID] = basisOf(src, po.ids[src.Sub.ID], po)
	}
	return bases, nil
}

// loadBasis 读一份订阅的折算输入：配额现读（s.subs.Quotas），套餐类型现读（s.catalog.PlansByIDs）。
func (s *Service) loadBasis(ctx context.Context, sub subsvc.Subscription) (prorationBasis, error) {
	quotas, err := s.subs.Quotas(ctx, sub.ID)
	if err != nil {
		return prorationBasis{}, err
	}
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{sub.PlanID})
	if err != nil {
		return prorationBasis{}, err
	}
	plan, ok := plans[sub.PlanID]
	if !ok {
		return prorationBasis{}, catalogsvc.ErrNotFound
	}
	bases, err := s.loadBases(ctx, []basisSource{{Sub: sub, Kind: plan.Kind, Quotas: quotas}})
	if err != nil {
		return prorationBasis{}, err
	}
	return bases[sub.ID], nil
}

// sourceCredit 是原订阅在 asOf 时刻的剩余价值与明细。
func sourceCredit(b prorationBasis, asOf time.Time) (decimal.Decimal, *CreditDetail) {
	credit, d := prorationCredit(b, asOf)
	return credit, &d
}
