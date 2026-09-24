// [INPUT]: 依赖 commission_available.go 的可用佣金口径与科目锁，依赖 ledger.go / reservations.go 的记账与加锁原语，依赖 domain/payment 的 MulDiv
// [OUTPUT]: 对外提供 CommissionSummary、ListMyCommissions、RequestWithdrawal、ListMyWithdrawals、PostWithdrawalPayout、SettleMatured、CommissionWithdrawalIdempotencyScope、CommissionScope* 与 ValidCommissionScope、提现错误
// [POS]: billing 分销佣金的计提（计佣范围 first_order 时被推荐人只计第一笔）、解冻、提现申请与打款记账；转余额在 commission_transfer.go，两者共用同一口径
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 分销佣金。
//
// 三个阶段，每一步都在账本上留痕：
//
//	计提  订单支付成功 → 借 平台收入 / 贷 待结佣金     （欠推荐人的钱产生了）
//	解冻  冻结期满     → 借 待结佣金 / 贷 可提现佣金   （钱可以拿了）
//	打款  提现完成     → 借 可提现佣金 / 贷 渠道资金   （钱出去了）
//
// 冻结期是必须的：订单可能退款、可能是盗刷。没有冻结期，
// 骗子用盗刷的卡下单、当场提走佣金、然后持卡人发起拒付 ——
// 平台既退了款又赔了佣金。冻结期把这个套利窗口关上。
//
// 提现申请与审批不记账，只改状态。真正的资金变动发生在打款那一刻，
// 提前记账会让「审批中」的钱在账本上凭空消失一段时间。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var (
	ErrNoCommission                    = httpx.New(httpx.CodeValidationFailed, "没有可提现的佣金")
	ErrWithdrawTooSmall                = httpx.New(httpx.CodeValidationFailed, "提现金额低于最低限额")
	ErrWithdrawTooMuch                 = httpx.New(httpx.CodeConflict, "提现金额超过可提现余额")
	ErrWithdrawPending                 = httpx.New(httpx.CodeConflict, "还有正在处理的提现申请")
	ErrWithdrawCurrencyAmbiguous       = httpx.New(httpx.CodeConflict, "可提现佣金包含多个币种，请联系管理员分币种处理")
	errCommissionSettlementUnderfunded = errors.New("pending commission ledger is underfunded")
	errCommissionPayoutUnderfunded     = errors.New("available commission ledger is underfunded")
	errCommissionOrderIneligible       = errors.New("commission order is no longer settlement eligible")
)

const maxMaturedCommissionBatch = 500

// CommissionWithdrawalIdempotencyScope 是门户 POST v1/me/withdrawals 的幂等域。
const CommissionWithdrawalIdempotencyScope = "commission_withdrawal_request"

// 计佣范围（system_settings commission.scope，字符串）。没有这条设置时按
// every_order 兜底，与引入这个开关之前的行为一致。
const (
	CommissionScopeFirstOrder = "first_order" // 只对被推荐人的第一笔计佣订单返佣
	CommissionScopeEveryOrder = "every_order" // 被推荐人每一笔订单都返佣
)

// ValidCommissionScope 判断后台写入的计佣范围是否合法。
func ValidCommissionScope(scope string) bool {
	return scope == CommissionScopeFirstOrder || scope == CommissionScopeEveryOrder
}

// commissionConfig 是租户级的分销参数。
type commissionConfig struct {
	RatePercent int    // 整数百分比。精度就到 1%，再细对代理没有意义
	FreezeDays  int    // 冻结天数
	MinWithdraw int64  // 最低提现金额，最小货币单位
	Scope       string // 计佣范围，见 CommissionScope*
}

func loadCommissionConfig(ctx context.Context, tx pgx.Tx, tenantID string) (commissionConfig, error) {
	// 兜底值与迁移里的默认值保持一致：设置被误删时行为不会突变
	c := commissionConfig{RatePercent: 0, FreezeDays: 3, MinWithdraw: 10000}
	err := tx.QueryRow(ctx, `
		SELECT COALESCE((SELECT (value #>> '{}')::int FROM system_settings
		                  WHERE tenant_id = $1 AND key = 'commission.rate_percent'), 0),
		       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
		                  WHERE tenant_id = $1 AND key = 'commission.freeze_days'), 3),
		       COALESCE((SELECT (value #>> '{}')::bigint FROM system_settings
		                  WHERE tenant_id = $1 AND key = 'commission.min_withdraw'), 10000),
		       COALESCE((SELECT value #>> '{}' FROM system_settings
		                  WHERE tenant_id = $1 AND key = 'commission.scope'), '')`,
		tenantID).Scan(&c.RatePercent, &c.FreezeDays, &c.MinWithdraw, &c.Scope)
	if !ValidCommissionScope(c.Scope) {
		c.Scope = CommissionScopeEveryOrder
	}
	return c, err
}

// accrueCommission 在订单支付成功的事务里计提佣金。
//
// 返回 nil 表示这一单没有佣金（没有推荐人、费率为零、或已经计提过），
// 这都是正常情况，不是错误 —— 绝大多数订单本来就没有推荐关系。
func (s *Service) accrueCommission(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, buyerUserID, currency string, baseAmount int64) error {

	if baseAmount <= 0 {
		return nil
	}

	var referrerID string
	var riskFlag string
	err := tx.QueryRow(ctx, `
		SELECT referrer_user_id::text, risk_flag
		  FROM referrals
		 WHERE tenant_id = $1 AND referee_user_id = $2::uuid`,
		tenantID, buyerUserID).Scan(&referrerID, &riskFlag)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if riskFlag == "confirmed_fraud" {
		// 已确认的欺诈推荐不再产生新佣金。不报错也不记录 ——
		// 这一单照常成交，只是不给分成
		return nil
	}

	cfg, err := loadCommissionConfig(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	if cfg.RatePercent <= 0 {
		return nil
	}
	if cfg.Scope == CommissionScopeFirstOrder {
		// 「首单」按佣金记录判：被推荐人已有别的订单计过佣（含后来冲销的），
		// 这一单就不再计。回调重放同一单时 order_id 相同，不算「别的订单」。
		var earlier bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM commission_entries
			                WHERE tenant_id = $1 AND referee_user_id = $2::uuid
			                  AND order_id <> $3::uuid)`,
			tenantID, buyerUserID, orderID).Scan(&earlier); err != nil {
			return err
		}
		if earlier {
			return nil
		}
	}

	rateBP := cfg.RatePercent * 100
	amount, err := payment.MulDiv(baseAmount, int64(rateBP), 10000)
	if err != nil {
		return err
	}
	if amount <= 0 {
		return nil
	}

	// 同源账号自推自买是最常见的薅法。标记出来让人工过一眼，
	// 而不是直接拒绝 —— 一家人共用宽带互相推荐是合理的。
	var sameSource bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM audit_events a
			 WHERE a.tenant_id = $1 AND a.actor_id = $2::uuid
			   AND a.source_ip_hash IS NOT NULL
			   AND EXISTS (SELECT 1 FROM audit_events b
			                WHERE b.tenant_id = $1 AND b.actor_id = $3::uuid
			                  AND b.source_ip_hash = a.source_ip_hash))`,
		tenantID, referrerID, buyerUserID).Scan(&sameSource); err != nil {
		return err
	}

	reviewReason := ""
	if sameSource || riskFlag == "suspicious" {
		if sameSource {
			reviewReason = "推荐人与购买人来自同一来源地址"
		} else {
			reviewReason = "推荐关系已被标记为可疑"
		}
	}

	pendingAcct, err := EnsureAccount(ctx, tx, tenantID, AccountUserCommissionPending,
		currency, &referrerID, "")
	if err != nil {
		return err
	}
	revenueAcct, err := EnsureAccount(ctx, tx, tenantID, AccountPlatformRevenue,
		currency, nil, "main")
	if err != nil {
		return err
	}

	txnID, err := Post(ctx, tx, tenantID, Posting{
		Kind: "commission_accrued", Currency: currency,
		SourceType: "order", SourceID: &orderID,
		Memo:      "分销佣金计提",
		ActorKind: "system",
		Entries: []Entry{
			{AccountID: revenueAcct, Direction: Debit, Amount: amount,
				Description: "佣金支出冲减收入"},
			{AccountID: pendingAcct, Direction: Credit, Amount: amount,
				Description: "待结佣金"},
		},
	})
	if err != nil {
		return err
	}

	frozenUntil := time.Now().Add(time.Duration(cfg.FreezeDays) * 24 * time.Hour)
	// UNIQUE (order_id, referrer_user_id) 兜底：支付回调重放时
	// 不会给同一单重复计提
	tag, err := tx.Exec(ctx, `
		INSERT INTO commission_entries
			(tenant_id, referrer_user_id, referee_user_id, order_id, currency,
			 base_amount, rate_bp, commission_amount, frozen_until,
			 review_required, review_reason, accrual_txn_id)
		VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12::uuid)
		ON CONFLICT (order_id, referrer_user_id) DO NOTHING`,
		tenantID, referrerID, buyerUserID, orderID, currency,
		baseAmount, rateBP, amount, frozenUntil,
		reviewReason != "", nullIfEmptyStr(reviewReason), txnID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("commission accrual claim already exists")
	}
	return nil
}

func nullIfEmptyStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// SettleMatured 把冻结期已过的佣金转为可提现。
//
// 逐条处理而不是一条 UPDATE 批量搞定：每条都要记一笔账，
// 而记账要按用户和币种取账户。批量改状态却不记账，
// 会让账本余额和佣金表对不上 —— 那种错要靠对账才能发现。
func (s *Service) SettleMatured(ctx context.Context, tenantID string) (int, error) {
	if tenantID == "" {
		return 0, errors.New("commission settlement requires a tenant")
	}

	// Candidate locks live only for this bounded discovery transaction. Each
	// candidate is re-locked and revalidated in its own transaction below, so
	// two workers may discover the same ID but can never settle it twice.
	ids, err := s.discoverMaturedCommissionIDs(ctx, tenantID)
	if err != nil {
		return 0, err
	}

	settled := 0
	var failures []error
	for _, entryID := range ids {
		committed, warning, err := s.settleMaturedCommission(ctx, tenantID, entryID)
		if err != nil {
			failures = append(failures, fmt.Errorf("settle commission %s: %w", entryID, err))
			continue
		}
		if warning != nil {
			// The warning is returned only after the review quarantine committed.
			// It reports evidence without rolling back the quarantine or blocking
			// later candidates in the same bounded worker pass.
			failures = append(failures, warning)
		}
		if committed {
			settled++
		}
	}
	return settled, errors.Join(failures...)
}

func (s *Service) discoverMaturedCommissionIDs(ctx context.Context,
	tenantID string) ([]string, error) {
	var ids []string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text
			  FROM commission_entries
			 WHERE tenant_id=$1 AND status='pending'
			   AND review_required=false AND review_reason IS NULL
			   AND settle_txn_id IS NULL
			   AND frozen_until IS NOT NULL AND frozen_until<=now()
			 ORDER BY frozen_until,id
			 LIMIT $2 FOR UPDATE SKIP LOCKED`, tenantID, maxMaturedCommissionBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

func (s *Service) settleMaturedCommission(ctx context.Context, tenantID,
	entryID string) (committed bool, warning error, err error) {
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// Refund and chargeback flows serialize on the order row. Lock it before
		// the commission row so a refund cannot race a frozen commission into the
		// available balance.
		var orderID string
		err := tx.QueryRow(ctx, `
			SELECT order_id::text
			  FROM commission_entries
			 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, entryID).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var orderStatus string
		err = tx.QueryRow(ctx, `
			SELECT status
			  FROM orders
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR UPDATE`, tenantID, orderID).Scan(&orderStatus)
		if err != nil {
			return err
		}

		var lockedOrderID, userID, currency, status string
		var amount int64
		var reviewRequired, due bool
		var reviewReason, settleTxnID *string
		err = tx.QueryRow(ctx, `
			SELECT order_id::text,referrer_user_id::text,currency::text,commission_amount,status,
			       review_required,review_reason,
			       frozen_until IS NOT NULL AND frozen_until<=now(),
			       settle_txn_id::text
			  FROM commission_entries
			 WHERE tenant_id=$1 AND id=$2::uuid
			 FOR UPDATE`, tenantID, entryID).Scan(
			&lockedOrderID, &userID, &currency, &amount, &status, &reviewRequired, &reviewReason,
			&due, &settleTxnID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if lockedOrderID != orderID {
			return errors.New("commission order identity changed while locking")
		}
		if status != "pending" || reviewRequired || reviewReason != nil || !due || settleTxnID != nil {
			return nil
		}
		if orderStatus != "paid" && orderStatus != "fulfilled" {
			reason := fmt.Sprintf("order status %s is not commission-settlement eligible", orderStatus)
			tag, err := tx.Exec(ctx, `
				UPDATE commission_entries
				   SET review_required=true,review_reason=$3,updated_at=now()
				 WHERE tenant_id=$1 AND id=$2::uuid
				   AND status='pending' AND review_required=false AND review_reason IS NULL
				   AND settle_txn_id IS NULL
				   AND frozen_until IS NOT NULL AND frozen_until<=now()`,
				tenantID, entryID, reason)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("ineligible commission review quarantine transition lost")
			}
			if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
				return err
			}
			warning = fmt.Errorf("%w: entry=%s order=%s status=%s",
				errCommissionOrderIneligible, entryID, orderID, orderStatus)
			return nil
		}

		accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID,
			[]ledgerAccountSpec{
				{Key: "pending", AccountType: AccountUserCommissionPending,
					Currency: currency, UserID: &userID},
				{Key: "available", AccountType: AccountUserCommissionAvailable,
					Currency: currency, UserID: &userID},
			})
		if err != nil {
			return err
		}
		pendingAcct := accounts["pending"]
		availableAcct := accounts["available"]
		pendingBalance, err := Balance(ctx, tx, pendingAcct)
		if err != nil {
			return err
		}
		if pendingBalance < amount {
			reason := fmt.Sprintf("pending commission ledger underfunded: balance=%d required=%d",
				pendingBalance, amount)
			tag, err := tx.Exec(ctx, `
				UPDATE commission_entries
				   SET review_required=true,review_reason=$3,updated_at=now()
				 WHERE tenant_id=$1 AND id=$2::uuid
				   AND status='pending' AND review_required=false AND review_reason IS NULL
				   AND settle_txn_id IS NULL
				   AND frozen_until IS NOT NULL AND frozen_until<=now()`,
				tenantID, entryID, reason)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("underfunded commission review quarantine transition lost")
			}
			if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
				return err
			}
			warning = fmt.Errorf("%w: entry=%s balance=%d required=%d",
				errCommissionSettlementUnderfunded, entryID, pendingBalance, amount)
			return nil
		}

		txnID, err := Post(ctx, tx, tenantID, Posting{
			Kind: "commission_settled", Currency: currency,
			SourceType: "commission_entry", SourceID: &entryID,
			Memo: "佣金解冻", ActorKind: "system",
			Entries: []Entry{
				{AccountID: pendingAcct, Direction: Debit, Amount: amount,
					Description: "待结佣金转出"},
				{AccountID: availableAcct, Direction: Credit, Amount: amount,
					Description: "可提现佣金"},
			},
		})
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE commission_entries
			   SET status='available',settle_txn_id=$3::uuid,updated_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid
			   AND status='pending' AND review_required=false AND review_reason IS NULL
			   AND settle_txn_id IS NULL
			   AND frozen_until IS NOT NULL AND frozen_until<=now()`,
			tenantID, entryID, txnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("commission settlement transition lost")
		}
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			return err
		}
		committed = true
		return nil
	})
	return committed, warning, err
}

// CommissionSummary 是用户看到的佣金概况。
type CommissionSummary struct {
	Currency    string `json:"currency"`
	Pending     int64  `json:"pending"`      // 冻结中
	Available   int64  `json:"available"`    // 可提现
	Withdrawing int64  `json:"withdrawing"`  // 提现处理中
	Settled     int64  `json:"settled"`      // 已提现
	Invitees    int    `json:"invitees"`     // 邀请人数
	Orders      int    `json:"orders"`       // 产生佣金的订单数
	RatePercent int    `json:"rate_percent"` // 当前费率
	MinWithdraw int64  `json:"min_withdraw"`
}

func (s *Service) CommissionSummary(ctx context.Context, tenantID, userID string) (*CommissionSummary, error) {
	out := CommissionSummary{Currency: "CNY"}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		cfg, err := loadCommissionConfig(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		out.RatePercent = cfg.RatePercent
		out.MinWithdraw = cfg.MinWithdraw

		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(sum(commission_amount) FILTER (WHERE status = 'pending'), 0),
			       count(*) FILTER (WHERE status <> 'reversed'),
			       COALESCE(max(currency::text), 'CNY')
			  FROM commission_entries
			 WHERE tenant_id = $1 AND referrer_user_id = $2::uuid`,
			tenantID, userID).Scan(&out.Pending, &out.Orders, &out.Currency); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(sum(amount) FILTER (
			         WHERE status IN ('requested','reviewing','approved','processing')), 0),
			       COALESCE(sum(amount) FILTER (WHERE status = 'paid'), 0)
			  FROM withdrawals
			 WHERE tenant_id = $1 AND user_id = $2::uuid`,
			tenantID, userID).Scan(&out.Withdrawing, &out.Settled); err != nil {
			return err
		}

		// 可提现与「全部转入余额」用的是同一个数，口径见 commission_available.go
		if out.Available, err = commissionAvailableSnapshot(ctx, tx,
			tenantID, userID, out.Currency); err != nil {
			return err
		}

		return tx.QueryRow(ctx, `
			SELECT count(*) FROM referrals
			 WHERE tenant_id = $1 AND referrer_user_id = $2::uuid`,
			tenantID, userID).Scan(&out.Invitees)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListMyCommissions 返回佣金明细。
func (s *Service) ListMyCommissions(ctx context.Context, tenantID, userID string) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.commission_amount, c.base_amount, c.rate_bp, c.currency::text,
			       c.status, c.frozen_until, c.created_at,
			       COALESCE(o.order_no, ''), COALESCE(u.email::text, '')
			  FROM commission_entries c
			  LEFT JOIN orders o ON o.id = c.order_id
			  LEFT JOIN users u ON u.id = c.referee_user_id
			 WHERE c.tenant_id = $1 AND c.referrer_user_id = $2::uuid
			 ORDER BY c.created_at DESC LIMIT 100`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var amt, base int64
			var rate int
			var cur, status, orderNo, email string
			var frozen, created any
			if err := rows.Scan(&amt, &base, &rate, &cur, &status, &frozen,
				&created, &orderNo, &email); err != nil {
				return err
			}
			out = append(out, map[string]any{
				"amount": amt, "base": base, "rate_percent": rate / 100,
				"currency": cur, "status": status, "frozen_until": frozen,
				"created_at": created, "order_no": orderNo,
				"from": maskEmailAddr(email),
			})
		}
		return rows.Err()
	})
	return out, err
}

func maskEmailAddr(e string) string {
	for i := 0; i < len(e); i++ {
		if e[i] == '@' {
			if i <= 2 {
				return "***" + e[i:]
			}
			return e[:2] + "***" + e[i:]
		}
	}
	return "***"
}

// RequestWithdrawal 发起一次提现申请。
func (s *Service) RequestWithdrawal(ctx context.Context, tenantID, userID string,
	amount int64, payoutDetail string) (string, error) {

	var id string
	// 与 TransferCommissionToBalance 同一把锁、同一口径（commission_available.go）。
	// 锁等待之后的序列化冲突（40001）在这里自动重试，后到的一方拿到的是
	// 「余额不足」这类业务错误，而不是 500。
	err := s.pool.InTxSerializableRetry(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		id = ""
		cfg, err := loadCommissionConfig(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if amount < cfg.MinWithdraw {
			return ErrWithdrawTooSmall
		}

		accounts, currencies, err := lockUserCommissionAccounts(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if len(currencies) == 0 {
			return ErrNoCommission
		}

		// 一次只允许一笔在途。多笔并行会让「可提现余额」
		// 变成一道需要减去若干在途金额的算术题，用户算错就会反复被拒
		var inFlight int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM withdrawals
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			   AND status IN ('requested','reviewing','approved','processing')`,
			tenantID, userID).Scan(&inFlight); err != nil {
			return err
		}
		if inFlight > 0 {
			return ErrWithdrawPending
		}

		var currency string
		var available int64
		positive := 0
		for _, candidate := range currencies {
			avail, err := withdrawableCommission(ctx, tx, tenantID, userID,
				candidate, accounts[candidate])
			if err != nil {
				return err
			}
			if avail > 0 {
				positive++
				currency, available = candidate, avail
			}
		}
		if positive > 1 {
			// The public request currently has no currency field. Never combine
			// balances with different monetary units or guess which one the user
			// intended; a future version can expose an explicit currency selector.
			return ErrWithdrawCurrencyAmbiguous
		}
		if positive == 0 {
			return ErrNoCommission
		}
		if amount > available {
			return ErrWithdrawTooMuch
		}

		// 收款信息是强个人信息：支付宝账号、银行卡号。
		// 加密存储，AAD 用 "payout" 与其它密文隔离
		var enc []byte
		if payoutDetail != "" && s.envelope != nil {
			enc, err = s.envelope.Seal([]byte(payoutDetail), []byte("payout"))
			if err != nil {
				return err
			}
		}

		return tx.QueryRow(ctx, `
			INSERT INTO withdrawals
				(tenant_id, user_id, currency, amount, payout_detail_encrypted, status)
			VALUES ($1,$2::uuid,$3,$4,$5,'requested')
			RETURNING id::text`,
			tenantID, userID, currency, amount, enc).Scan(&id)
	})
	return id, err
}

// ListMyWithdrawals 返回用户自己的提现记录。
func (s *Service) ListMyWithdrawals(ctx context.Context, tenantID, userID string) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, amount, currency::text, status,
			       COALESCE(reject_reason, ''), requested_at, completed_at
			  FROM withdrawals
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			 ORDER BY requested_at DESC LIMIT 50`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, cur, status, reason string
			var amt int64
			var req, done any
			if err := rows.Scan(&id, &amt, &cur, &status, &reason, &req, &done); err != nil {
				return err
			}
			out = append(out, map[string]any{
				"id": id, "amount": amt, "currency": cur, "status": status,
				"reject_reason": reason, "requested_at": req, "completed_at": done,
			})
		}
		return rows.Err()
	})
	return out, err
}

// PostWithdrawalPayout 记一笔提现打款。
//
// 由管理端在自己的事务里调用：审批与打款是管理动作，
// 但记账规则属于 billing —— 让 admin 包自己拼分录，
// 迟早会出现两处对同一件事记法不一致的账。
func (s *Service) PostWithdrawalPayout(ctx context.Context, tx pgx.Tx, tenantID,
	userID, currency string, amount int64, withdrawalID string) (string, error) {
	if tenantID == "" || userID == "" || currency == "" || withdrawalID == "" || amount <= 0 {
		return "", errors.New("commission payout identity and positive amount are required")
	}

	// Revalidate the approved business row under lock even when the current
	// admin caller already selected it. This keeps the billing primitive safe
	// for every caller and makes duplicate payout attempts fail before money.
	var storedUser, storedCurrency, status string
	var storedAmount int64
	var existingTxn *string
	err := tx.QueryRow(ctx, `
		SELECT user_id::text,currency::text,amount,status,payout_txn_id::text
		  FROM withdrawals
		 WHERE tenant_id=$1 AND id=$2::uuid
		 FOR UPDATE`, tenantID, withdrawalID).
		Scan(&storedUser, &storedCurrency, &storedAmount, &status, &existingTxn)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("approved commission withdrawal not found")
	}
	if err != nil {
		return "", err
	}
	if storedUser != userID || storedCurrency != currency || storedAmount != amount ||
		status != "approved" || existingTxn != nil {
		return "", errors.New("commission withdrawal is not an unposted approved payout")
	}

	// Create both accounts by deterministic business identity, resolve their
	// UUIDs, then lock the complete participating set in global UUID order.
	// No available-commission debit is written before this lock set and its
	// authoritative ledger balance check have completed.
	accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID,
		[]ledgerAccountSpec{
			{Key: "available", AccountType: AccountUserCommissionAvailable,
				Currency: currency, UserID: &userID},
			{Key: "channel", AccountType: AccountChannelCash,
				Currency: currency, OwnerRef: "payout"},
		})
	if err != nil {
		return "", err
	}
	availableAcct := accounts["available"]
	channelAcct := accounts["channel"]
	availableBalance, err := Balance(ctx, tx, availableAcct)
	if err != nil {
		return "", err
	}
	if availableBalance < amount {
		return "", fmt.Errorf("%w: balance=%d required=%d",
			errCommissionPayoutUnderfunded, availableBalance, amount)
	}

	txnID, err := Post(ctx, tx, tenantID, Posting{
		Kind: "commission_paid", Currency: currency,
		SourceType: "withdrawal", SourceID: &withdrawalID,
		Memo: "佣金提现打款", ActorKind: "admin",
		Entries: []Entry{
			{AccountID: availableAcct, Direction: Debit, Amount: amount,
				Description: "可提现佣金结清"},
			{AccountID: channelAcct, Direction: Credit, Amount: amount,
				Description: "打款支出"},
		},
	})
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE withdrawals
		   SET status='processing',payout_txn_id=$3::uuid,updated_at=now()
		 WHERE tenant_id=$1 AND id=$2::uuid
		   AND status='approved' AND payout_txn_id IS NULL`,
		tenantID, withdrawalID, txnID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("commission payout transition lost")
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return "", err
	}
	return txnID, nil
}
