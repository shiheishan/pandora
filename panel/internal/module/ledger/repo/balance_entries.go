package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BalanceEntryRow 是用户某个科目的一条分录、所属交易，以及从开户到这条（含）为止的贷方、借方合计。
type BalanceEntryRow struct {
	ID             uuid.UUID       `gorm:"column:id"`
	Kind           string          `gorm:"column:kind"`
	Direction      string          `gorm:"column:direction"`
	Amount         decimal.Decimal `gorm:"column:amount"`
	CreditsThrough decimal.Decimal `gorm:"column:credits_through"`
	DebitsThrough  decimal.Decimal `gorm:"column:debits_through"`
	Memo           string          `gorm:"column:memo"`
	SourceType     *string         `gorm:"column:source_type"`
	SourceID       *uuid.UUID      `gorm:"column:source_id"`
	OccurredAt     time.Time       `gorm:"column:occurred_at"`
	Total          int64           `gorm:"column:total"`
}

// BalanceEntries 读用户某类科目的一页分录（新的在前，与 History 同序：分录 created_at DESC, id DESC），并返回该科目分录总数。
// 变动后余额 = 开户以来本科目贷方合计 − 借方合计（user_balance 是贷方科目），一条语句用窗口函数拿到，与这一页同一快照。
// 每次扫这个用户该科目的全部分录（按 ledger_entries_account_idx），后台只读、单人量级，可以接受。
func (r *Repo) BalanceEntries(ctx context.Context, userID uuid.UUID, accountType string, limit, offset int) ([]BalanceEntryRow, int64, error) {
	const sql = `
WITH running AS (
  SELECT e.id, e.created_at, t.kind, e.direction, e.amount, t.memo, t.source_type, t.source_id, t.occurred_at,
         coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit') OVER w, 0) AS credits_through,
         coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit') OVER w, 0) AS debits_through,
         count(*) OVER () AS total
    FROM ledger_entries e
    JOIN ledger_transactions t ON t.id = e.transaction_id
   WHERE e.account_id = (SELECT id FROM ledger_accounts
                          WHERE account_type = ? AND owner_user_id = ? AND owner_ref = '')
  WINDOW w AS (ORDER BY e.created_at, e.id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
)
SELECT id, kind, direction, amount, credits_through, debits_through, memo, source_type, source_id, occurred_at, total
  FROM running
 ORDER BY created_at DESC, id DESC
 LIMIT ? OFFSET ?`

	var rows []BalanceEntryRow
	if err := r.d.Conn(ctx).Raw(sql, accountType, userID, limit, offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	if len(rows) > 0 {
		return rows, rows[0].Total, nil
	}

	if offset > 0 {
		const countSQL = `
SELECT count(*)
  FROM ledger_entries
 WHERE account_id = (SELECT id FROM ledger_accounts
                      WHERE account_type = ? AND owner_user_id = ? AND owner_ref = '')`
		var total int64
		if err := r.d.Conn(ctx).Raw(countSQL, accountType, userID).Scan(&total).Error; err != nil {
			return nil, 0, err
		}
		return []BalanceEntryRow{}, total, nil
	}

	return []BalanceEntryRow{}, 0, nil
}
