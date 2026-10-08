package billing

// 变更套餐的履约：把一条订阅原地换成另一个套餐。
//
// 两个来源共用 applyPlanChangeTx 这一份实现（不另写一套）：
//
//	变更单   fulfillPlanChangeLocked：门户改套餐、后台人工开单遇到不同套餐（D-E-2、2026-10-07）
//	套餐卡   grantPlanChange（plan_change_grant.go）：礼品卡套餐卡遇到不同套餐（2026-10-07）
//
// 两者的周期、配额、凭据、退余额与订阅事件完全一样；区别只在钱从哪来：变更单的剩余价值
// 与新价都冻结在订单上，套餐卡本身算 0 元，原套餐的剩余价值全额退进余额。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// planChangeApply 是一次原地换套餐落到订阅上所需的全部输入。
type planChangeApply struct {
	TenantID      string
	SubID         string
	UserID        string
	PlanID        string
	PlanVersionID string
	PriceID       *string
	Interval      string
	IntervalCount int16
	// SnapshotCurrency / SnapshotAmount 写进订阅快照（新套餐的价格）
	SnapshotCurrency string
	SnapshotAmount   int64
	// ProrationCredit 是原订阅的剩余价值；Refund 是其中退进余额的部分，币种 RefundCurrency
	ProrationCredit int64
	Refund          int64
	RefundCurrency  string
	// 来源二选一：OrderID（变更单）或 GiftCodeID（套餐卡的卡密）。退余额分录与订阅事件
	// 据此挂到订单或卡密上，迁移 00071 / 00134 的证据守卫按同一来源核对。
	OrderID    string
	GiftCodeID string
	ActorKind  string
	ActorID    *string
}

// applyPlanChangeTx 把订阅换成新套餐：周期从现在起按新套餐重开，配额按新套餐版本
// 重置，凭据不换、只跟到新周期末，剩余价值抵不完的部分退进余额，写一条 plan_changed
// 事件。调用方已锁住订阅行。返回新周期末。
func applyPlanChangeTx(ctx context.Context, tx pgx.Tx, a planChangeApply) (time.Time, error) {
	if (a.OrderID == "") == (a.GiftCodeID == "") {
		return time.Time{}, errors.New("plan change must come from exactly one order or gift card code")
	}
	tenantID, subID, userID := a.TenantID, a.SubID, a.UserID

	var fromPlanID, fromVersionID, oldStatus string
	var oldEnd *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT plan_id::text, plan_version_id::text, status, current_period_end
		  FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`,
		tenantID, subID, userID).Scan(&fromPlanID, &fromVersionID, &oldStatus,
		&oldEnd); err != nil {
		return time.Time{}, err
	}

	now := time.Now().UTC()
	newEnd := addInterval(now, a.Interval, int(a.IntervalCount))

	// 配额按新套餐重新起算，清零前的用量先留日志。配额行不能删：人工调整记录
	// （00006 的配额调整表）挂在行上且只许追加（DATA-003）。新套餐没有的指标把
	// 上限置空 —— 上限为空在节点下发与扣量里本来就是「不限量」，与新开一条
	// 该套餐的订阅（没有这一行）效果相同。
	type usedQuota struct {
		metric   string
		consumed int64
	}
	var before []usedQuota
	rows, err := tx.Query(ctx, `
		SELECT metric, consumed FROM quota_balances
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid ORDER BY id`, tenantID, subID)
	if err != nil {
		return time.Time{}, err
	}
	for rows.Next() {
		var u usedQuota
		if err := rows.Scan(&u.metric, &u.consumed); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		before = append(before, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET status = 'active', plan_id = $3::uuid, plan_version_id = $4::uuid,
		       price_id = $5::uuid, current_period_start = $6, current_period_end = $7,
		       snapshot_currency = $8, snapshot_amount = $9, grace_end = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`,
		tenantID, subID, a.PlanID, a.PlanVersionID, a.PriceID, now, newEnd,
		a.SnapshotCurrency, a.SnapshotAmount); err != nil {
		return time.Time{}, fmt.Errorf("变更订阅套餐: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE quota_balances qb
		   SET limit_value = qd.limit_value,
		       granted = coalesce(qd.limit_value, 0),
		       consumed = 0,
		       period_start = $4,
		       period_end = `+quotaPeriodEndSQL("qb.period", "$4::timestamptz", "$5::timestamptz")+`,
		       notified_thresholds = '{}',
		       overage_applied_at = NULL,
		       updated_at = now()
		  FROM quota_balances cur
		  LEFT JOIN quota_definitions qd
		    ON qd.plan_version_id = $3::uuid
		   AND qd.metric = cur.metric AND qd.period = cur.period
		 WHERE qb.id = cur.id
		   AND cur.tenant_id = $1 AND cur.subscription_id = $2::uuid`,
		tenantID, subID, a.PlanVersionID, now, newEnd); err != nil {
		return time.Time{}, fmt.Errorf("按新套餐重置配额: %w", err)
	}
	// 新套餐多出来的指标补上配额行（已有的在上面改过，这里跳过）。
	if err := initQuotaBalances(ctx, tx, tenantID, subID, a.PlanVersionID, now, newEnd); err != nil {
		return time.Time{}, err
	}
	for _, u := range before {
		if err := LogTrafficReset(ctx, tx, tenantID, subID, userID, u.metric,
			"plan_change", u.consumed, nil, ""); err != nil {
			return time.Time{}, fmt.Errorf("记录变更重置: %w", err)
		}
	}
	// 凭据不换，只跟着新周期走（与续费、礼品卡、后台加时长同一个函数）。
	if _, err := syncCredentialExpiryTx(ctx, tx, tenantID, subID, newEnd); err != nil {
		return time.Time{}, err
	}

	// 剩余价值抵完新价还有余，差额退进余额。那部分钱此前已记成平台收入，
	// 这里冲回收入、记成欠用户的余额（负债），不是凭空造钱。
	if a.Refund > 0 {
		sourceType, sourceID := "order", a.OrderID
		if a.GiftCodeID != "" {
			sourceType, sourceID = "gift_card_code", a.GiftCodeID
		}
		accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, []ledgerAccountSpec{
			{Key: "revenue", AccountType: AccountPlatformRevenue, Currency: a.RefundCurrency, OwnerRef: "main"},
			{Key: "balance", AccountType: AccountUserBalance, Currency: a.RefundCurrency, UserID: &userID},
		})
		if err != nil {
			return time.Time{}, err
		}
		if _, err := Post(ctx, tx, tenantID, Posting{
			Kind: "plan_change_refund", Currency: a.RefundCurrency,
			SourceType: sourceType, SourceID: &sourceID,
			Memo: "plan change remaining value to balance", ActorKind: "system",
			Entries: []Entry{
				{AccountID: accounts["revenue"], Direction: Debit, Amount: a.Refund,
					Description: "reverse unused plan revenue"},
				{AccountID: accounts["balance"], Direction: Credit, Amount: a.Refund,
					Description: "plan change refund to balance"},
			},
		}); err != nil {
			return time.Time{}, err
		}
	}

	payload := map[string]any{
		"from_plan_id": fromPlanID, "from_plan_version_id": fromVersionID,
		"to_plan_id": a.PlanID, "to_plan_version_id": a.PlanVersionID,
		"previous_end": oldEnd, "period_end": newEnd,
		"proration_credit": a.ProrationCredit, "balance_refund": a.Refund,
	}
	if a.GiftCodeID != "" {
		// 00134 的证据守卫按这两项找卡密与兑换流水，并核对退余额分录的金额与币种
		payload["source"] = "gift_card"
		payload["gift_card_code_id"] = a.GiftCodeID
		payload["refund_currency"] = a.RefundCurrency
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status,
			 actor_kind, actor_id, order_id, payload)
		VALUES ($1, $2::uuid, 'plan_changed', $3, 'active', $4, $5::uuid, $6::uuid, $7)`,
		tenantID, subID, oldStatus, a.ActorKind, a.ActorID, nullIfEmpty(a.OrderID),
		payload); err != nil {
		return time.Time{}, err
	}
	return newEnd, nil
}

// fulfillPlanChangeLocked 把已支付的变更单落到订阅上。调用方已按
// 订单 → 订阅 的顺序锁住两者，并已把订单转到 paid。
func (s *Service) fulfillPlanChangeLocked(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, userID, subID string) (string, error) {

	var (
		orderSubID, currency                string
		subtotal, discount, prorationCredit int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(subscription_id::text, ''), currency::text,
		       subtotal_amount, discount_amount, proration_credit_amount
		  FROM orders
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
		   AND kind = 'upgrade' AND status = 'paid'`,
		tenantID, orderID, userID).Scan(&orderSubID, &currency, &subtotal, &discount,
		&prorationCredit); err != nil {
		return "", fmt.Errorf("读取变更订单: %w", err)
	}
	if subID == "" || orderSubID != subID {
		return "", errors.New("plan change order is not bound to the locked subscription")
	}

	a := planChangeApply{
		TenantID: tenantID, SubID: subID, UserID: userID,
		SnapshotCurrency: currency, ProrationCredit: prorationCredit,
		// 降级：剩余价值抵完新价（优惠后）还有余，差额退进余额
		Refund:         max(prorationCredit-(subtotal-discount), 0),
		RefundCurrency: currency, OrderID: orderID, ActorKind: "payment",
	}
	if err := tx.QueryRow(ctx, `
		SELECT plan_id::text, plan_version_id::text, price_id::text,
		       snapshot_interval, snapshot_interval_count, unit_amount
		  FROM order_items
		 WHERE tenant_id = $1 AND order_id = $2::uuid AND plan_id IS NOT NULL`,
		tenantID, orderID).Scan(&a.PlanID, &a.PlanVersionID, &a.PriceID, &a.Interval,
		&a.IntervalCount, &a.SnapshotAmount); err != nil {
		return "", fmt.Errorf("读取变更订单行: %w", err)
	}
	if _, err := applyPlanChangeTx(ctx, tx, a); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'paid'`, tenantID, orderID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("plan change fulfilment transition lost")
	}
	return subID, nil
}
