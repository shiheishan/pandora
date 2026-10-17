package service

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// dayMoney 是某天的实际借贷与调整。
type dayMoney struct {
	credit, debit, adj decimal.Decimal
}

// dayKey 拼 dayMap 的键（YYYY-MM-DD）。
func dayKey(day string) string {
	return day
}

// dayMap 是 YYYY-MM-DD → 当天借贷与调整。
type dayMap map[string]*dayMoney

func (m dayMap) ensure(key string) *dayMoney {
	if m[key] == nil {
		m[key] = &dayMoney{
			credit: decimal.Zero,
			debit:  decimal.Zero,
			adj:    decimal.Zero,
		}
	}
	return m[key]
}

// revenueByDay 取 [from, to) 内每天的实际借贷与调整，key 为 YYYY-MM-DD。
func (s *Service) revenueByDay(ctx context.Context, from, to time.Time) (dayMap, error) {
	ledgerRows, err := s.deps.Ledger.RevenueByDay(ctx, from, to)
	if err != nil {
		return nil, err
	}
	adjRows, err := s.repo.AdjustmentSumsByDay(ctx, dayString(from), dayString(to))
	if err != nil {
		return nil, err
	}

	out := dayMap{}
	for _, r := range ledgerRows {
		k := dayKey(r.Day)
		dm := out.ensure(k)
		dm.credit = dm.credit.Add(r.Credit)
		dm.debit = dm.debit.Add(r.Debit)
	}
	for _, r := range adjRows {
		k := dayKey(r.Day)
		dm := out.ensure(k)
		dm.adj = dm.adj.Add(r.Amount)
	}
	return out, nil
}

func dayActual(dm *dayMoney) decimal.Decimal {
	if dm == nil {
		return decimal.Zero
	}
	return dm.credit.Sub(dm.debit)
}

func dayDisplayed(dm *dayMoney) decimal.Decimal {
	if dm == nil {
		return decimal.Zero
	}
	return dm.credit.Sub(dm.debit).Add(dm.adj)
}

func sumActualByDay(m dayMap, from, to time.Time) decimal.Decimal {
	sum := decimal.Zero
	for d := from; !d.After(to); d = addDays(d, 1) {
		sum = sum.Add(dayActual(m[dayKey(dayString(d))]))
	}
	return sum
}

func sumAdjByDay(m dayMap, from, to time.Time) decimal.Decimal {
	sum := decimal.Zero
	for d := from; !d.After(to); d = addDays(d, 1) {
		dm := m[dayKey(dayString(d))]
		if dm != nil {
			sum = sum.Add(dm.adj)
		}
	}
	return sum
}

func ledgerActualTotal(t ledgersvc.RevenueTotal) decimal.Decimal {
	return t.Credit.Sub(t.Debit)
}

func sumDisplayedInRange(m dayMap, from, to time.Time) decimal.Decimal {
	sum := decimal.Zero
	for d := from; d.Before(to); d = addDays(d, 1) {
		sum = sum.Add(dayDisplayed(m[dayKey(dayString(d))]))
	}
	return sum
}

// RevenueTimeseries 给最近 days 天（含今天，站点时区）按日补齐的营收，与紧邻前一个等长区间的展示净额合计。
func (s *Service) RevenueTimeseries(ctx context.Context, days int) (RevenueSeries, error) {
	if days == 0 {
		days = 30
	}
	if days != 7 && days != 30 && days != 90 {
		return RevenueSeries{}, errs.Invalid(map[string]string{"days": "只支持 7、30、90"})
	}

	loc, err := s.location(ctx)
	if err != nil {
		return RevenueSeries{}, err
	}
	now := s.now()
	today := midnight(now, loc)
	from := addDays(today, -(days - 1))
	prevFrom := addDays(from, -days)
	end := addDays(today, 1)

	m, err := s.revenueByDay(ctx, prevFrom, end)
	if err != nil {
		return RevenueSeries{}, err
	}

	points := make([]RevenuePoint, 0, days)
	for d := from; !d.After(today); d = addDays(d, 1) {
		dm := m[dayKey(dayString(d))]
		credit, debit, adj := decimal.Zero, decimal.Zero, decimal.Zero
		if dm != nil {
			credit, debit, adj = dm.credit, dm.debit, dm.adj
		}
		points = append(points, RevenuePoint{
			Date:         dayString(d),
			ActualCredit: credit,
			ActualDebit:  debit,
			Adjustment:   adj,
			DisplayedNet: credit.Sub(debit).Add(adj),
		})
	}

	return RevenueSeries{
		Days:          days,
		Points:        points,
		PreviousTotal: sumDisplayedInRange(m, prevFrom, from),
	}, nil
}
