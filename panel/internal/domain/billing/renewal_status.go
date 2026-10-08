package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// subscriptionBoundOrderKind 是挂在已有订阅上、履约时原地改那条订阅的订单：
// 续费与变更套餐。它们的结算与创建都先锁订阅，再碰预留图与账本。
func subscriptionBoundOrderKind(kind string) bool {
	return kind == "renewal" || kind == "upgrade"
}

// subscriptionAcceptsPaidChange 是续费与变更套餐对订阅状态的唯一口径：建单时
// 校验一次，结算时锁住订阅后再校验一次——支付窗口里订阅可能被改成终态，履约
// 写 active 会被状态机拒绝，钱于是改走挂账（R117）。迁移 00124 的挂账守卫写着
// 同一组状态，renewal_contract_test.go 钉住两边一致。
//
// 生效中的四种状态之外，已过期但原地续费窗口没关（renewal_closed_at 为空，过期
// 不满 30 天）的订阅也收：续费、改套餐都在原订阅上做，链接不变（规则 4）。
// 窗口关了只能新购。已取消、暂停、待开通一律不收。
func subscriptionAcceptsPaidChange(status string, renewalClosed bool) bool {
	switch status {
	case "active", "trialing", "grace", "past_due":
		return true
	case "expired":
		return !renewalClosed
	}
	return false
}

// ErrRenewalWindowClosed 是过期超过原地续费窗口的订阅再来续费时的 409。
var ErrRenewalWindowClosed = httpx.New(httpx.CodeConflict,
	"这条订阅已过期超过 30 天，不能再原地续费，请重新购买（会换新的订阅链接）")

// lockOrderSubscriptionForSettlement establishes the shared lock order for
// renewal and plan change settlement:
// order -> subscription -> payment intents -> reservation graph -> ledger.
// The caller has already locked the order before entering this helper. The
// returned status and renewal-window flag are read under the lock, for the
// settlement-time recheck (subscriptionAcceptsPaidChange).
func lockOrderSubscriptionForSettlement(ctx context.Context, tx pgx.Tx,
	tenantID, orderID, userID string) (string, string, bool, error) {
	var subID string
	if err := tx.QueryRow(ctx, `
		SELECT subscription_id::text FROM orders
		 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		   AND kind IN ('renewal','upgrade')`,
		tenantID, orderID, userID).Scan(&subID); err != nil {
		return "", "", false, fmt.Errorf("读取订单关联的订阅: %w", err)
	}
	if subID == "" {
		return "", "", false, errors.New("续费或变更订单没有关联订阅")
	}
	var lockedID, status string
	var closed bool
	if err := tx.QueryRow(ctx, `
		SELECT id::text, status, renewal_closed_at IS NOT NULL FROM subscriptions
		 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		 FOR UPDATE`, tenantID, subID, userID).Scan(&lockedID, &status, &closed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, httpx.NotFoundOrForbidden()
		}
		return "", "", false, err
	}
	return lockedID, status, closed, nil
}
