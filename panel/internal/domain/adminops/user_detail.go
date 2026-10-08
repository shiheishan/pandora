package adminops

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// GetUser 读后台用户抽屉的全部字段。
//
// 九条读互不依赖，排进一个批次经 BatchScoped 一次往返发出（原来是 InTx 里逐条执行：
// BEGIN + 注入、九条语句、COMMIT，共 11 次往返）。配额原来按上一条读出的订阅 id 再查，
// 现在按同一个用户的订阅子查询取，不再依赖前一条的结果。只读，没有写入要回滚。
func (s *Service) GetUser(ctx context.Context, tenantID, userID string) (*UserDetail, error) {
	// 非 uuid 的 id 与不存在同样 404：交给 SQL 会变成 500
	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	d := UserDetail{Subscriptions: []SubscriptionRow{}, Orders: []OrderRow{}, Roles: []string{}}
	quotas := map[string][]QuotaRow{}
	var verifiedAt *time.Time
	b := &pgx.Batch{}

	b.Queue(`
		SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
		       u.created_at, u.last_login_at, u.email_verified_at,
		       coalesce((SELECT -la.balance_signed FROM ledger_accounts la
		                  WHERE la.owner_user_id = u.id
		                    AND la.account_type = 'user_balance' LIMIT 1), 0),
		       coalesce((SELECT la.currency FROM ledger_accounts la
		                  WHERE la.owner_user_id = u.id
		                    AND la.account_type = 'user_balance' LIMIT 1), 'CNY'),
		       coalesce((SELECT g.name FROM user_groups g
		                  WHERE g.id = u.user_group_id), ''),
		       u.user_group_id::text,
		       coalesce((SELECT sum(o.paid_amount) FROM orders o WHERE o.tenant_id = u.tenant_id
		                  AND o.user_id = u.id AND o.status IN ('paid','fulfilled')), 0)::bigint,
		       (SELECT count(*) FROM orders o WHERE o.tenant_id = u.tenant_id AND o.user_id = u.id)::int,
		       (SELECT count(*) FROM referrals rf WHERE rf.tenant_id = u.tenant_id
		                  AND rf.referrer_user_id = u.id)::int,
		       -- 还没加到任何一份的流量包余量（购买模型统一：流量包按份挂）
		       (SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
		          FROM traffic_pack_grants g
		         WHERE g.tenant_id = u.tenant_id AND g.user_id = u.id AND g.subscription_id IS NULL
		           AND g.consumed_bytes < g.granted_bytes)
		  FROM users u WHERE u.tenant_id = $1 AND u.id = $2`,
		tenantID, userID,
	).QueryRow(func(row pgx.Row) error {
		err := row.Scan(&d.ID, &d.Email, &d.DisplayName, &d.Status, &d.RiskLevel,
			&d.CreatedAt, &d.LastLoginAt, &verifiedAt, &d.Balance, &d.Currency,
			&d.GroupName, &d.GroupID, &d.Stats.PaidTotal, &d.Stats.OrderCount, &d.Stats.ReferralCount,
			&d.UnattachedPackBytes)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	})

	b.Queue(`
		SELECT currency, sum(paid_amount)::bigint FROM orders
		 WHERE tenant_id = $1 AND user_id = $2 AND status IN ('paid','fulfilled')
		 GROUP BY currency HAVING sum(paid_amount) > 0
		 ORDER BY currency`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		totals, err := pgx.CollectRows(rows, pgx.RowToStructByPos[CurrencyAmount])
		d.Stats.PaidTotals = totals
		if d.Stats.PaidTotals == nil {
			d.Stats.PaidTotals = []CurrencyAmount{}
		}
		return err
	})

	// 邀请人与 Telegram 绑定最多一行：用 Query 取首行，查无此行不是错误
	b.Queue(`
		SELECT r.id::text, r.email::text FROM referrals rf
		  JOIN users r ON r.tenant_id = rf.tenant_id AND r.id = rf.referrer_user_id
		 WHERE rf.tenant_id = $1 AND rf.referee_user_id = $2`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		if rows.Next() {
			var ref UserRef
			if err := rows.Scan(&ref.ID, &ref.Email); err != nil {
				return err
			}
			d.Referrer = &ref
		}
		return rows.Err()
	})
	b.Queue(`
		SELECT username, bound_at FROM telegram_bindings
		 WHERE tenant_id = $1 AND user_id = $2
		 ORDER BY bound_at DESC LIMIT 1`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		if rows.Next() {
			var tg TelegramRef
			if err := rows.Scan(&tg.Username, &tg.BoundAt); err != nil {
				return err
			}
			d.Telegram = &tg
		}
		return rows.Err()
	})

	// 在线设备按单条订阅计（onlineDevicesSQL），窗口起点只算一次；流量包余量按份取
	// （每份一次 idx_traffic_pack_grants_open_sub 探测），与门户「我的套餐」同口径
	b.Queue(`
		WITH win AS MATERIALIZED (SELECT `+onlineSinceSQL("$1")+` AS since)
		SELECT s.id, pl.name, pv.version, s.status, s.current_period_start, s.current_period_end,
		       s.snapshot_amount, s.snapshot_currency, s.auto_renew,
		       s.device_limit, pv.max_devices, coalesce(od.device_count, 0)::int,
		       s.label,
		       (SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
		          FROM traffic_pack_grants g
		         WHERE g.tenant_id = s.tenant_id AND g.subscription_id = s.id
		           AND g.consumed_bytes < g.granted_bytes)
		  FROM subscriptions s
		  JOIN plans pl ON pl.id = s.plan_id
		  JOIN plan_versions pv ON pv.id = s.plan_version_id
		  CROSS JOIN win
		  LEFT JOIN LATERAL (`+onlineDevicesSQL("s.tenant_id", "s.id", "win.since")+`) od ON true
		 WHERE s.tenant_id = $1 AND s.user_id = $2
		 ORDER BY s.created_at DESC`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			r := SubscriptionRow{Quotas: []QuotaRow{}}
			if err := rows.Scan(&r.ID, &r.PlanName, &r.PlanVersion, &r.Status,
				&r.CurrentPeriodStart, &r.PeriodEnd, &r.Amount, &r.Currency, &r.AutoRenew,
				&r.DeviceLimitOverride, &r.PlanMaxDevices, &r.OnlineDevices,
				&r.Label, &r.PackRemainingBytes); err != nil {
				return err
			}
			d.Subscriptions = append(d.Subscriptions, r)
		}
		return rows.Err()
	})

	// 全部订阅的配额一次读完，每条订阅内的顺序不变（按 metric、period_start 倒序）
	b.Queue(`
		SELECT q.subscription_id::text, q.metric, q.limit_value, q.consumed, q.remaining
		  FROM quota_balances q
		 WHERE q.tenant_id = $1
		   AND q.subscription_id IN (SELECT s.id FROM subscriptions s
		                              WHERE s.tenant_id = $1 AND s.user_id = $2)
		 ORDER BY q.subscription_id, q.metric, q.period_start DESC`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var sub string
			var q QuotaRow
			if err := rows.Scan(&sub, &q.Metric, &q.Limit, &q.Consumed, &q.Remaining); err != nil {
				return err
			}
			quotas[sub] = append(quotas[sub], q)
		}
		return rows.Err()
	})

	// 与订单列表同一份查询：原先这里自己写了一遍 SELECT，漏了首项快照，
	// plan_name / interval / interval_count / item_count 恒为零值（缺陷 9）
	b.Queue(orderRowSelectSQL+`
		 WHERE o.tenant_id = $1 AND o.user_id = $2
		 ORDER BY o.created_at DESC LIMIT 20`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			r, err := scanOrderRow(rows)
			if err != nil {
				return err
			}
			d.Orders = append(d.Orders, r)
		}
		return rows.Err()
	})

	b.Queue(`
		SELECT r.code FROM role_bindings rb JOIN roles r ON r.id = rb.role_id
		 WHERE rb.tenant_id = $1 AND rb.user_id = $2
		   AND (rb.expires_at IS NULL OR rb.expires_at > now())
		 ORDER BY r.code`, tenantID, userID,
	).Query(func(rows pgx.Rows) error {
		roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
		d.Roles = append(d.Roles, roles...)
		return err
	})

	if err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{}, b); err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	d.EmailVerified = verifiedAt != nil
	for i := range d.Subscriptions {
		if q := quotas[d.Subscriptions[i].ID]; q != nil {
			d.Subscriptions[i].Quotas = q
		}
	}
	return &d, nil
}
