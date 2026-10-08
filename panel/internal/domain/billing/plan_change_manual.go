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
// 「原订阅」由人选（购买模型统一，2026-10-07）：后台开单带 Target、套餐卡带 choice，
// 选项与默认值只由 purchase.Options 给出（placement_candidates.go），不再由系统自动挑一条。

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
