package service

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/report/repo"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

func adjustmentTotalFromRows(rows []repo.AdjustmentTotalRow, cur money.Currency) decimal.Decimal {
	for _, r := range rows {
		if r.Currency == string(cur) {
			return r.Amount
		}
	}
	return decimal.Zero
}

func revenueRowForCurrency(
	cur money.Currency,
	byDay dayMap,
	today time.Time,
	ledgerTotals []ledgersvc.RevenueTotal,
	adjTotals []repo.AdjustmentTotalRow,
) RevenueRow {
	yesterday := addDays(today, -1)
	day7Start := addDays(today, -6)
	day30Start := addDays(today, -29)

	actualToday := dayActual(byDay[dayKey(cur, dayString(today))])
	actualYesterday := dayActual(byDay[dayKey(cur, dayString(yesterday))])
	actual7 := sumActualByDay(byDay, cur, day7Start, today)
	actual30 := sumActualByDay(byDay, cur, day30Start, today)
	actualTotal := ledgerActualTotal(ledgerTotals, cur)

	adjToday := sumAdjByDay(byDay, cur, today, today)
	adjYesterday := sumAdjByDay(byDay, cur, yesterday, yesterday)
	adj7 := sumAdjByDay(byDay, cur, day7Start, today)
	adj30 := sumAdjByDay(byDay, cur, day30Start, today)
	adjTotal := adjustmentTotalFromRows(adjTotals, cur)

	return RevenueRow{
		Currency: cur,

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

// Overview 给看板概览：用户、订阅、节点、订单计数，两币种的营收（实际、调整、展示），账本漂移科目数。
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
	ledgerTotals, err := s.deps.Ledger.RevenueTotals(ctx)
	if err != nil {
		return Overview{}, err
	}
	adjTotals, err := s.repo.AdjustmentTotals(ctx)
	if err != nil {
		return Overview{}, err
	}

	revenue := []RevenueRow{
		revenueRowForCurrency(money.CNY, byDay, today, ledgerTotals, adjTotals),
		revenueRowForCurrency(money.USD, byDay, today, ledgerTotals, adjTotals),
	}

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
