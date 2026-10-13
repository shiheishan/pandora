package service

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// dayMoney 是某币种某天的实际借贷与调整。
type dayMoney struct {
	credit, debit, adj decimal.Decimal
}

// dayKey 拼 dayMap 的键。
func dayKey(currency money.Currency, day string) string {
	return string(currency) + "|" + day
}

// dayMap 是 币种|YYYY-MM-DD → 当天借贷与调整。
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

// revenueByDay 取 [from, to) 内每天每币种的实际借贷与调整，key 为 币种|YYYY-MM-DD。
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
		k := dayKey(r.Currency, r.Day)
		dm := out.ensure(k)
		dm.credit = dm.credit.Add(r.Credit)
		dm.debit = dm.debit.Add(r.Debit)
	}
	for _, r := range adjRows {
		cur, err := money.ParseCurrency(r.Currency)
		if err != nil {
			continue
		}
		k := dayKey(cur, r.Day)
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

func sumActualByDay(m dayMap, cur money.Currency, from, to time.Time) decimal.Decimal {
	sum := decimal.Zero
	for d := from; !d.After(to); d = addDays(d, 1) {
		sum = sum.Add(dayActual(m[dayKey(cur, dayString(d))]))
	}
	return sum
}

func sumAdjByDay(m dayMap, cur money.Currency, from, to time.Time) decimal.Decimal {
	sum := decimal.Zero
	for d := from; !d.After(to); d = addDays(d, 1) {
		dm := m[dayKey(cur, dayString(d))]
		if dm != nil {
			sum = sum.Add(dm.adj)
		}
	}
	return sum
}

func ledgerActualTotal(totals []ledgersvc.RevenueTotal, cur money.Currency) decimal.Decimal {
	for _, r := range totals {
		if r.Currency == cur {
			return r.Credit.Sub(r.Debit)
		}
	}
	return decimal.Zero
}

func sumDisplayedInRange(m dayMap, cur money.Currency, from, to time.Time) decimal.Decimal {
	sum := decimal.Zero
	for d := from; d.Before(to); d = addDays(d, 1) {
		sum = sum.Add(dayDisplayed(m[dayKey(cur, dayString(d))]))
	}
	return sum
}

// RevenueTimeseries 给最近 days 天（含今天，站点时区）某币种按日补齐的营收，与紧邻前一个等长区间的展示净额合计。
func (s *Service) RevenueTimeseries(ctx context.Context, currency string, days int) (RevenueSeries, error) {
	fields := map[string]string{}
	cur, err := money.ParseCurrency(currency)
	if err != nil {
		fields["currency"] = "仅支持 CNY 或 USD"
	}
	if days == 0 {
		days = 30
	}
	if days != 7 && days != 30 && days != 90 {
		fields["days"] = "只支持 7、30、90"
	}
	if len(fields) > 0 {
		return RevenueSeries{}, errs.Invalid(fields)
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
		dm := m[dayKey(cur, dayString(d))]
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
		Currency:      cur,
		Days:          days,
		Points:        points,
		PreviousTotal: sumDisplayedInRange(m, cur, prevFrom, from),
	}, nil
}
