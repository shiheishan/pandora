package repo

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// RevenueDayRow 是 RevenueByDay 的一行。
type RevenueDayRow struct {
	Currency string          `gorm:"column:currency"`
	Day      string          `gorm:"column:day"`
	Credit   decimal.Decimal `gorm:"column:credit"`
	Debit    decimal.Decimal `gorm:"column:debit"`
}

// RevenueTotalRow 是 RevenueTotals 的一行。
type RevenueTotalRow struct {
	Currency string          `gorm:"column:currency"`
	Credit   decimal.Decimal `gorm:"column:credit"`
	Debit    decimal.Decimal `gorm:"column:debit"`
}

// RevenueByDay 按（币种, 站点时区的日）汇总 platform_revenue 在 [from, to) 内的分录。
func (r *Repo) RevenueByDay(ctx context.Context, tz string, from, to time.Time) ([]RevenueDayRow, error) {
	var out []RevenueDayRow
	err := r.d.Conn(ctx).Raw(`
		SELECT e.currency,
		       to_char((t.occurred_at AT TIME ZONE ?)::date, 'YYYY-MM-DD') AS day,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0) AS credit,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0) AS debit
		  FROM ledger_entries e
		  JOIN ledger_transactions t ON t.id = e.transaction_id
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE a.account_type = 'platform_revenue' AND t.occurred_at >= ? AND t.occurred_at < ?
		 GROUP BY 1, 2
		 ORDER BY 1, 2`, tz, from, to).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// RevenueTotals 按币种汇总 platform_revenue 全部时间的借贷。
func (r *Repo) RevenueTotals(ctx context.Context) ([]RevenueTotalRow, error) {
	var out []RevenueTotalRow
	err := r.d.Conn(ctx).Raw(`
		SELECT e.currency,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0) AS credit,
		       coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0) AS debit
		  FROM ledger_entries e
		  JOIN ledger_accounts a ON a.id = e.account_id
		 WHERE a.account_type = 'platform_revenue'
		 GROUP BY e.currency
		 ORDER BY e.currency`).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// DriftAccounts 统计余额缓存与分录合计不一致的科目个数。
func (r *Repo) DriftAccounts(ctx context.Context, debitNormalTypes []string) (int64, error) {
	if len(debitNormalTypes) == 0 {
		debitNormalTypes = []string{""}
	}
	var n int64
	err := r.d.Conn(ctx).Raw(`
		SELECT count(*)
		  FROM ledger_accounts a
		  LEFT JOIN (
		        SELECT account_id,
		               coalesce(sum(amount) FILTER (WHERE direction = 'debit'), 0) AS d,
		               coalesce(sum(amount) FILTER (WHERE direction = 'credit'), 0) AS c
		          FROM ledger_entries GROUP BY account_id) s ON s.account_id = a.id
		 WHERE a.balance <> CASE WHEN a.account_type IN ? THEN coalesce(s.d, 0) - coalesce(s.c, 0)
		                         ELSE coalesce(s.c, 0) - coalesce(s.d, 0) END`, debitNormalTypes).Scan(&n).Error
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
