// Package billing 实现订单、支付与复式账本。
package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AccountType 是会计科目类型，与迁移里的 CHECK 约束保持一致。
type AccountType string

const (
	AccountUserBalance             AccountType = "user_balance"
	AccountUserBalanceHold         AccountType = "user_balance_hold"
	AccountUserCommissionPending   AccountType = "user_commission_pending"
	AccountUserCommissionAvailable AccountType = "user_commission_available"
	AccountChannelCash             AccountType = "channel_cash"
	AccountPlatformRevenue         AccountType = "platform_revenue"
	AccountPlatformFeeExpense      AccountType = "platform_fee_expense"
	AccountRefundPayable           AccountType = "refund_payable"
	AccountDisputeHold             AccountType = "dispute_hold"
	AccountSuspense                AccountType = "suspense"
	AccountLatePaymentSuspense     AccountType = "late_payment_suspense"
)

// normalBalance 决定科目的余额方向。
//
// 会计惯例：资产与费用类借方增加；负债、权益、收入类贷方增加。
// 用户余额对平台是「欠用户的钱」，属负债；渠道资金是平台持有的资产。
var normalBalance = map[AccountType]string{
	AccountUserBalance:             "credit",
	AccountUserBalanceHold:         "credit",
	AccountUserCommissionPending:   "credit",
	AccountUserCommissionAvailable: "credit",
	AccountChannelCash:             "debit",
	AccountPlatformRevenue:         "credit",
	AccountPlatformFeeExpense:      "debit",
	AccountRefundPayable:           "credit",
	AccountDisputeHold:             "credit",
	AccountSuspense:                "debit",
	AccountLatePaymentSuspense:     "credit",
}

type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// Entry 是一条待记分录。Amount 恒为正，方向由 Direction 表达。
type Entry struct {
	AccountID   string
	Direction   Direction
	Amount      int64
	Description string
}

// Posting 是一笔完整的账务交易。
type Posting struct {
	Kind       string // order_paid / refund_issued / balance_topup / commission_accrued ...
	Currency   string
	SourceType string
	SourceID   *string
	Memo       string
	ActorKind  string
	ActorID    *string
	Entries    []Entry
}

var ErrUnbalanced = errors.New("借贷不平")

// EnsureAccount 取得账户 ID，不存在则创建。
//
// 用 ON CONFLICT DO UPDATE 而非 DO NOTHING：DO NOTHING 在冲突时不返回行，
// 还得再查一次；DO UPDATE 到一个无副作用的赋值上即可直接 RETURNING。
func EnsureAccount(ctx context.Context, tx pgx.Tx, tenantID string,
	at AccountType, currency string, ownerUserID *string, ownerRef string) (string, error) {

	nb, ok := normalBalance[at]
	if !ok {
		return "", fmt.Errorf("未知科目类型 %q", at)
	}

	var id string
	var err error

	if ownerUserID != nil {
		err = tx.QueryRow(ctx, `
			INSERT INTO ledger_accounts
				(tenant_id, account_type, normal_balance, currency, owner_user_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, account_type, owner_user_id, currency)
				WHERE owner_user_id IS NOT NULL
			DO UPDATE SET updated_at = ledger_accounts.updated_at
			RETURNING id`,
			tenantID, at, nb, currency, *ownerUserID).Scan(&id)
	} else {
		err = tx.QueryRow(ctx, `
			INSERT INTO ledger_accounts
				(tenant_id, account_type, normal_balance, currency, owner_ref)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, account_type, coalesce(owner_ref, ''), currency)
				WHERE owner_user_id IS NULL
			DO UPDATE SET updated_at = ledger_accounts.updated_at
			RETURNING id`,
			tenantID, at, nb, currency, ownerRef).Scan(&id)
	}

	if err != nil {
		return "", fmt.Errorf("取得账户 %s/%s: %w", at, currency, err)
	}
	return id, nil
}

// Post 记一笔账。
//
// 应用层先自查一次配平，纯粹是为了给出可读的错误信息 ——
// 真正的强制在数据库的延迟约束触发器上，那一层任何绕过应用的写入也逃不掉。
func Post(ctx context.Context, tx pgx.Tx, tenantID string, p Posting) (string, error) {
	if len(p.Entries) < 2 {
		return "", fmt.Errorf("%w：一笔交易至少需要两条分录", ErrUnbalanced)
	}

	var sum int64
	for _, e := range p.Entries {
		if e.Amount <= 0 {
			return "", fmt.Errorf("分录金额必须为正，收到 %d", e.Amount)
		}
		if e.Direction == Debit {
			sum += e.Amount
		} else {
			sum -= e.Amount
		}
	}
	if sum != 0 {
		return "", fmt.Errorf("%w：借贷差额 %d（币种最小单位）", ErrUnbalanced, sum)
	}

	var txnID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions
			(tenant_id, kind, currency, source_type, source_id, memo, actor_kind, actor_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		tenantID, p.Kind, p.Currency, nullStr(p.SourceType), p.SourceID,
		nullStr(p.Memo), defaultStr(p.ActorKind, "system"), p.ActorID,
	).Scan(&txnID); err != nil {
		return "", fmt.Errorf("创建账务交易: %w", err)
	}

	batch := &pgx.Batch{}
	for _, e := range p.Entries {
		batch.Queue(`
			INSERT INTO ledger_entries
				(tenant_id, transaction_id, account_id, direction, amount, currency, description)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			tenantID, txnID, e.AccountID, e.Direction, e.Amount, p.Currency,
			nullStr(e.Description))
	}

	br := tx.SendBatch(ctx, batch)
	for range p.Entries {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return "", fmt.Errorf("写入分录: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return "", fmt.Errorf("提交分录批次: %w", err)
	}

	return txnID, nil
}

// Balance 返回账户的业务余额（已按科目方向换算为正常语义下的正数）。
//
// 读的是 ledger_accounts.balance_signed 这个由触发器维护的缓存值。
// 它与分录求和的一致性由 app.verify_ledger_account() 随时可验，
// 对账任务会定期跑全量校验。
func Balance(ctx context.Context, tx pgx.Tx, accountID string) (int64, error) {
	var signed int64
	var nb string
	if err := tx.QueryRow(ctx,
		`SELECT balance_signed, normal_balance FROM ledger_accounts WHERE id = $1`,
		accountID).Scan(&signed, &nb); err != nil {
		return 0, err
	}
	if nb == "credit" {
		return -signed, nil
	}
	return signed, nil
}

// LockAccountForUpdate 锁定账户行，用于「先检查余额再扣减」这类需要串行的场景。
// 必须在同一事务内先锁后读，否则并发扣减会双花。
func LockAccountForUpdate(ctx context.Context, tx pgx.Tx, accountID string) (int64, error) {
	var signed int64
	var nb string
	if err := tx.QueryRow(ctx,
		`SELECT balance_signed, normal_balance FROM ledger_accounts
		  WHERE id = $1 FOR UPDATE`,
		accountID).Scan(&signed, &nb); err != nil {
		return 0, err
	}
	if nb == "credit" {
		return -signed, nil
	}
	return signed, nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
