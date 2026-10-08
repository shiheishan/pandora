package billing

// 换套餐的三个入口（用户 2026-10-07 定）：
//
//	门户改套餐   用户在原订阅上选新套餐（CreatePlanChange）
//	后台人工开单 用户已有别的套餐的订阅时，在原订阅上开一张变更单（CreateManualOrder →
//	             CreatePlanChange 带 Manual* 字段），不再新开订阅、换链接
//	套餐卡       用户已有别的套餐的订阅时，在原订阅上换套餐（GiftGranter.GrantPlan →
//	             grantPlanChange，plan_change_grant.go）
//
// 三者共用同一份折算（plan_change_quote.go）与同一份履约（applyPlanChangeTx）：
// 原套餐还没用完的已付费剩余价值先抵新套餐的价格，抵不完的退进余额。赠送单、套餐卡
// 本身算 0 元，所以原套餐的剩余价值全额退进余额。订阅链接不变，用户在客户端刷新一次
// 订阅就换到新套餐。
//
// 「原订阅」怎么选：与同套餐只续不新开（same_plan.go）同一个次序，推广到任意套餐——
// 可以原地续费或变更的订阅里（subscriptionAcceptsPaidChange：生效中，或过期 30 天内、
// 窗口没关），生效中的优先，其次到期最晚，再次最新创建。已取消的不放开。同套餐的
// 订阅优先走续费（调用方先查同套餐）。

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// planChangeTargetSubscription 取用户可以原地换套餐的那条订阅，没有返回空串。
// 次序见文件头；是否可换只经 subscriptionAcceptsPaidChange 判断。
// lock 为真时锁住选中的那一行并复核（调用方随后要在它上面换套餐）。
func planChangeTargetSubscription(ctx context.Context, tx pgx.Tx,
	tenantID, userID string, lock bool) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, status, renewal_closed_at IS NOT NULL
		  FROM subscriptions
		 WHERE tenant_id = $1 AND user_id = $2::uuid
		 ORDER BY (status <> 'expired') DESC, current_period_end DESC NULLS LAST,
		          created_at DESC`, tenantID, userID)
	if err != nil {
		return "", err
	}
	subID := ""
	for rows.Next() {
		var id, status string
		var closed bool
		if err := rows.Scan(&id, &status, &closed); err != nil {
			rows.Close()
			return "", err
		}
		if subID == "" && subscriptionAcceptsPaidChange(status, closed) {
			subID = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || subID == "" || !lock {
		return subID, err
	}
	// 锁住后复核：读和锁之间状态可能刚变
	var status string
	var closed bool
	if err := tx.QueryRow(ctx, `
		SELECT status, renewal_closed_at IS NOT NULL FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, subID).Scan(&status, &closed); err != nil {
		return "", err
	}
	if !subscriptionAcceptsPaidChange(status, closed) {
		return "", nil
	}
	return subID, nil
}

// PlanChangeSubscription 返回用户可以原地换套餐的订阅 ID（没有为空串）。后台人工开单在
// 没有同套餐订阅时据此决定开变更单还是新购单；只读，不加锁（quotePlanChange 会锁住复核）。
func (s *Service) PlanChangeSubscription(ctx context.Context, tenantID, userID string) (string, error) {
	var subID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		subID, err = planChangeTargetSubscription(ctx, tx, tenantID, userID, false)
		return err
	})
	return subID, err
}

// waiveNewPrice 把新价全额减免（人工赠送）：折扣等于小计，剩余价值于是全部退进余额。
// 折算本身（剩余价值）不动。人工单不带优惠券。
func (q *planChangeQuote) waiveNewPrice() {
	q.Coupon, q.CouponFace = nil, nil
	q.Discount = q.Subtotal
	q.Total = orderTotal(q.Subtotal, q.Discount, q.ProrationCredit, 0)
	q.BalanceRefund = max(q.ProrationCredit-(q.Subtotal-q.Discount), 0)
	q.Direction = "downgrade"
}

// settleManualOfflineTx 在建单事务里按线下渠道结清一张人工开的单（与标记已支付同一条
// settlePaymentTx），返回结算结果与结清后的订单状态。钱进了挂账就整笔回滚、回 409。
func (s *Service) settleManualOfflineTx(ctx context.Context, tx pgx.Tx, tenantID, orderID,
	currency string, payable int64, actorID string, receipt OfflineReceipt) (*PaymentWebhookOutput, string, error) {
	settled := &PaymentWebhookOutput{}
	if err := s.settlePaymentTx(ctx, tx, tenantID, offlinePaymentInput(
		orderID, currency, payable, actorID, receipt), settled); err != nil {
		return nil, "", err
	}
	if !settled.Processed || settled.AlreadyHandled || settled.PaymentID == "" {
		return nil, "", errors.New("offline settlement of a new manual order did not capture")
	}
	if settled.QuarantineKind != "" {
		return nil, "", markPaidQuarantined(settled.QuarantineKind)
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM orders
		WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, orderID).Scan(&status); err != nil {
		return nil, "", err
	}
	return settled, status, nil
}

// manualPlanChangeAudit 是后台人工开单落成变更单时的审计（与订单同一事务）。
func manualPlanChangeAudit(in PlanChangeInput, out PlanChangeOrderOutput, q *planChangeQuote) audit.Entry {
	actor := in.ManualActor
	orderID := out.OrderID
	digest := map[string]any{
		"order_no": out.OrderNo, "user_id": in.UserID,
		"subscription_id": in.SubscriptionID, "plan_change": true,
		"plan_id": in.PlanID, "direction": q.Direction,
		"reason": in.ManualReason, "status": out.Status,
		"settlement": in.ManualSettlement,
		"subtotal":   q.Subtotal, "granted": out.DiscountAmount,
		"proration_credit": q.ProrationCredit, "balance_refund": q.BalanceRefund,
		"total": q.Total, "currency": q.Currency,
	}
	if out.settlement != nil {
		digest["reference"] = in.Offline.Reference
		digest["amount"] = out.PayableAmount
		digest["payment_id"] = out.settlement.PaymentID
		digest["ledger_txn"] = out.settlement.LedgerTxnID
	}
	return audit.Entry{
		ActorKind: "admin", ActorID: &actor,
		Action: "order.manual_created", ResourceType: "order",
		ResourceID: &orderID, AfterDigest: digest,
		APIDomain: "admin",
	}
}

// ErrPlanChangeGiftOrderOpen 是套餐卡要换套餐的订阅上还挂着未完结的续费或变更单时的 409：
// 那张单冻结的剩余价值会因为这次换套餐失效。
var ErrPlanChangeGiftOrderOpen = httpx.New(httpx.CodeConflict,
	"你的订阅还有未完成的续费或变更套餐订单，请先支付或取消后再兑换")
