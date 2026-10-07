package billing

// 续费履约：付费续费（订单）与套餐卡同套餐续费（礼品卡，无订单）共用 renewSubscriptionTx。
//
// 规则（用户 2026-10-07 定）：
//
//	基准时刻   付款时刻（订单 paid_at，与结算同一事务的数据库时刻），不是履约代码跑到的时刻
//	提前续费   付款时还没到期：新周期接在原到期日之后（规则 5）。本期配额一行都不动，
//	           剩余流量照常用到原到期日；到点由 RollQuotaPeriods 把 cycle 行滚进新周期
//	回调晚到   订单在原到期日之前创建、回调在到期之后才到（支付窗口 30 分钟）：按付款发生在
//	           到期前处理，同样接在原到期日之后，不算过期恢复
//	过期恢复   其余付款时已经到期的：从付款时刻起算一整期；total / cycle / day / month 四种
//	           配额一起清零，周期边界按 quotaPeriodEndSQL 对齐到付款时刻（day / month 是自然
//	           周期，从付款时刻起一天 / 一个月），只写一次 renewal 重置日志，不逐期补算；
//	           续费事件的起始状态记 expired（状态列还停在 active、过期扫描没赶上的也一样）
//	凭据       不动，只把有效期跟到新周期末（syncCredentialExpiryTx）：链接不变
//	流量包     挂在用户身上，不碰（D-E-1）

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// renewalGrant 是一次续费要延长的周期与快照。
type renewalGrant struct {
	TenantID       string
	UserID         string
	SubscriptionID string
	Interval       string
	IntervalCount  int16
	PlanVersionID  string
	PriceID        *string
	// PaidAt 是付款时刻；OrderCreatedAt 可空（礼品卡没有订单），用来认出「回调晚到」
	PaidAt         time.Time
	OrderCreatedAt *time.Time
	// ActorKind / OrderID / Source 写进 renewed 事件
	ActorKind string
	OrderID   *string
	Source    string
}

// renewalOutcome 是一次续费的结果。
type renewalOutcome struct {
	FromStatus  string
	Restarted   bool
	PreviousEnd *time.Time
	PeriodEnd   time.Time
}

// renewalBase 决定新周期从哪里接：返回基准时刻与是否「过期恢复」（重开周期）。
//
// oldEnd 为空（永不过期的订阅）按过期恢复处理：从付款时刻起算一整期，与原先一致。
func renewalBase(oldEnd *time.Time, paidAt time.Time, orderCreatedAt *time.Time) (time.Time, bool) {
	if oldEnd == nil {
		return paidAt, true
	}
	if oldEnd.After(paidAt) {
		return *oldEnd, false // 提前续费
	}
	if orderCreatedAt != nil && orderCreatedAt.Before(*oldEnd) {
		return *oldEnd, false // 到期前下的单、回调到期后才到：仍算提前续费
	}
	return paidAt, true
}

// renewSubscriptionTx 在调用方已锁住的订阅上续一期。调用方负责校验订阅状态
// （subscriptionAcceptsPaidChange）并持有订阅行锁。
func renewSubscriptionTx(ctx context.Context, tx pgx.Tx, g renewalGrant) (renewalOutcome, error) {
	var out renewalOutcome
	var oldStatus string
	if err := tx.QueryRow(ctx, `
		SELECT current_period_end, status FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`,
		g.TenantID, g.SubscriptionID, g.UserID).Scan(&out.PreviousEnd, &oldStatus); err != nil {
		return out, err
	}

	paidAt := g.PaidAt.UTC().Truncate(time.Microsecond)
	base, restart := renewalBase(out.PreviousEnd, paidAt, g.OrderCreatedAt)
	out.Restarted = restart
	out.PeriodEnd = addInterval(base, g.Interval, int(g.IntervalCount)).Truncate(time.Microsecond)
	out.FromStatus = oldStatus
	if restart && out.PreviousEnd != nil {
		// 状态列可能还停在 active（过期扫描没赶上）：事件按事实记 expired，变更套餐的
		// 折算靠它认出周期重开（plan_change_quote.go）
		out.FromStatus = "expired"
	}

	// expired -> active 由 00124 放开；grace / past_due 回到 active 时周期起点也重置
	if _, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET status = 'active',
		       current_period_start = CASE WHEN $7 OR status IN ('past_due','grace')
		                                   THEN $3 ELSE current_period_start END,
		       current_period_end = $4,
		       plan_version_id = $5::uuid,
		       price_id = COALESCE($6::uuid, price_id),
		       grace_end = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`,
		g.TenantID, g.SubscriptionID, paidAt, out.PeriodEnd, g.PlanVersionID, g.PriceID,
		restart); err != nil {
		return out, fmt.Errorf("延长订阅周期: %w", err)
	}

	if restart {
		if err := restartQuotaPeriodsTx(ctx, tx, g, paidAt, out.PeriodEnd); err != nil {
			return out, err
		}
		// 上限按套餐版本写回：救回时折算加进来的那部分（subscription_rescue.go）只属于
		// 上一个周期。metric 在同一版本下允许同时存在 cycle/day/month/total，必须把
		// period 也作为连接键；只按 metric 会让 PostgreSQL 从多行中任取一条。
		//
		// 提前续费不动上限：续费沿用订阅自己的套餐版本，上限本来不变，本期折算出的
		// 额度要留到原到期日；新周期的上限由 RollQuotaPeriods 滚动时写回。
		if _, err := tx.Exec(ctx, `
			UPDATE quota_balances qb
			   SET limit_value = qd.limit_value,
			       granted = COALESCE(qd.limit_value, 0)
			  FROM quota_definitions qd
			 WHERE qb.tenant_id = $1 AND qb.subscription_id = $2::uuid
			   AND qd.plan_version_id = $3::uuid
			   AND qd.metric = qb.metric AND qd.period = qb.period`,
			g.TenantID, g.SubscriptionID, g.PlanVersionID); err != nil {
			return out, fmt.Errorf("更新配额上限: %w", err)
		}
	}

	// 凭据有效期跟着周期走（token 不换），与礼品卡、后台加时长同一个函数
	if _, err := syncCredentialExpiryTx(ctx, tx, g.TenantID, g.SubscriptionID, out.PeriodEnd); err != nil {
		return out, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status,
			 actor_kind, order_id, payload)
		VALUES ($1,$2::uuid,'renewed',$3,'active',$4,$5::uuid,$6)`,
		g.TenantID, g.SubscriptionID, out.FromStatus, g.ActorKind, g.OrderID,
		map[string]any{"period_end": out.PeriodEnd, "previous_end": out.PreviousEnd,
			"restart": restart, "source": g.Source}); err != nil {
		return out, err
	}
	return out, nil
}

// restartQuotaPeriodsTx 是过期恢复时的配额对齐：全部配额行（total / cycle / day / month）
// 已用量清零，周期边界对齐到付款时刻。清零前的用量先取出来写重置日志 —— UPDATE 之后
// 就再也读不到了，而「续费时你已经用了多少」正是用户最常问的那个数字。
//
// 原先只清 period = 'cycle'：带总量或按月给流量的套餐过期又超量后，续了费照样连不上；
// day / month 行在过期期间不滚动（rollQuotaSQL 跳过已过期订阅），续费后还要每 10 分钟
// 追一格。
func restartQuotaPeriodsTx(ctx context.Context, tx pgx.Tx, g renewalGrant,
	start, cycleEnd time.Time) error {
	rows, err := tx.Query(ctx, `
		UPDATE quota_balances qb
		   SET consumed = 0,
		       period_start = $3::timestamptz,
		       period_end = `+quotaPeriodEndSQL("qb.period", "$3::timestamptz", "$4::timestamptz")+`,
		       notified_thresholds = '{}',
		       overage_applied_at = NULL,
		       updated_at = now()
		 WHERE qb.tenant_id = $1 AND qb.subscription_id = $2::uuid
		RETURNING qb.metric, OLD.consumed`,
		g.TenantID, g.SubscriptionID, start, cycleEnd)
	if err != nil {
		return fmt.Errorf("重置配额: %w", err)
	}
	type consumedSnapshot struct {
		metric   string
		consumed int64
	}
	var before []consumedSnapshot
	for rows.Next() {
		var snap consumedSnapshot
		if err := rows.Scan(&snap.metric, &snap.consumed); err != nil {
			rows.Close()
			return err
		}
		before = append(before, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, snap := range before {
		if err := LogTrafficReset(ctx, tx, g.TenantID, g.SubscriptionID, g.UserID,
			snap.metric, "renewal", snap.consumed, nil, ""); err != nil {
			return fmt.Errorf("记录续费重置: %w", err)
		}
	}
	return nil
}

// fulfillRenewal 在支付成功后延长订阅周期并按策略重置配额。
// Callers must lock the subscription before any renewal ledger-account lock.
func (s *Service) fulfillRenewal(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, userID string) (string, error) {
	subID, _, _, err := lockOrderSubscriptionForSettlement(ctx, tx, tenantID, orderID, userID)
	if err != nil {
		return "", err
	}
	return s.fulfillRenewalLocked(ctx, tx, tenantID, orderID, userID, subID)
}

func (s *Service) fulfillRenewalLocked(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, userID, subID string) (string, error) {

	if subID == "" {
		return "", errors.New("续费订单没有关联订阅")
	}

	g := renewalGrant{TenantID: tenantID, UserID: userID, SubscriptionID: subID,
		ActorKind: "payment", OrderID: &orderID, Source: "order"}
	if err := tx.QueryRow(ctx, `
		SELECT snapshot_interval, snapshot_interval_count,
		       plan_version_id::text, price_id::text
		  FROM order_items
		 WHERE tenant_id = $1 AND order_id = $2::uuid
		 ORDER BY created_at LIMIT 1`,
		tenantID, orderID).Scan(&g.Interval, &g.IntervalCount,
		&g.PlanVersionID, &g.PriceID); err != nil {
		return "", fmt.Errorf("读取续费订单行: %w", err)
	}
	// 付款时刻：结算与零元单捕获都在同一事务里先把订单置为 paid（paid_at = now()）
	var paidAt *time.Time
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT paid_at, created_at FROM orders
		 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, orderID).Scan(&paidAt, &createdAt); err != nil {
		return "", fmt.Errorf("读取续费订单: %w", err)
	}
	if paidAt == nil {
		return "", errors.New("renewal order fulfilled before it was paid")
	}
	g.PaidAt = *paidAt
	g.OrderCreatedAt = &createdAt

	if _, err := renewSubscriptionTx(ctx, tx, g); err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, orderID); err != nil {
		return "", err
	}
	return subID, nil
}
