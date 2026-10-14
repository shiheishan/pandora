package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// RevenueDay 是平台收入科目（platform_revenue）某币种在站点时区某一天的分录合计。
type RevenueDay struct {
	Currency money.Currency
	Day      string          // YYYY-MM-DD（站点时区）
	Credit   decimal.Decimal // 贷方合计（收入增加：订单结算、佣金作废冲回）
	Debit    decimal.Decimal // 借方合计（收入减少：佣金计提、换套餐退回）
}

// RevenueTotal 是平台收入科目某币种全部时间的借贷合计。
type RevenueTotal struct {
	Currency money.Currency
	Credit   decimal.Decimal
	Debit    decimal.Decimal
}

// RevenueByDay 按（币种, 站点时区的日）汇总平台收入科目在 [from, to) 内的分录；没有分录的日子不在结果里。按币种、日子升序。
func (s *Service) RevenueByDay(ctx context.Context, from, to time.Time) ([]RevenueDay, error) {
	tz := from.Location().String()
	rows, err := s.repo.RevenueByDay(ctx, tz, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]RevenueDay, len(rows))
	for i, r := range rows {
		out[i] = RevenueDay{
			Currency: money.Currency(r.Currency),
			Day:      r.Day,
			Credit:   r.Credit,
			Debit:    r.Debit,
		}
	}
	return out, nil
}

// RevenueTotals 按币种汇总平台收入科目全部时间的借贷；没有分录的币种不在结果里。按币种升序。
func (s *Service) RevenueTotals(ctx context.Context) ([]RevenueTotal, error) {
	rows, err := s.repo.RevenueTotals(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RevenueTotal, len(rows))
	for i, r := range rows {
		out[i] = RevenueTotal{
			Currency: money.Currency(r.Currency),
			Credit:   r.Credit,
			Debit:    r.Debit,
		}
	}
	return out, nil
}

// DriftAccounts 数余额缓存与分录合计对不上的科目个数（应为 0；非 0 说明账本缓存坏了，看板标红）。
func (s *Service) DriftAccounts(ctx context.Context) (int64, error) {
	var debitNormal []string
	for _, t := range []AccountType{
		UserBalance, UserBalanceHold, UserCommissionPending, UserCommissionAvailable,
		ChannelCash, PlatformRevenue, PlatformFeeExpense, PlatformPromoExpense,
		Suspense, LatePaymentSuspense,
	} {
		if t.DebitNormal() {
			debitNormal = append(debitNormal, string(t))
		}
	}
	return s.repo.DriftAccounts(ctx, debitNormal)
}
