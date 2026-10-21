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

// BalanceEntries 读用户某类科目的一页分录（新的在前，与 History 同序：分录 id DESC），并返回该科目分录总数。
//
// 变动后余额 = 开户以来本科目贷方合计 − 借方合计（user_balance 是贷方科目），一条语句用窗口函数拿到，与这一页同一快照。
// 窗口与分页都按分录 id：id 是插入时生成的 uuidv7，插入在拿到科目行锁之后（service.post），id 顺序就是真实的记账顺序，
// 每行的变动后余额都是科目真实出现过的值、不会为负（科目余额有非负约束）；按 created_at（事务开始时刻）排，
// 早开事务、晚拿锁的那笔会排到前面，算出没出现过、甚至为负的中间值。
// 窗口、计数与分页只在 ledger_entries 上算，只给当前页（≤ 上限条）连交易表。
// 每次要把这个用户该科目的全部分录排一遍（过滤走 ledger_entries_account_idx），后台只读、单人量级，可以接受。
func (r *Repo) BalanceEntries(ctx context.Context, userID uuid.UUID, accountType string, limit, offset int) ([]BalanceEntryRow, int64, error) {
	const sql = `
WITH page AS (
  SELECT id, transaction_id, direction, amount, credits_through, debits_through, total
    FROM (
      SELECT e.id, e.transaction_id, e.direction, e.amount,
             coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit') OVER w, 0) AS credits_through,
             coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit') OVER w, 0) AS debits_through,
             count(*) OVER () AS total
        FROM ledger_entries e
       WHERE e.account_id = (SELECT id FROM ledger_accounts
                              WHERE account_type = ? AND owner_user_id = ? AND owner_ref = '')
      WINDOW w AS (ORDER BY e.id ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
    ) running
   ORDER BY id DESC
   LIMIT ? OFFSET ?
)
SELECT p.id, t.kind, p.direction, p.amount, p.credits_through, p.debits_through,
       t.memo, t.source_type, t.source_id, t.occurred_at, p.total
  FROM page p
  JOIN ledger_transactions t ON t.id = p.transaction_id
 ORDER BY p.id DESC`

	var rows []BalanceEntryRow
	if err := r.d.Conn(ctx).Raw(sql, accountType, userID, limit, offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	if len(rows) > 0 {
		return rows, rows[0].Total, nil
	}

	// 翻过头了（窗口里拿不到总数）才另查一次总数
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
