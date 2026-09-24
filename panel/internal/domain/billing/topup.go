// [INPUT]: 依赖 order_holds.go 的 insertHeldReservation、ledger.go 的记账、middleware 幂等声明、platform/idempotencybind
// [OUTPUT]: 对外提供 CreateTopup 与输入输出、TopupIdempotencyScope、AdjustBalance、BalanceOf、ListBalanceHistory；包内提供 postTopupPaid
// [POS]: billing 的余额入金：自助充值单（kind=topup，结算即履约）与管理员调账
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 余额充值与手动调账。
//
// 在这之前余额是只出不进的：下单能用余额抵扣，却没有任何入金路径 ——
// 余额栏永远显示 0，那个抵扣分支等于死代码。
//
// 两条入金通道：
//
//	用户自助  建一张 kind='topup' 的订单 → 走现有支付渠道 → 回调里入账
//	管理员    kind='manual'，直接入账，强制填理由
//
// 充值单复用 orders 表而不是另起一张表：它同样要走支付、要对账、要能退款，
// 单独一套等于把这些能力全部重写一遍。区别只在履约那一步 ——
// 买套餐是开订阅，充值是往余额账户里记一笔。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/idempotencybind"
)

var (
	ErrTopupTooSmall = httpx.New(httpx.CodeValidationFailed, "充值金额太小")
	ErrTopupTooLarge = httpx.New(httpx.CodeValidationFailed, "单次充值金额超出上限")
	ErrTopupCurrency = httpx.New(httpx.CodeValidationFailed, "充值币种只支持 CNY 或 USD")
)

// 单次充值的上下限，最小货币单位。
//
// 下限挡的是「充一分钱」这类试探：每张单都要占用支付渠道的请求配额，
// 也要在对账时被人看一眼。上限挡的是手滑多打几个零 ——
// 真要充这么多，走人工。
const (
	minTopup = 100     // 1 元
	maxTopup = 5000000 // 5 万元

	TopupIdempotencyScope = "balance_topup_create"
)

type CreateTopupInput struct {
	UserID   string
	Amount   int64
	Currency string
	Claim    middleware.IdempotencyClaim
}

type CreateTopupOutput struct {
	OrderID  string `json:"order_id"`
	OrderNo  string `json:"order_no"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`

	prepared httpx.PreparedResponse
}

func (o *CreateTopupOutput) PreparedResponse() httpx.PreparedResponse {
	return o.prepared
}

func validateTopupAmount(amount int64) error {
	if amount < minTopup {
		return ErrTopupTooSmall
	}
	if amount > maxTopup {
		return ErrTopupTooLarge
	}
	return nil
}

func normalizeTopupCurrency(currency string) (string, error) {
	if currency == "" {
		return "CNY", nil
	}
	if currency != "CNY" && currency != "USD" {
		return "", ErrTopupCurrency
	}
	return currency, nil
}

// CreateTopup 建一张充值订单。
func (s *Service) CreateTopup(ctx context.Context, tenantID string,
	in CreateTopupInput) (*CreateTopupOutput, error) {
	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, in.UserID, TopupIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("create topup: %w", err)
	}

	if err := validateTopupAmount(in.Amount); err != nil {
		return nil, err
	}
	currency, err := normalizeTopupCurrency(in.Currency)
	if err != nil {
		return nil, err
	}

	var out CreateTopupOutput
	err = s.pool.InTx(ctx, dbScope(tenantID, in.UserID), func(tx pgx.Tx) error {
		orderNo, err := newOrderNo()
		if err != nil {
			return err
		}
		// 充值单没有折扣也不能用余额付（拿余额充余额没有意义），
		// 所以 subtotal / total / payable 三者相等
		var orderID string
		var expiresAt time.Time
		if err := tx.QueryRow(ctx, `
			INSERT INTO orders
				(tenant_id, order_no, user_id, kind, status, currency,
				 subtotal_amount, discount_amount, tax_amount,
				 total_amount, balance_applied, payable_amount, expires_at,
				 idempotency_key_id)
			VALUES ($1, $2, $3::uuid, 'topup', 'pending_payment', $4,
			        $5, 0, 0, $5, 0, $5, now() + interval '30 minutes',
			        $6::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, currency, in.Amount, in.Claim.ID,
		).Scan(&orderID, &expiresAt); err != nil {
			return err
		}

		if _, err := insertHeldReservation(ctx, tx, tenantID, orderID,
			in.UserID, in.Claim.ID, expiresAt); err != nil {
			return err
		}

		out = CreateTopupOutput{
			OrderID: orderID, OrderNo: orderNo,
			Amount: in.Amount, Currency: currency,
		}
		prepared, err := httpx.PrepareJSON(http.StatusOK, out)
		if err != nil {
			return err
		}
		out.prepared = prepared

		if err := idempotencybind.BindResource(
			ctx, tx, in.Claim, "order", orderID,
		); err != nil {
			return err
		}
		if err := idempotencybind.CompleteSuccessJSON(
			ctx, tx, in.Claim, "order", orderID, prepared,
		); err != nil {
			return err
		}

		userID := in.UserID
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "balance.topup_created", ResourceType: "order", ResourceID: &orderID,
			APIDomain: "public", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"amount": in.Amount, "currency": currency},
		}); err != nil {
			return err
		}

		// Surface every deferred order/idempotency/reservation invariant before
		// the callback returns so no handler can observe a partially valid graph.
		_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type topupPaidPosting struct {
	OrderID      string
	UserID       string
	Currency     string
	Amount       int64
	FeeAmount    int64
	ProviderCode string
}

// topupPaidEntrySpec 是不依赖数据库账户 ID 的分录模板，便于纯单元测试验证经济语义。
type topupPaidEntrySpec struct {
	AccountType AccountType
	Direction   Direction
	Amount      int64
	Description string
}

// buildTopupPaidEntrySpecs 构造充值支付的完整分录：
//
//	借 渠道资金       = 充值额 - 渠道手续费
//	借 平台手续费支出 = 渠道手续费
//	贷 用户余额       = 充值额
//
// 充值只增加负债，不确认平台收入。净额为零时不产生零金额分录。
func buildTopupPaidEntrySpecs(amount, feeAmount int64) ([]topupPaidEntrySpec, error) {
	if amount <= 0 {
		return nil, errors.New("充值入账金额必须为正")
	}
	if feeAmount < 0 {
		return nil, errors.New("渠道手续费不能为负")
	}
	if feeAmount > amount {
		return nil, errors.New("渠道手续费不能超过充值金额")
	}

	entries := make([]topupPaidEntrySpec, 0, 3)
	if net := amount - feeAmount; net > 0 {
		entries = append(entries, topupPaidEntrySpec{
			AccountType: AccountChannelCash,
			Direction:   Debit,
			Amount:      net,
			Description: "渠道净收款",
		})
	}
	if feeAmount > 0 {
		entries = append(entries, topupPaidEntrySpec{
			AccountType: AccountPlatformFeeExpense,
			Direction:   Debit,
			Amount:      feeAmount,
			Description: "渠道手续费",
		})
	}
	entries = append(entries, topupPaidEntrySpec{
		AccountType: AccountUserBalance,
		Direction:   Credit,
		Amount:      amount,
		Description: "用户余额增加",
	})
	return entries, nil
}

// postTopupPaid 把充值支付作为一笔原子 posting 入账。
func (s *Service) postTopupPaid(ctx context.Context, tx pgx.Tx, tenantID string,
	p topupPaidPosting) (string, error) {

	specs, err := buildTopupPaidEntrySpecs(p.Amount, p.FeeAmount)
	if err != nil {
		return "", err
	}

	entries := make([]Entry, 0, len(specs))
	for _, spec := range specs {
		var accountID string
		switch spec.AccountType {
		case AccountChannelCash, AccountPlatformFeeExpense:
			accountID, err = EnsureAccount(ctx, tx, tenantID, spec.AccountType,
				p.Currency, nil, p.ProviderCode)
		case AccountUserBalance:
			accountID, err = EnsureAccount(ctx, tx, tenantID, spec.AccountType,
				p.Currency, &p.UserID, "")
		default:
			return "", errors.New("充值分录包含未知科目")
		}
		if err != nil {
			return "", err
		}
		entries = append(entries, Entry{
			AccountID: accountID, Direction: spec.Direction,
			Amount: spec.Amount, Description: spec.Description,
		})
	}

	orderID := p.OrderID
	return Post(ctx, tx, tenantID, Posting{
		Kind: "balance_topup", Currency: p.Currency,
		SourceType: "order", SourceID: &orderID,
		Memo: "余额充值", ActorKind: "system", Entries: entries,
	})
}

// AdjustBalanceInput 是管理员手动调账的入参。
type AdjustBalanceInput struct {
	UserID   string
	Amount   int64 // 正数增加，负数扣减
	Currency string
	Reason   string
	ActorID  string
}

// AdjustBalance 由管理员直接增减用户余额。
//
// 强制填理由，而且理由会进审计。手动调账是最容易被滥用的一个口子 ——
// 能凭空给账户加钱的功能，必须每一次都说得清是谁、为什么。
func (s *Service) AdjustBalance(ctx context.Context, tenantID string,
	in AdjustBalanceInput) (int64, error) {

	if in.Amount == 0 {
		return 0, httpx.New(httpx.CodeValidationFailed, "调整金额不能为零")
	}
	if len([]rune(in.Reason)) < 5 {
		return 0, httpx.Invalid(map[string]string{
			"reason": "请写清调整原因，至少 5 个字"})
	}
	currency := in.Currency
	if currency == "" {
		currency = "CNY"
	}

	var after int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		balAcct, err := EnsureAccount(ctx, tx, tenantID, AccountUserBalance,
			currency, &in.UserID, "")
		if err != nil {
			return err
		}
		// 锁住账户再算：并发调账时后一笔要看到前一笔的结果，
		// 否则两个管理员同时扣款会各自基于同一个旧余额判断「够扣」
		avail, err := LockAccountForUpdate(ctx, tx, balAcct)
		if err != nil {
			return err
		}
		if in.Amount < 0 && avail < -in.Amount {
			return httpx.New(httpx.CodeConflict, "余额不足，无法扣减这么多")
		}

		// 调账的对手方记在暂记科目：这笔钱不是从支付渠道来的，
		// 挂在渠道资金上会让渠道对账凭空多出一笔对不上的数
		suspense, err := EnsureAccount(ctx, tx, tenantID, AccountSuspense,
			currency, nil, "manual_adjust")
		if err != nil {
			return err
		}

		amt := in.Amount
		entries := []Entry{}
		if amt > 0 {
			entries = append(entries,
				Entry{AccountID: suspense, Direction: Debit, Amount: amt,
					Description: "人工调账"},
				Entry{AccountID: balAcct, Direction: Credit, Amount: amt,
					Description: "余额增加"})
		} else {
			entries = append(entries,
				Entry{AccountID: balAcct, Direction: Debit, Amount: -amt,
					Description: "余额扣减"},
				Entry{AccountID: suspense, Direction: Credit, Amount: -amt,
					Description: "人工调账"})
		}

		userID := in.UserID
		txnID, err := Post(ctx, tx, tenantID, Posting{
			Kind: "balance_adjusted", Currency: currency,
			SourceType: "user", SourceID: &userID,
			Memo: in.Reason, ActorKind: "admin",
			ActorID: nullStr(in.ActorID), Entries: entries,
		})
		if err != nil {
			return err
		}

		after, err = Balance(ctx, tx, balAcct)
		if err != nil {
			return err
		}

		var actorID *string
		if in.ActorID != "" {
			v := in.ActorID
			actorID = &v
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "balance.adjusted", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"amount": in.Amount, "currency": currency,
				"reason": in.Reason, "balance_after": after, "ledger_txn": txnID,
			},
		})
	})
	return after, err
}

// BalanceOf 按指定币种读一个用户的当前余额。
func (s *Service) BalanceOf(ctx context.Context, tenantID, userID, currency string) (int64, error) {
	var amount int64
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		// 直接读账户上的物化余额，不去累加分录 ——
		// 分录只增不减，用它算余额会随着交易量线性变慢
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(-balance_signed, 0)
			  FROM ledger_accounts
			 WHERE tenant_id = $1 AND owner_user_id = $2::uuid
			   AND account_type = 'user_balance'
			   AND currency = $3`, tenantID, userID, currency).Scan(&amount)
		// 还没有账户就是还没有过任何余额往来，余额自然是 0，不是错误
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return amount, err
}

// ListBalanceHistory 返回余额变动明细。
func (s *Service) ListBalanceHistory(ctx context.Context, tenantID, userID string) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT t.kind, e.direction, e.amount, COALESCE(t.memo, ''), t.occurred_at
			  FROM ledger_entries e
			  JOIN ledger_transactions t ON t.id = e.transaction_id
			  JOIN ledger_accounts a ON a.id = e.account_id
			 WHERE a.tenant_id = $1 AND a.owner_user_id = $2::uuid
			   AND a.account_type = 'user_balance'
			 ORDER BY e.created_at DESC LIMIT 100`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, direction, memo string
			var amount int64
			var at any
			if err := rows.Scan(&kind, &direction, &amount, &memo, &at); err != nil {
				return err
			}
			// 余额是负债科目：贷方增加、借方减少。
			// 直接把方向翻译成正负，前端不该被迫理解会计方向
			delta := amount
			if direction == "debit" {
				delta = -amount
			}
			out = append(out, map[string]any{
				"kind": kind, "delta": delta, "memo": memo, "at": at,
			})
		}
		return rows.Err()
	})
	return out, err
}
