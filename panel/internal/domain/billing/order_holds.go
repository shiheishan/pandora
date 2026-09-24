// [INPUT]: 依赖 reservations.go 的 prepareAndLockLedgerAccounts、ledger.go 的 Balance/Post 与科目类型
// [OUTPUT]: 对包内提供 insertHeldReservation、prepareBalanceHold、postBalanceHold
// [POS]: billing 各种建单路径（checkout.go 的新购、topup.go 的充值、traffic_pack.go 的流量包）共用的预留父节点与余额冻结步骤；零元单捕获仍在 checkout.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 每张订单都有一个预留父节点（order_reservations）与一条 reserve 事件；
// 用余额抵扣的订单还要把那部分余额从可用转进冻结。三条建单路径各写一遍时，
// 这些语句曾经逐字重复 —— 数据库的预留图不变量要求它们完全一致，
// 一处改漏就是只在某一种订单上才暴露的 check_violation。

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// insertHeldReservation 建订单的预留父节点与第一条 held 事件，返回预留 ID。
func insertHeldReservation(ctx context.Context, tx pgx.Tx, tenantID, orderID,
	userID, businessRequestID string, expiresAt time.Time) (string, error) {

	var reservationID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO order_reservations
			(tenant_id, order_id, user_id, expires_at)
		VALUES ($1, $2::uuid, $3::uuid, $4)
		RETURNING id::text`,
		tenantID, orderID, userID, expiresAt,
	).Scan(&reservationID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_reservation_events
			(tenant_id, reservation_id, order_id, from_state, to_state,
			 event_kind, business_request_id, actor_kind, actor_id)
		VALUES ($1, $2::uuid, $3::uuid, NULL, 'held',
		        'reserve', $4::uuid, 'user', $5::uuid)`,
		tenantID, reservationID, orderID, businessRequestID, userID,
	); err != nil {
		return "", err
	}
	return reservationID, nil
}

// balanceHoldAccounts 是一次余额抵扣要动的两个科目。
type balanceHoldAccounts struct {
	AvailableID string
	HoldID      string
}

// prepareBalanceHold 在建单之前锁住余额科目并确认够扣。
//
// 两侧科目在拿任何行锁之前一起准备好；零元单（payable = 0）的捕获还要记收入，
// 收入科目也并进同一个按 UUID 排序的锁集合，避免与结算路径交叉死锁。
func prepareBalanceHold(ctx context.Context, tx pgx.Tx, tenantID, userID,
	currency string, amount, payable int64) (balanceHoldAccounts, error) {

	specs := []ledgerAccountSpec{
		{Key: "available", AccountType: AccountUserBalance,
			Currency: currency, UserID: &userID},
		{Key: "hold", AccountType: AccountUserBalanceHold,
			Currency: currency, UserID: &userID},
	}
	if payable == 0 {
		specs = append(specs, ledgerAccountSpec{
			Key: "revenue", AccountType: AccountPlatformRevenue,
			Currency: currency, OwnerRef: "main",
		})
	}
	accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, specs)
	if err != nil {
		return balanceHoldAccounts{}, err
	}
	out := balanceHoldAccounts{AvailableID: accounts["available"], HoldID: accounts["hold"]}
	avail, err := Balance(ctx, tx, out.AvailableID)
	if err != nil {
		return balanceHoldAccounts{}, err
	}
	if avail < amount {
		return balanceHoldAccounts{}, httpx.New(httpx.CodeConflict, "余额不足")
	}
	return out, nil
}

// postBalanceHold 把抵扣的余额从可用转进冻结，并登记 balance_holds。
func postBalanceHold(ctx context.Context, tx pgx.Tx, tenantID, reservationID,
	orderID, userID, currency string, amount int64, accounts balanceHoldAccounts) error {

	holdTxnID, err := Post(ctx, tx, tenantID, Posting{
		Kind: "balance_hold", Currency: currency,
		SourceType: "order", SourceID: &orderID,
		Memo: "order balance hold", ActorKind: "user", ActorID: &userID,
		Entries: []Entry{
			{AccountID: accounts.AvailableID, Direction: Debit, Amount: amount,
				Description: "order balance reserved"},
			{AccountID: accounts.HoldID, Direction: Credit, Amount: amount,
				Description: "order balance hold liability"},
		},
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO balance_holds
			(tenant_id, reservation_id, order_id, user_id, currency, amount,
			 available_account_id, hold_account_id, hold_txn_id)
		VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5, $6,
		        $7::uuid, $8::uuid, $9::uuid)`,
		tenantID, reservationID, orderID, userID, currency, amount,
		accounts.AvailableID, accounts.HoldID, holdTxnID,
	)
	return err
}
