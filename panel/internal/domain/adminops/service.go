// Package adminops 实现管理后台的读写用例。
//
// 与 billing / identity 的分工：那两个包承载业务不变量（账本必须配平、
// 状态机不能非法跳转），本包只做「管理员视角的查询与编排」，
// 任何涉及资金或权益的动作仍然调用它们，绝不自己写一遍记账逻辑。
//
// 本包所有写操作都必须留下审计（SEC-012），这是管理面区别于用户面的硬要求。
package adminops

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

type Service struct {
	pool *db.Pool
	// dash 是看板读模型的 30 秒缓存（dashboard_cache.go）；nil 时不缓存
	dash *dashboardCache
	// userGenWake 是批量生成账号任务的进程内唤醒（user_generation_wake.go）；容量 1。
	userGenWake chan struct{}
}

func NewService(pool *db.Pool) *Service {
	return &Service{pool: pool, dash: newDashboardCache(dashboardCacheTTL, time.Now), userGenWake: make(chan struct{}, 1)}
}

//==============================================================================
// 仪表盘
//==============================================================================

type Overview struct {
	Users struct {
		Total     int64 `json:"total"`
		Active    int64 `json:"active"`
		Today     int64 `json:"today"`
		Last7Days int64 `json:"last_7_days"`
	} `json:"users"`
	Subscriptions struct {
		Active   int64 `json:"active"`
		Trialing int64 `json:"trialing"`
		Expiring int64 `json:"expiring_7_days"`
		Expired  int64 `json:"expired"`
		// New7Days 是近 7 天新建、当前仍 active / trialing 的订阅
		New7Days int64 `json:"new_7_days"`
	} `json:"subscriptions"`
	// Nodes 只算没退役的节点；online 与 GET v1/nodes 的 stale 取反一致（心跳 ≤90 秒）
	Nodes struct {
		Total  int64 `json:"total"`
		Online int64 `json:"online"`
	} `json:"nodes"`
	// 按币种分组而不是汇总成一个数：不同币种的最小单位金额直接相加
	// 得到的是无意义的数字（1 日元 + 1 美元 = 2 什么？）。
	// 平台同时在售多币种价格时，混加会让经营数据彻底失真。
	Revenue []RevenueRow `json:"revenue"`
	Orders  struct {
		Paid    int64 `json:"paid_today"`
		Pending int64 `json:"pending"`
		Failed  int64 `json:"failed_today"`
	} `json:"orders"`
	// LedgerDrift 非零说明账本缓存余额与分录求和不一致，属于必须立刻查的严重信号
	LedgerDrift int64 `json:"ledger_drift_accounts"`
}

type RevenueRow struct {
	Currency         string `json:"currency"`
	Today            int64  `json:"today"`
	Yesterday        int64  `json:"yesterday"`
	ActualYesterday  int64  `json:"actual_yesterday"`
	Last7Days        int64  `json:"last_7_days"`
	Last30           int64  `json:"last_30_days"`
	Total            int64  `json:"total"`
	ActualToday      int64  `json:"actual_today"`
	Actual7Days      int64  `json:"actual_7_days"`
	Actual30Days     int64  `json:"actual_30_days"`
	ActualTotal      int64  `json:"actual_total"`
	AdjustmentToday  int64  `json:"adjustment_today"`
	Adjustment7Days  int64  `json:"adjustment_7_days"`
	Adjustment30Days int64  `json:"adjustment_30_days"`
	AdjustmentTotal  int64  `json:"adjustment_total"`
}

// Overview 读看板概览，结果按租户缓存 dashboardCacheTTL。
func (s *Service) Overview(ctx context.Context, tenantID string) (*Overview, error) {
	return cachedRead(s.dash, "overview:"+tenantID, func() (*Overview, error) {
		return s.readOverview(ctx, tenantID)
	})
}

// overviewRevenueSQL 是一个币种的收入：实际收入只认 platform_revenue 的全部分录净额
// （credit - debit）；报表调整来自独立追加表，只影响展示值，不回写账本。日界按租户时区，
// 时区在语句里按租户读（原来先单独查一次再代入，多一次往返）。
const overviewRevenueSQL = `
	WITH params AS (
	 SELECT (now() AT TIME ZONE t.timezone)::date AS today, t.timezone AS tz
	   FROM tenants t WHERE t.id = $1
	), actual AS (
	 SELECT coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
	          FILTER(WHERE (lt.occurred_at AT TIME ZONE p.tz)::date>=p.today),0)::bigint,
	        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
	          FILTER(WHERE (lt.occurred_at AT TIME ZONE p.tz)::date>=p.today-6),0)::bigint,
	        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
	          FILTER(WHERE (lt.occurred_at AT TIME ZONE p.tz)::date>=p.today-29),0)::bigint,
	        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END),0)::bigint,
	        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
	          FILTER(WHERE (lt.occurred_at AT TIME ZONE p.tz)::date=p.today-1),0)::bigint
	   FROM ledger_entries le
	   JOIN ledger_transactions lt ON lt.id=le.transaction_id AND lt.tenant_id=le.tenant_id
	   JOIN ledger_accounts la ON la.id=le.account_id AND la.tenant_id=le.tenant_id
	   CROSS JOIN params p
	  WHERE le.tenant_id=$1 AND le.currency=$2 AND la.account_type='platform_revenue'
	), adj AS (
	 SELECT coalesce(sum(amount) FILTER(WHERE effective_on=p.today),0)::bigint,
	        coalesce(sum(amount) FILTER(WHERE effective_on>=p.today-6),0)::bigint,
	        coalesce(sum(amount) FILTER(WHERE effective_on>=p.today-29),0)::bigint,
	        coalesce(sum(amount),0)::bigint,
	        coalesce(sum(amount) FILTER(WHERE effective_on=p.today-1),0)::bigint
	   FROM revenue_report_adjustments, params p WHERE tenant_id=$1 AND currency=$2
	) SELECT * FROM actual CROSS JOIN adj`

// readOverview 直读概览。各项互不依赖，排进一个批次经 BatchScoped 一次往返
// （原来 InTx 里逐条执行：BEGIN + 注入、八条语句、COMMIT，共 10 次往返）。
func (s *Service) readOverview(ctx context.Context, tenantID string) (*Overview, error) {
	var o Overview
	b := &pgx.Batch{}
	b.Queue(`
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'active'),
		       count(*) FILTER (WHERE created_at >= date_trunc('day', now())),
		       count(*) FILTER (WHERE created_at >= now() - interval '7 days')
		  FROM users WHERE tenant_id = $1`, tenantID,
	).QueryRow(func(row pgx.Row) error {
		return row.Scan(&o.Users.Total, &o.Users.Active, &o.Users.Today, &o.Users.Last7Days)
	})
	b.Queue(`
		SELECT count(*) FILTER (WHERE status = 'active'),
		       count(*) FILTER (WHERE status = 'trialing'),
		       -- 7 天内到期：补上下限（w5expiry）。原先没有 > now()，状态还没被过期扫描
		       -- 翻成 expired 的历史订阅全算进来，数字只增不减
		       count(*) FILTER (WHERE status IN ('active','trialing')
		                          AND current_period_end > now()
		                          AND current_period_end < now() + interval '7 days'),
		       count(*) FILTER (WHERE status = 'expired'),
		       count(*) FILTER (WHERE status IN ('active','trialing')
		                          AND created_at >= now() - interval '7 days')
		  FROM subscriptions WHERE tenant_id = $1`, tenantID,
	).QueryRow(func(row pgx.Row) error {
		return row.Scan(&o.Subscriptions.Active, &o.Subscriptions.Trialing,
			&o.Subscriptions.Expiring, &o.Subscriptions.Expired, &o.Subscriptions.New7Days)
	})
	b.Queue(`
		SELECT count(*),
		       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds')
		  FROM nodes
		 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status <> 'retired'`, tenantID,
	).QueryRow(func(row pgx.Row) error { return row.Scan(&o.Nodes.Total, &o.Nodes.Online) })
	revenue := []RevenueRow{{Currency: "CNY"}, {Currency: "USD"}}
	for i := range revenue {
		r := &revenue[i]
		b.Queue(overviewRevenueSQL, tenantID, r.Currency).QueryRow(func(row pgx.Row) error {
			var adjYesterday int64
			if err := row.Scan(&r.ActualToday, &r.Actual7Days, &r.Actual30Days, &r.ActualTotal, &r.ActualYesterday,
				&r.AdjustmentToday, &r.Adjustment7Days, &r.Adjustment30Days, &r.AdjustmentTotal, &adjYesterday); err != nil {
				return err
			}
			r.Yesterday = r.ActualYesterday + adjYesterday
			r.Today = r.ActualToday + r.AdjustmentToday
			r.Last7Days = r.Actual7Days + r.Adjustment7Days
			r.Last30 = r.Actual30Days + r.Adjustment30Days
			r.Total = r.ActualTotal + r.AdjustmentTotal
			return nil
		})
	}
	b.Queue(`
		SELECT count(*) FILTER (WHERE status IN ('paid','fulfilled')
		                          AND paid_at >= date_trunc('day', now())),
		       count(*) FILTER (WHERE status IN ('draft','pending_payment')),
		       count(*) FILTER (WHERE status IN ('cancelled','expired')
		                          AND updated_at >= date_trunc('day', now()))
		  FROM orders WHERE tenant_id = $1`, tenantID,
	).QueryRow(func(row pgx.Row) error { return row.Scan(&o.Orders.Paid, &o.Orders.Pending, &o.Orders.Failed) })
	b.Queue(`SELECT count(*) FROM app.verify_ledger_all()`).
		QueryRow(func(row pgx.Row) error { return row.Scan(&o.LedgerDrift) })
	if err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{}, b); err != nil {
		return nil, httpx.Internal(err)
	}
	o.Revenue = revenue
	return &o, nil
}

//==============================================================================
// 用户
//==============================================================================

// SetUserStatus 停用/恢复账号。
//
// 停用同时吊销全部会话：否则被封的账号靠已签发的令牌还能继续用到过期，
// 「封号」就成了摆设。
func (s *Service) SetUserStatus(ctx context.Context, tenantID, actorID, userID, status, reason string) error {
	switch status {
	case "active", "suspended", "banned":
	default:
		return httpx.New(httpx.CodeBadRequest, "不支持的账号状态")
	}
	if status != "active" && strings.TrimSpace(reason) == "" {
		return httpx.New(httpx.CodeValidationFailed, "停用或封禁必须填写原因")
	}
	if actorID == userID {
		return httpx.New(httpx.CodeConflict, "不能变更自己的账号状态")
	}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if err := iamguard.LockLastAdministrator(ctx, tx, tenantID); err != nil {
			return err
		}
		var before string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM users WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
			tenantID, userID).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		// 越级闸：目标持有操作者没有的权限时拒绝（403），停用与恢复都一样。
		// 在「状态没变」的短路之前判断，管不了的账号连空操作也不放行
		if _, err := iamguard.CanManage(ctx, tx, tenantID, actorID, userID); err != nil {
			return err
		}
		if before == status {
			return iamguard.RequireEffectiveAdministrator(ctx, tx, tenantID)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE users SET status = $3 WHERE tenant_id = $1 AND id = $2`,
			tenantID, userID, status); err != nil {
			return err
		}

		revoked := int64(0)
		if status != "active" {
			var err error
			if revoked, err = revokeUserLogins(ctx, tx, tenantID, userID, status); err != nil {
				return err
			}
		}
		if err := iamguard.RequireEffectiveAdministrator(ctx, tx, tenantID); err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "user.status_change", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			BeforeDigest: map[string]any{"status": before},
			// 原因并入 after_digest：审计表没有独立的 reason 列，
			// 摘要字段本就是为「为什么这么改」准备的
			AfterDigest: map[string]any{
				"status": status, "reason": reason, "sessions_revoked": revoked},
			RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return err
		}
		return httpx.Internal(err)
	}
	return nil
}

// revokeUserLogins 让一个被停用或封禁的账号立刻下线：吊销全部会话与 refresh
// 令牌，返回吊销的会话数。改状态与风控批量禁用共用它——停用却不踢下线，
// 等于给了对方一段令牌自然过期前的窗口。
func revokeUserLogins(ctx context.Context, tx pgx.Tx, tenantID, userID, status string) (int64, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $3
		 WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		tenantID, userID, "账号状态变更为 "+status)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET status = 'revoked'
		 WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`,
		tenantID, userID); err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

//==============================================================================
// 套餐目录
//==============================================================================

type PlanRow struct {
	ID               string     `json:"id"`
	ProductID        string     `json:"product_id"`
	RowVersion       int64      `json:"row_version"`
	Code             string     `json:"code"`
	Name             string     `json:"name"`
	Description      *string    `json:"description"`
	Status           string     `json:"status"`
	Visibility       string     `json:"visibility"`
	SortOrder        int        `json:"sort_order"`
	CurrentVersionID *string    `json:"current_version_id"`
	DraftVersionID   *string    `json:"draft_version_id"`
	Version          *int       `json:"version"`
	MaxDevices       *int       `json:"max_devices"`
	TrafficLimit     *int64     `json:"traffic_limit"`
	Highlights       []string   `json:"highlights"`
	Recommended      bool       `json:"recommended"`
	Prices           []PriceRow `json:"prices"`
	ActiveSubs       int        `json:"active_subscriptions"`
	// NodeCount 是这个套餐当前版本能看到的在线节点数。
	//
	// 为零意味着买了这个套餐的人会拿到一份空订阅 —— 客户端里一个节点都没有。
	// 这种情况从用户侧看是「买了个寂寞」，从管理侧却完全无声无息，
	// 所以必须在卖之前就摆到台面上。
	NodeCount int `json:"node_count"`
}

type PriceRow struct {
	ID          string     `json:"id"`
	Currency    string     `json:"currency"`
	UnitAmount  int64      `json:"unit_amount"`
	Interval    string     `json:"billing_interval"`
	Count       int16      `json:"interval_count"`
	TrialDays   int16      `json:"trial_days"`
	Status      string     `json:"status"`
	UserGroupID *string    `json:"user_group_id"`
	ValidFrom   *time.Time `json:"valid_from"`
	ValidUntil  *time.Time `json:"valid_until"`
	RowVersion  int64      `json:"row_version"`
}

func (s *Service) ListPlans(ctx context.Context, tenantID string) ([]PlanRow, error) {
	out := []PlanRow{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT pl.id, pl.product_id, pl.row_version, pl.code, pl.name, pl.description,
			       pl.status, pl.visibility, pl.sort_order, pl.current_version_id,
			       pl.highlights, pl.recommended,
			       (SELECT dpv.id FROM plan_versions dpv
			         WHERE dpv.tenant_id=pl.tenant_id AND dpv.plan_id=pl.id AND dpv.status='draft'
			         LIMIT 1),
			       pv.version, pv.max_devices,
			       (SELECT qd.limit_value FROM quota_definitions qd
			         WHERE qd.plan_version_id = pv.id AND qd.metric = 'traffic.bytes' LIMIT 1),
			       (SELECT count(*) FROM subscriptions s
			         WHERE s.tenant_id = pl.tenant_id AND s.plan_id = pl.id
			           AND s.status IN `+subscription.LiveStatusesSQL+`),
			       (SELECT count(*) FROM nodes n
			          JOIN plan_node_pools pnp ON pnp.pool_id = n.pool_id
			         WHERE pnp.plan_version_id = pv.id
				   AND n.serving_status = 'active' AND n.node_type IS NOT NULL)
			  FROM plans pl
			  LEFT JOIN plan_versions pv ON pv.id = pl.current_version_id
			 WHERE pl.tenant_id = $1
			 ORDER BY pl.sort_order, pl.created_at`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var p PlanRow
			if err := rows.Scan(&p.ID, &p.ProductID, &p.RowVersion, &p.Code, &p.Name,
				&p.Description, &p.Status, &p.Visibility, &p.SortOrder,
				&p.CurrentVersionID, &p.Highlights, &p.Recommended, &p.DraftVersionID, &p.Version, &p.MaxDevices,
				&p.TrafficLimit, &p.ActiveSubs, &p.NodeCount); err != nil {
				return err
			}
			p.Prices = []PriceRow{}
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for i := range out {
			prows, err := tx.Query(ctx, `
				SELECT pr.id, pr.currency, pr.unit_amount, pr.billing_interval,
				       pr.interval_count, pr.trial_days, pr.status, pr.user_group_id,
				       pr.valid_from, pr.valid_until, pr.row_version
				  FROM prices pr JOIN plans pl ON pl.product_id = pr.product_id
				 WHERE pr.tenant_id = $1 AND pl.id = $2
				 ORDER BY pr.status, pr.unit_amount`, tenantID, out[i].ID)
			if err != nil {
				return err
			}
			for prows.Next() {
				var pr PriceRow
				if err := prows.Scan(&pr.ID, &pr.Currency, &pr.UnitAmount, &pr.Interval,
					&pr.Count, &pr.TrialDays, &pr.Status, &pr.UserGroupID,
					&pr.ValidFrom, &pr.ValidUntil, &pr.RowVersion); err != nil {
					prows.Close()
					return err
				}
				out[i].Prices = append(out[i].Prices, pr)
			}
			prows.Close()
			if err := prows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}
