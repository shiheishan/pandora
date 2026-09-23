package billing

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// CommissionTransferIdempotencyScope is globally unique across API actions.
// 必须匹配 middleware 的幂等 scope 校验 [a-z0-9_]{1,64}（不得含点号）。
const CommissionTransferIdempotencyScope = "commission_transfer_to_balance"

// TransferCommissionToBalance 把可提现佣金转成账户余额（对标 Xboard user/transfer）。
//
// 为什么这个功能值得存在：提现要走审批、要等打款、有最低门槛（100 元）。
// 而用户往往只是想拿佣金续个费 —— 那笔钱本来就要回到站内，
// 绕一圈银行卡再充值回来对谁都没好处。
//
// 账本动作：借 可提现佣金 / 贷 用户余额。两个都是贷方科目，
// 借记佣金即减少负债，贷记余额即增加负债，总额不变 ——
// 这是一次负债在科目间的搬家，不是收入也不是支出。
func (s *Service) TransferCommissionToBalance(ctx context.Context,
	tenantID, userID string, amount int64) (string, error) {

	if tenantID == "" || userID == "" {
		return "", httpx.New(httpx.CodeBadRequest, "tenant and user are required")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return "", httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	if amount <= 0 {
		return "", httpx.Invalid(map[string]string{"amount": "转入金额必须大于 0"})
	}

	var txnID string
	// Serializable：并发两次转账不能把同一笔佣金转出两遍。
	err := s.pool.InTxSerializable(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		currency := "CNY"
		accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, []ledgerAccountSpec{
			{Key: "commission", AccountType: AccountUserCommissionAvailable,
				Currency: currency, UserID: &userID},
			{Key: "balance", AccountType: AccountUserBalance,
				Currency: currency, UserID: &userID},
		})
		if err != nil {
			return err
		}

		// 余额必须在锁住科目之后再读：先读后锁的话，
		// 中间有另一笔转账进来，这里看到的就是过期的数字。
		avail, err := Balance(ctx, tx, accounts["commission"])
		if err != nil {
			return err
		}
		if avail < amount {
			return httpx.New(httpx.CodeConflict,
				"可提现佣金不足。冻结期内的佣金要等解冻后才能转出")
		}

		txnID, err = Post(ctx, tx, tenantID, Posting{
			Kind: "commission_to_balance", Currency: currency,
			Memo: "佣金转入余额", ActorKind: "user", ActorID: &userID,
			Entries: []Entry{
				{AccountID: accounts["commission"], Direction: Debit, Amount: amount,
					Description: "transfer commission out"},
				{AccountID: accounts["balance"], Direction: Credit, Amount: amount,
					Description: "commission transferred to balance"},
			},
		})
		if err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "commission.transferred_to_balance", ResourceType: "ledger_transaction",
			ResourceID: &txnID,
			AfterDigest: map[string]any{
				"amount": amount, "currency": currency,
				"commission_before": avail, "commission_after": avail - amount,
			},
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return "", he
		}
		return "", err
	}
	return txnID, nil
}
