package billing

// 礼品卡加时长、后台加时长救回已过期或试用中的订阅时的流量（用户 2026-10-07 规则 2、6）。
//
//	cycle 流量   按「延长天数 ÷ 套餐周期天数」折算，加进本周期 cycle 配额的上限；已用量沿用。
//	             只延时间的话，流量已用完的用户救回后照样连不上；全额清零的话，一张 1 天卡
//	             就能换一整月流量。只有付费续费给满额（renewal_fulfill.go）。
//	day / month  走过到期日的救回从今天起按自然周期重新对齐（已用量清零、写重置日志）：
//	             过期期间它们不滚动，不对齐的话恢复后要一格一格追。
//	total        订阅存续期总量，不动。
//
// 折算出的额度加在 limit_value 上，只属于本周期：下一次付费续费（过期恢复）或周期滚动
// 会把上限按套餐定义写回去。

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
)

// defaultRescueCycle 是订阅没有价格档（礼品卡开通且卡上没绑价格）时折算用的套餐周期。
const defaultRescueCycle = 30 * 24 * time.Hour

// proratedQuota 是 limit × days / cycle，向下取整；整数运算，不经浮点。
func proratedQuota(limit int64, days int, cycle time.Duration) int64 {
	if limit <= 0 || days <= 0 || cycle <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(limit), big.NewInt(int64(days)*int64(24*time.Hour)))
	n.Quo(n, big.NewInt(int64(cycle)))
	if !n.IsInt64() {
		return 0
	}
	return n.Int64()
}

// subscriptionCycleLength 取订阅价格档的一个计费周期有多长（与 addInterval 同口径）。
func subscriptionCycleLength(ctx context.Context, tx pgx.Tx, tenantID, subID string,
	from time.Time) (time.Duration, error) {
	var interval *string
	var count *int16
	err := tx.QueryRow(ctx, `
		SELECT pr.billing_interval, pr.interval_count
		  FROM subscriptions s
		  LEFT JOIN prices pr ON pr.tenant_id = s.tenant_id AND pr.id = s.price_id
		 WHERE s.tenant_id = $1 AND s.id = $2::uuid`, tenantID, subID).Scan(&interval, &count)
	if err != nil {
		return 0, err
	}
	if interval == nil || count == nil {
		return defaultRescueCycle, nil
	}
	return addInterval(from, *interval, int(*count)).Sub(from), nil
}

// rescueQuotaTx 给被救回的订阅折算流量；返回每个指标加了多少。调用方已锁住订阅行，
// 并已把本周期 cycle 行的 period_end 推到 periodEnd。
func rescueQuotaTx(ctx context.Context, tx pgx.Tx, ext subscriptionExtension,
	periodEnd time.Time, lapsed bool, now time.Time) (map[string]int64, error) {

	cycle, err := subscriptionCycleLength(ctx, tx, ext.TenantID, ext.SubscriptionID, now)
	if err != nil {
		return nil, fmt.Errorf("读取套餐周期: %w", err)
	}
	type row struct {
		id, metric string
		limit      int64
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, metric, limit_value FROM quota_balances
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid
		   AND period = 'cycle' AND period_end = $3 AND limit_value IS NOT NULL
		 ORDER BY id FOR UPDATE`, ext.TenantID, ext.SubscriptionID, periodEnd)
	if err != nil {
		return nil, err
	}
	var cycleRows []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.metric, &r.limit); err != nil {
			rows.Close()
			return nil, err
		}
		cycleRows = append(cycleRows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	prorated := map[string]int64{}
	for _, r := range cycleRows {
		add := proratedQuota(r.limit, ext.Days, cycle)
		if add <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE quota_balances
			   SET limit_value = limit_value + $2,
			       notified_thresholds = '{}',
			       overage_applied_at = NULL,
			       updated_at = now()
			 WHERE id = $1::uuid`, r.id, add); err != nil {
			return nil, fmt.Errorf("折算救回流量: %w", err)
		}
		prorated[r.metric] += add
	}

	if !lapsed {
		return prorated, nil
	}
	var userID string
	if err := tx.QueryRow(ctx, `SELECT user_id::text FROM subscriptions
		WHERE tenant_id = $1 AND id = $2::uuid`, ext.TenantID, ext.SubscriptionID).Scan(&userID); err != nil {
		return nil, err
	}
	resetRows, err := tx.Query(ctx, `
		UPDATE quota_balances qb
		   SET consumed = 0,
		       period_start = $3::timestamptz,
		       period_end = `+quotaPeriodEndSQL("qb.period", "$3::timestamptz", "$4::timestamptz")+`,
		       notified_thresholds = '{}',
		       overage_applied_at = NULL,
		       updated_at = now()
		 WHERE qb.tenant_id = $1 AND qb.subscription_id = $2::uuid
		   AND qb.period IN ('day', 'month')
		RETURNING qb.metric, OLD.consumed`, ext.TenantID, ext.SubscriptionID, now, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("对齐日 / 月配额: %w", err)
	}
	type snap struct {
		metric   string
		consumed int64
	}
	var before []snap
	for resetRows.Next() {
		var s snap
		if err := resetRows.Scan(&s.metric, &s.consumed); err != nil {
			resetRows.Close()
			return nil, err
		}
		before = append(before, s)
	}
	resetRows.Close()
	if err := resetRows.Err(); err != nil {
		return nil, err
	}
	// 重置日志的原因：礼品卡记 gift_card；后台加时长记 manual（约束要求带操作人）
	reason, actor := "gift_card", (*string)(nil)
	if ext.Source == "admin" {
		reason, actor = "manual", ext.ActorID
	}
	for _, s := range before {
		if err := LogTrafficReset(ctx, tx, ext.TenantID, ext.SubscriptionID, userID,
			s.metric, reason, s.consumed, actor, "救回已过期订阅，日 / 月配额从今天重新起算"); err != nil {
			return nil, fmt.Errorf("记录救回重置: %w", err)
		}
	}
	return prorated, nil
}
