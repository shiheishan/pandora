package repo

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// RevenueDayRow 是 RevenueByDay 的一行。
type RevenueDayRow struct {
	Day    string          `gorm:"column:day"`
	Credit decimal.Decimal `gorm:"column:credit"`
	Debit  decimal.Decimal `gorm:"column:debit"`
}

// RevenueTotalRow 是 RevenueTotals 的汇总。
type RevenueTotalRow struct {
	Credit decimal.Decimal `gorm:"column:credit"`
	Debit  decimal.Decimal `gorm:"column:debit"`
}

// RevenueByDay 按站点时区的日汇总 platform_revenue 在 [from, to) 内的分录。
func (r *Repo) RevenueByDay(ctx context.Context, tz string, from, to time.Time) ([]RevenueDayRow, error) {
	var out []RevenueDayRow
	err := r.d.Conn(ctx).Raw(`
		SELECT to_char((t.occurred_at AT TIME ZONE ?)::date, 'YYYY-MM-DD') AS day,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0) AS credit,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0) AS debit
		  FROM ledger_entries e
		  JOIN ledger_transactions t ON t.id = e.transaction_id
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE a.account_type = 'platform_revenue' AND t.occurred_at >= ? AND t.occurred_at < ?
		 GROUP BY 1
		 ORDER BY 1`, tz, from, to).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// RevenueTotals 汇总 platform_revenue 全部时间的借贷（无分录时 credit、debit 为 0）。
func (r *Repo) RevenueTotals(ctx context.Context) (RevenueTotalRow, error) {
	var out RevenueTotalRow
	err := r.d.Conn(ctx).Raw(`
		SELECT coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0) AS credit,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0) AS debit
		  FROM ledger_entries e
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE a.account_type = 'platform_revenue'`).Scan(&out).Error
	if err != nil {
		return RevenueTotalRow{}, mapErr(err)
	}
	return out, nil
}
