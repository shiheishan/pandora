package service

// 换套餐剩余价值的输入（折算基数）：subscription 给出每份订阅本周期挂了订单的事件（按先后），本模块读这些订单的付费与
// 计费周期，流量取 cycle / total 的 traffic.bytes 配额（有上限的），cycle 行加上提前续费后还没开始的几期。
// 报价与后台开单预览一次要折算多份，loadBases 按批读：本周期订单 1 次、订单与订单行各 1 次，配额来自订阅摘要。

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// paidKinds 是参与剩余价值基数的订单种类（买下订阅时长的那几种）。
var paidKinds = map[string]bool{KindNew: true, KindRenewal: true, KindUpgrade: true}

// basisSource 是一份要折算的订阅连同它的配额（批量时取订阅摘要里的，单份时现读）。
type basisSource struct {
	Sub    subsvc.Subscription
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

// basisOf 组一份订阅的折算输入；本周期付费单币种不一致回 errProrationMixedCurrency。
func basisOf(src basisSource, orderIDs []uuid.UUID, po periodOrders) (prorationBasis, error) {
	sub := src.Sub
	b := prorationBasis{PeriodStart: sub.PeriodStart, PeriodEnd: sub.PeriodEnd, Value: decimal.Zero}
	paidUntil := sub.PeriodStart
	for _, id := range orderIDs {
		o, ok := po.orders[id]
		if !ok || !paidKinds[o.Kind] || o.Status != StatusFulfilled {
			continue
		}
		it, ok := po.items[id]
		if !ok || it.SnapshotInterval == nil || it.SnapshotIntervalCount == nil {
			continue
		}
		cur := money.Currency(o.Currency)
		if b.Currency != "" && b.Currency != cur {
			return b, errProrationMixedCurrency
		}
		b.Currency = cur
		// 付费 = max(小计 − 折扣 − 免掉的零头, 0)：0 元单、全额券、人工赠送单为 0
		b.Value = b.Value.Add(decimal.Max(o.SubtotalAmount.Sub(o.DiscountAmount).Sub(o.WaivedAmount), decimal.Zero))
		// 0 元单的计费周期也接进付费总时长（产品决定，见 proration.go 开头）
		paidUntil = subsvc.AddInterval(paidUntil, *it.SnapshotInterval, *it.SnapshotIntervalCount)
	}
	b.PaidSpan = paidUntil.Sub(sub.PeriodStart)
	for _, q := range src.Quotas {
		if q.Metric != catalogsvc.MetricTraffic || q.Limit == nil ||
			(q.Period != catalogsvc.PeriodCycle && q.Period != catalogsvc.PeriodTotal) {
			continue
		}
		t := trafficAllowance{Cap: *q.Limit, Consumed: q.Consumed}
		if q.Period == catalogsvc.PeriodCycle && q.PeriodEnd != nil {
			t.Cap += pendingCycleAllowance(t.Cap, q.PeriodStart, *q.PeriodEnd, sub.PeriodEnd)
		}
		b.Traffic = append(b.Traffic, t)
	}
	return b, nil
}

// loadBases 批量读折算输入。本周期付费单币种不一致的那份不进 bases，记进 mixed。
func (s *Service) loadBases(ctx context.Context, srcs []basisSource) (bases map[uuid.UUID]prorationBasis, mixed map[uuid.UUID]bool, err error) {
	bases = make(map[uuid.UUID]prorationBasis, len(srcs))
	mixed = map[uuid.UUID]bool{}
	if len(srcs) == 0 {
		return bases, mixed, nil
	}
	subIDs := make([]uuid.UUID, 0, len(srcs))
	for _, src := range srcs {
		subIDs = append(subIDs, src.Sub.ID)
	}
	po, err := s.loadPeriodOrders(ctx, subIDs)
	if err != nil {
		return nil, nil, err
	}
	for _, src := range srcs {
		b, err := basisOf(src, po.ids[src.Sub.ID], po)
		if errors.Is(err, errProrationMixedCurrency) {
			mixed[src.Sub.ID] = true
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		bases[src.Sub.ID] = b
	}
	return bases, mixed, nil
}

// loadBasis 读一份订阅的折算输入（配额现读）；本周期付费单币种不一致回 errProrationMixedCurrency。
func (s *Service) loadBasis(ctx context.Context, sub subsvc.Subscription) (prorationBasis, error) {
	quotas, err := s.subs.Quotas(ctx, sub.ID)
	if err != nil {
		return prorationBasis{}, err
	}
	bases, mixed, err := s.loadBases(ctx, []basisSource{{Sub: sub, Quotas: quotas}})
	if err != nil {
		return prorationBasis{}, err
	}
	if mixed[sub.ID] {
		return prorationBasis{}, errProrationMixedCurrency
	}
	return bases[sub.ID], nil
}

// sourceCredit 是原订阅在 asOf 时刻的剩余价值与明细；本期付费单的币种与新价不同就不能换。
func sourceCredit(b prorationBasis, asOf time.Time, cur money.Currency) (decimal.Decimal, *CreditDetail, error) {
	if b.Currency != "" && b.Currency != cur {
		return decimal.Zero, nil, ErrPlanChangeCurrency
	}
	credit, d := prorationCredit(b, asOf)
	return credit, &d, nil
}
