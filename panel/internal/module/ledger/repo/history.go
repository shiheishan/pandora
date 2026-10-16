package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// HistoryRow 是用户某个科目的一条分录及其交易信息。
type HistoryRow struct {
	Kind       string          `gorm:"column:kind"`
	Direction  string          `gorm:"column:direction"`
	Amount     decimal.Decimal `gorm:"column:amount"`
	Memo       string          `gorm:"column:memo"`
	OccurredAt time.Time       `gorm:"column:occurred_at"`
}

// History 读用户某类科目的最近 limit 条分录，按分录 created_at DESC, id DESC。没有科目时返回空切片（不是 nil）。
func (r *Repo) History(ctx context.Context, userID uuid.UUID, accountType string, limit int) ([]HistoryRow, error) {
	const sql = `
SELECT t.kind, e.direction, e.amount, t.memo, t.occurred_at
  FROM ledger_entries e
  JOIN ledger_transactions t ON t.id = e.transaction_id
 WHERE e.account_id = (SELECT id FROM ledger_accounts
                        WHERE account_type = ? AND owner_user_id = ? AND owner_ref = '')
 ORDER BY e.created_at DESC, e.id DESC
 LIMIT ?`
	var rows []HistoryRow
	if err := r.d.Conn(ctx).Raw(sql, accountType, userID, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		return []HistoryRow{}, nil
	}
	return rows, nil
}
