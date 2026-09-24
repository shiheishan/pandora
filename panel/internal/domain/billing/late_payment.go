// [INPUT]: 依赖 ledger.go 的 late_payment_suspense 与 user_balance 科目记账，依赖 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 LatePaymentCase、ListLatePayments（待处理合计按币种分开）、ApplyLatePaymentToBalance 及其输入类型
// [POS]: billing 的挂账出口：unexpected_payment.go 负责写入 case，这里负责查看与转入余额，被 api/admin/late_payment.go 消费
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 挂账（late_payment_cases）的查看与处理。
//
// 写入路径早就有了：用户取消订单之后支付才到账，quarantineUnexpectedPayment
// 会把钱记进 late_payment_suspense 科目并建一条 case。账是平的、钱没丢，
// 但在此之前没有任何 SELECT、没有路由、没有界面 —— 写进去就再也没人看得见。
// 管理员不知道有笔待处理的钱，用户问「我付了怎么没到账」也查不到。
//
// 参考过 Xboard 的做法：它的回调里是
//     if ($order->status !== Order::STATUS_PENDING) return true;
// 直接丢掉，连记录都没有。所以这不是照搬谁的功能，是把我们自己
// 已经在产生的数据接上出口。
//
// 处理方式只做一种：转入用户余额。原路退回渠道不做 —— 那需要对接各渠道的
// 退款 API 和对账，按当前的运营方式走工单人工处理更实际。

type LatePaymentCase struct {
	ID          string     `json:"id"`
	CaseKind    string     `json:"case_kind"`
	Status      string     `json:"status"`
	Amount      int64      `json:"amount"`
	Currency    string     `json:"currency"`
	OrderNo     string     `json:"order_no"`
	OrderStatus string     `json:"order_status"`
	UserID      string     `json:"user_id"`
	UserEmail   string     `json:"user_email"`
	ReceivedAt  time.Time  `json:"received_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Resolution  *string    `json:"resolution_reason,omitempty"`
}

type ListLatePaymentsInput struct {
	Status string
	Limit  int
	Offset int
}

// ListLatePayments 默认只列未处理的：管理员打开这一页是为了「有没有要处理的钱」，
// 把历史已处理的混在一起会让待办淹没在流水里。
//
// 待处理合计按币种分开返回（币种 → 最小单位金额）：CNY 的分与 USD 的美分
// 不能相加，此前直接 sum(amount) 得出的是一个没有单位的数（缺陷 13）。
func (s *Service) ListLatePayments(ctx context.Context, tenantID string,
	in ListLatePaymentsInput) ([]LatePaymentCase, int64, map[string]int64, error) {

	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 25
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	status := strings.TrimSpace(in.Status)
	switch status {
	case "", "suspense", "applied", "refunded", "manual_review", "refund_pending":
	default:
		return nil, 0, nil, httpx.New(httpx.CodeBadRequest, "不支持的挂账状态")
	}

	out := []LatePaymentCase{}
	var total int64
	pendingAmounts := map[string]int64{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM late_payment_cases
			 WHERE tenant_id = $1 AND ($2 = '' OR status = $2)`,
			tenantID, status).Scan(&total); err != nil {
			return err
		}
		// 待处理总额单独算：这是这一页最该一眼看到的数字。
		sums, err := tx.Query(ctx, `
			SELECT currency::text, sum(amount)::bigint
			  FROM late_payment_cases
			 WHERE tenant_id = $1 AND status = 'suspense'
			 GROUP BY currency`, tenantID)
		if err != nil {
			return err
		}
		for sums.Next() {
			var currency string
			var amount int64
			if err := sums.Scan(&currency, &amount); err != nil {
				sums.Close()
				return err
			}
			pendingAmounts[currency] = amount
		}
		sums.Close()
		if err := sums.Err(); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT c.id::text, c.case_kind, c.status, c.amount, c.currency::text,
			       o.order_no, o.status, o.user_id::text, u.email::text,
			       c.received_at, c.resolved_at, c.resolution_reason
			  FROM late_payment_cases c
			  JOIN orders o ON o.tenant_id = c.tenant_id AND o.id = c.order_id
			  JOIN users u ON u.tenant_id = c.tenant_id AND u.id = o.user_id
			 WHERE c.tenant_id = $1 AND ($2 = '' OR c.status = $2)
			 ORDER BY (c.status = 'suspense') DESC, c.received_at DESC
			 LIMIT $3 OFFSET $4`, tenantID, status, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c LatePaymentCase
			if err := rows.Scan(&c.ID, &c.CaseKind, &c.Status, &c.Amount, &c.Currency,
				&c.OrderNo, &c.OrderStatus, &c.UserID, &c.UserEmail,
				&c.ReceivedAt, &c.ResolvedAt, &c.Resolution); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return out, total, pendingAmounts, nil
}

type ApplyLatePaymentInput struct {
	CaseID  string
	ActorID string
	Reason  string
}

// ApplyLatePaymentToBalance 把挂账的钱转进用户余额。
//
// 账本动作：借 late_payment_suspense（冲掉挂账）/ 贷 user_balance（用户可用）。
// 整个过程在一个事务里，且用 InTxSerializable —— 两个管理员同时点「转入余额」
// 时不能把同一笔钱记两遍。
func (s *Service) ApplyLatePaymentToBalance(ctx context.Context, tenantID string,
	in ApplyLatePaymentInput) (string, error) {

	in.Reason = strings.TrimSpace(in.Reason)
	if tenantID == "" || in.CaseID == "" || in.ActorID == "" {
		return "", httpx.New(httpx.CodeBadRequest, "tenant, actor and case are required")
	}
	if _, err := uuid.Parse(in.CaseID); err != nil {
		return "", httpx.NotFoundOrForbidden()
	}
	if _, err := uuid.Parse(in.ActorID); err != nil {
		return "", httpx.New(httpx.CodeBadRequest, "actor identifier is invalid")
	}
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return "", httpx.Invalid(map[string]string{
			"reason": "请写清处理原因，5 到 500 个字"})
	}

	actorID := in.ActorID
	var txnID string
	err := s.pool.InTxSerializable(ctx, dbScope(tenantID, actorID), func(tx pgx.Tx) error {
		var amount int64
		var currency, status, userID, orderID string
		err := tx.QueryRow(ctx, `
			SELECT c.amount, c.currency::text, c.status, o.user_id::text, o.id::text
			  FROM late_payment_cases c
			  JOIN orders o ON o.tenant_id = c.tenant_id AND o.id = c.order_id
			 WHERE c.tenant_id = $1 AND c.id = $2::uuid
			 FOR UPDATE OF c`, tenantID, in.CaseID).
			Scan(&amount, &currency, &status, &userID, &orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if status != "suspense" {
			return httpx.New(httpx.CodeConflict, "这笔挂账已经处理过了")
		}

		accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, []ledgerAccountSpec{
			{Key: "suspense", AccountType: AccountLatePaymentSuspense,
				Currency: currency, OwnerRef: "main"},
			{Key: "balance", AccountType: AccountUserBalance,
				Currency: currency, UserID: &userID},
		})
		if err != nil {
			return err
		}

		txnID, err = Post(ctx, tx, tenantID, Posting{
			Kind: "late_payment_applied", Currency: currency,
			SourceType: "order", SourceID: &orderID,
			Memo: in.Reason, ActorKind: "admin", ActorID: &actorID,
			Entries: []Entry{
				{AccountID: accounts["suspense"], Direction: Debit, Amount: amount,
					Description: "release late payment from suspense"},
				{AccountID: accounts["balance"], Direction: Credit, Amount: amount,
					Description: "credit late payment to user balance"},
			},
		})
		if err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE late_payment_cases
			   SET status = 'applied', resolved_at = now(),
			       resolution_reason = $3, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'suspense'`,
			tenantID, in.CaseID, in.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("late payment case transition lost")
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "late_payment.applied", ResourceType: "late_payment_case",
			ResourceID:   &in.CaseID,
			BeforeDigest: map[string]any{"status": "suspense"},
			AfterDigest: map[string]any{
				"status": "applied", "amount": amount, "currency": currency,
				"user_id": userID, "reason": in.Reason, "ledger_txn": txnID,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return "", err
	}
	return txnID, nil
}
