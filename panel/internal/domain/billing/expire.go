package billing

// 订阅过期扫描（用户 2026-10-07 规则；aegis-admin 的独立循环调用）。
//
// 停发本身不靠它：节点名单与订阅拉取都按 current_period_end 现算，到点就停。它做的是
// 「到期」之后那些需要落库的事：
//
//  1. 到期：status 改成 expired（active / trialing 到周期末，grace 到宽限末，past_due 到周期末），
//     写 expired 订阅事件，排 subscription.expired 插件钩子。只改状态，不动凭据 ——
//     凭据一动，续费时链接就救不回来了。到期通知与过期后的召回由 notify 的扫描按
//     status = 'expired' 发（notify/scan.go）。
//  2. 关窗：过期满 30 天（expiredRenewalWindow）的订阅写上 renewal_closed_at、吊销它的
//     有效凭据（旧链接作废），写 renewal_closed 事件。之后只能新购、换新链接（规则 4）。
//
// 两步都分批、每批一个短事务、按 id SKIP LOCKED：正在被续费、加时长锁着的订阅这一轮
// 跳过，下一轮再看（那边多半已经把它续回 active 了）。连跑两轮，第二轮什么都不改。

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// expiredRenewalWindowSQL 是原地续费窗口：过期满这么久就关窗（规则 4，30 天）。
// 窗口关没关只看 renewal_closed_at 这一列（Go 与 00124 的挂账守卫读同一个事实）。
const expiredRenewalWindowSQL = "30 days"

const (
	expireScanBatch      = 200
	expireScanMaxBatches = 50
)

// ExpiryScanResult 是一轮过期扫描的结果。
type ExpiryScanResult struct {
	Expired            int
	Closed             int
	RevokedCredentials int
}

// expireDueSQL 把到期的订阅改成 expired，$1 租户、$2 批量上限。
//
// 谓词带 current_period_end <= now()，走 idx_subscriptions_period_end（00003 的部分索引，
// 状态集合与这里相同）。grace 的截止取宽限末。RETURNING 的 OLD.status（PG18）是锁住
// 之后的最新值。
const expireDueSQL = `
	WITH due AS (
		SELECT s.id
		  FROM subscriptions s
		 WHERE s.tenant_id = $1
		   AND s.status IN ('active', 'trialing', 'past_due', 'grace')
		   AND s.current_period_end <= now()
		   AND (s.status <> 'grace' OR s.grace_end IS NULL OR s.grace_end <= now())
		 ORDER BY s.id
		 LIMIT $2
		   FOR UPDATE SKIP LOCKED
	), flipped AS (
		UPDATE subscriptions s
		   SET status = 'expired', updated_at = now()
		  FROM due
		 WHERE s.id = due.id
		RETURNING s.id, s.tenant_id, s.user_id, s.plan_id, OLD.status AS from_status,
		          s.current_period_end
	), events AS (
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status, actor_kind, payload)
		SELECT f.tenant_id, f.id, 'expired', f.from_status, 'expired', 'scheduler',
		       jsonb_build_object('period_end', f.current_period_end)
		  FROM flipped f
	)
	SELECT f.id::text, f.user_id::text, coalesce(p.name, ''), f.current_period_end
	  FROM flipped f
	  LEFT JOIN plans p ON p.tenant_id = f.tenant_id AND p.id = f.plan_id`

// closeRenewalWindowSQL 关掉过期满 30 天的订阅的原地续费窗口，并吊销它的有效凭据。
// $1 租户、$2 批量上限。返回关窗的订阅数与吊销的凭据数。
const closeRenewalWindowSQL = `
	WITH due AS (
		SELECT s.id
		  FROM subscriptions s
		 WHERE s.tenant_id = $1
		   AND s.status = 'expired' AND s.renewal_closed_at IS NULL
		   AND s.current_period_end <= now() - interval '` + expiredRenewalWindowSQL + `'
		 ORDER BY s.id
		 LIMIT $2
		   FOR UPDATE SKIP LOCKED
	), closed AS (
		UPDATE subscriptions s
		   SET renewal_closed_at = now(), updated_at = now()
		  FROM due
		 WHERE s.id = due.id
		RETURNING s.id, s.tenant_id
	), revoked AS (
		UPDATE subscription_credentials sc
		   SET status = 'revoked', revoked_at = now(), revoked_reason = 'renewal_window_closed'
		  FROM closed c
		 WHERE sc.tenant_id = c.tenant_id AND sc.subscription_id = c.id
		   AND sc.status IN ('active', 'grace')
		RETURNING sc.subscription_id
	), events AS (
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status, actor_kind, payload)
		SELECT c.tenant_id, c.id, 'renewal_closed', 'expired', 'expired', 'scheduler',
		       jsonb_build_object('window', '` + expiredRenewalWindowSQL + `',
		                          'revoked_credentials',
		                          (SELECT count(*) FROM revoked r WHERE r.subscription_id = c.id))
		  FROM closed c
	)
	SELECT (SELECT count(*) FROM closed)::int, (SELECT count(*) FROM revoked)::int`

// ScanExpiredSubscriptions 跑一轮过期扫描（见文件头）。幂等：连跑两轮，第二轮零改动。
func (s *Service) ScanExpiredSubscriptions(ctx context.Context, tenantID string) (ExpiryScanResult, error) {
	var res ExpiryScanResult
	for batch := 0; batch < expireScanMaxBatches; batch++ {
		n := 0
		err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, expireDueSQL, tenantID, expireScanBatch)
			if err != nil {
				return err
			}
			type expired struct {
				subID, userID, plan string
				periodEnd           time.Time
			}
			var items []expired
			for rows.Next() {
				var it expired
				if err := rows.Scan(&it.subID, &it.userID, &it.plan, &it.periodEnd); err != nil {
					rows.Close()
					return err
				}
				items = append(items, it)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			n = len(items)
			// 插件钩子与状态翻转同一事务；键里带周期末：续费恢复后再次到期是新的一次
			for _, it := range items {
				key := fmt.Sprintf("sub.expired:%s:%d", it.subID, it.periodEnd.Unix())
				if err := plugin.Emit(ctx, tx, tenantID, "subscription.expired", key, map[string]any{
					"event": "subscription.expired", "subscription_id": it.subID,
					"user_id": it.userID, "plan": it.plan,
					"expired_at": it.periodEnd.UTC().Format(time.RFC3339),
				}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("过期扫描: %w", err)
		}
		res.Expired += n
		if n < expireScanBatch {
			break
		}
	}
	for batch := 0; batch < expireScanMaxBatches; batch++ {
		var closed, revoked int
		err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, closeRenewalWindowSQL, tenantID, expireScanBatch).Scan(&closed, &revoked)
		})
		if err != nil {
			return res, fmt.Errorf("关闭原地续费窗口: %w", err)
		}
		res.Closed += closed
		res.RevokedCredentials += revoked
		if closed < expireScanBatch {
			break
		}
	}
	// 到期的订阅已经按 current_period_end 被节点名单排除，这里不推进下发纪元；
	// 关窗只吊销凭据，也不影响节点名单。
	return res, nil
}
