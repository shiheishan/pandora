// [INPUT]: 依赖 ledger.go 的 Balance 与 AccountUserCommissionAvailable，依赖 reservations.go 的 prepareAndLockLedgerAccounts，读 withdrawals 的在途金额
// [OUTPUT]: 对包内提供 lockUserCommissionAccounts、withdrawableCommission、commissionAvailableSnapshot
// [POS]: billing 佣金「可用」的唯一口径（D-F-1），被 commission.go 的 RequestWithdrawal / CommissionSummary 与 commission_transfer.go 的 TransferCommissionToBalance 共用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 可用佣金 = 账本科目 user_commission_available 的余额 − 尚未过账的在途提现。
//
// 为什么只认账本：此前摘要与提现按 commission_entries 汇总，转余额按账本余额，
// 两边互相看不见 —— 转进余额的钱还能再申请提现，提现在途的钱还能再转进余额，
// 最后要到打款那一步才被账本检查拦下。账本是钱此刻在哪的唯一记录；佣金条目
// 记的是「赚到过多少」，不是「还剩多少」。
//
// 在途只减 requested / reviewing / approved：打款那一刻（processing 及之后）
// 已经借记了这个科目，再减一次就是重复扣。
//
// 锁：提现申请与转余额都先锁住该用户的可提现佣金科目行，再按本口径校验。
// 后到的一方一定看得见前一方已经写下的分录或提现申请。

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const unpostedWithdrawalsSQL = `
	SELECT COALESCE(sum(amount), 0)::bigint
	  FROM withdrawals
	 WHERE tenant_id = $1 AND user_id = $2::uuid AND currency = $3
	   AND status IN ('requested','reviewing','approved')`

// lockUserCommissionAccounts 锁住用户名下全部币种的可提现佣金科目，
// 返回 币种 → 科目 ID 与按字母序排列的币种列表。没有科目时两者都为空。
func lockUserCommissionAccounts(ctx context.Context, tx pgx.Tx,
	tenantID, userID string) (map[string]string, []string, error) {

	rows, err := tx.Query(ctx, `
		SELECT DISTINCT currency::text
		  FROM ledger_accounts
		 WHERE tenant_id = $1 AND account_type = $2 AND owner_user_id = $3::uuid
		 ORDER BY 1`, tenantID, AccountUserCommissionAvailable, userID)
	if err != nil {
		return nil, nil, err
	}
	currencies := []string{}
	for rows.Next() {
		var currency string
		if err := rows.Scan(&currency); err != nil {
			rows.Close()
			return nil, nil, err
		}
		currencies = append(currencies, currency)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(currencies) == 0 {
		return map[string]string{}, currencies, nil
	}

	specs := make([]ledgerAccountSpec, 0, len(currencies))
	for _, currency := range currencies {
		specs = append(specs, ledgerAccountSpec{Key: currency,
			AccountType: AccountUserCommissionAvailable, Currency: currency, UserID: &userID})
	}
	accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, specs)
	if err != nil {
		return nil, nil, err
	}
	return accounts, currencies, nil
}

// withdrawableCommission 按唯一口径计算某币种的可用佣金。
// accountID 为空表示该用户还没有这个科目（余额视为 0）；
// 需要据此放行动账的调用方必须先在本事务里锁住该科目。
func withdrawableCommission(ctx context.Context, tx pgx.Tx,
	tenantID, userID, currency, accountID string) (int64, error) {

	var ledger int64
	if accountID != "" {
		var err error
		if ledger, err = Balance(ctx, tx, accountID); err != nil {
			return 0, err
		}
	}
	var reserved int64
	if err := tx.QueryRow(ctx, unpostedWithdrawalsSQL,
		tenantID, userID, currency).Scan(&reserved); err != nil {
		return 0, err
	}
	return ledger - reserved, nil
}

// commissionAvailableSnapshot 是只读展示用的同口径快照，不加锁、不建科目。
func commissionAvailableSnapshot(ctx context.Context, tx pgx.Tx,
	tenantID, userID, currency string) (int64, error) {

	var accountID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM ledger_accounts
		 WHERE tenant_id = $1 AND account_type = $2
		   AND owner_user_id = $3::uuid AND currency = $4`,
		tenantID, AccountUserCommissionAvailable, userID, currency).Scan(&accountID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	available, err := withdrawableCommission(ctx, tx, tenantID, userID, currency, accountID)
	if err != nil {
		return 0, err
	}
	if available < 0 {
		available = 0
	}
	return available, nil
}
