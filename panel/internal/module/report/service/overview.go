package service

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
)

func revenueRow(
	byDay dayMap,
	today time.Time,
	ledgerTotal ledgersvc.RevenueTotal,
	adjTotal decimal.Decimal,
) RevenueRow {
	yesterday := addDays(today, -1)
	day7Start := addDays(today, -6)
	day30Start := addDays(today, -29)

	actualToday := dayActual(byDay[dayKey(dayString(today))])
	actualYesterday := dayActual(byDay[dayKey(dayString(yesterday))])
	actual7 := sumActualByDay(byDay, day7Start, today)
	actual30 := sumActualByDay(byDay, day30Start, today)
	actualTotal := ledgerActualTotal(ledgerTotal)

	adjToday := sumAdjByDay(byDay, today, today)
	adjYesterday := sumAdjByDay(byDay, yesterday, yesterday)
	adj7 := sumAdjByDay(byDay, day7Start, today)
	adj30 := sumAdjByDay(byDay, day30Start, today)

	return RevenueRow{
		Today:      actualToday.Add(adjToday),
		Yesterday:  actualYesterday.Add(adjYesterday),
		Last7Days:  actual7.Add(adj7),
		Last30Days: actual30.Add(adj30),
		Total:      actualTotal.Add(adjTotal),

		ActualToday:     actualToday,
		ActualYesterday: actualYesterday,
		Actual7Days:     actual7,
		Actual30Days:    actual30,
		ActualTotal:     actualTotal,

		AdjustmentToday:  adjToday,
		Adjustment7Days:  adj7,
		Adjustment30Days: adj30,
		AdjustmentTotal:  adjTotal,
	}
}

// Overview 给看板概览：用户、订阅、节点、订单计数，全站营收（实际、调整、展示），账本漂移科目数。
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	return cached(ctx, s, "report:v1:overview", func(ctx context.Context) (Overview, error) {
		return s.loadOverview(ctx)
	})
}

func (s *Service) loadOverview(ctx context.Context) (Overview, error) {
	loc, err := s.location(ctx)
	if err != nil {
		return Overview{}, err
	}
	now := s.now()
	today := midnight(now, loc)
	week := 7 * 24 * time.Hour

	users, err := s.deps.Users.ReportUserCounts(ctx, today, now.Add(-week))
	if err != nil {
		return Overview{}, err
	}
	subs, err := s.deps.Subscriptions.ReportSubscriptionCounts(ctx, now, now.Add(week), now.Add(-week))
	if err != nil {
		return Overview{}, err
	}
	nodes, err := s.deps.Nodes.ReportNodeCounts(ctx, now)
	if err != nil {
		return Overview{}, err
	}
	orders, err := s.deps.Orders.ReportOrderCounts(ctx, today, now.Add(-StaleOrderAfter))
	if err != nil {
		return Overview{}, err
	}
	drift, err := s.deps.Ledger.DriftAccounts(ctx)
	if err != nil {
		return Overview{}, err
	}

	byDay, err := s.revenueByDay(ctx, addDays(today, -29), addDays(today, 1))
	if err != nil {
		return Overview{}, err
	}
	ledgerTotal, err := s.deps.Ledger.RevenueTotals(ctx)
	if err != nil {
		return Overview{}, err
	}
	adjTotal, err := s.repo.AdjustmentTotals(ctx)
	if err != nil {
		return Overview{}, err
	}

	revenue := revenueRow(byDay, today, ledgerTotal, adjTotal)

	return Overview{
		Users: UserCounts{
			Total:     users.Total,
			Active:    users.Active,
			Today:     users.CreatedSince,
			Last7Days: users.CreatedRecent,
		},
		Subscriptions: SubscriptionCounts{
			Active:        subs.Live,
			Expiring7Days: subs.Expiring,
			Expired:       subs.NotLive,
			New7Days:      subs.NewLive,
		},
		Nodes: NodeCounts{
			Total:  nodes.Total,
			Online: nodes.Online,
		},
		Revenue: revenue,
		Orders: OrderCounts{
			PaidToday:   orders.PaidSince,
			Pending:     orders.Pending,
			FailedToday: orders.ClosedSince,
		},
		LedgerDriftAccounts: drift,
	}, nil
}
