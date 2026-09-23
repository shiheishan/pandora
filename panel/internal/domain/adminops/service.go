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
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

// SalesCapability is an immutable process-start decision supplied by the
// authenticated release verifier. Requests cannot choose a receipt, expected
// digest or database identity and therefore cannot self-authorize sales.
type SalesCapability interface {
	AllowsP0BSales() bool
}

type Service struct {
	pool            *db.Pool
	salesCapability SalesCapability
}

// NewService defaults sales-authorizing mutations to denied. A release
// verifier may inject exactly one immutable capability after binding the
// running binary, database identity and catalog contract. Nil, zero or extra
// values remain denied.
func NewService(pool *db.Pool, capabilities ...SalesCapability) *Service {
	var capability SalesCapability
	if len(capabilities) == 1 && !nilSalesCapability(capabilities[0]) {
		capability = capabilities[0]
	}
	return &Service{pool: pool, salesCapability: capability}
}

func (s *Service) requireP0BSales() error {
	if s == nil || nilSalesCapability(s.salesCapability) || !s.salesCapability.AllowsP0BSales() {
		// 这句会直接出现在管理员面前。原先只说「服务暂时不可用，请稍后重试」，
		// 于是加价格、上架套餐一按就失败，重试多少次都一样，也没人猜得到
		// 是一个默认关闭的开关 —— 这两个功能就这么一直是死的。
		// 对外必须保持中性：这道闸门的存在本身不该被探测出来
		// （catalog_plan_update_route_test 明确断言响应里不出现
		// p0b / sales / capability / release 这些词）。
		//
		// 但管理员总得知道为什么用不了 —— 所以详情走日志，不走响应体。
		// 启动时那条 Warn 也会写清楚该设哪个变量。
		return httpx.New(httpx.CodeUnavailable, "服务暂时不可用，请稍后重试").
			WithInternal(errors.New(
				"P0B 销售能力未授权：在 .env 设置 AEGIS_SALES_ENABLED=1 后重启 aegis-admin"))
	}
	return nil
}

func nilSalesCapability(capability SalesCapability) bool {
	if capability == nil {
		return true
	}
	value := reflect.ValueOf(capability)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
	} `json:"subscriptions"`
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

func (s *Service) Overview(ctx context.Context, tenantID string) (*Overview, error) {
	var o Overview
	o.Revenue = []RevenueRow{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE status = 'active'),
			       count(*) FILTER (WHERE created_at >= date_trunc('day', now())),
			       count(*) FILTER (WHERE created_at >= now() - interval '7 days')
			  FROM users WHERE tenant_id = $1`, tenantID,
		).Scan(&o.Users.Total, &o.Users.Active, &o.Users.Today, &o.Users.Last7Days); err != nil {
			return err
		}

		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE status = 'active'),
			       count(*) FILTER (WHERE status = 'trialing'),
			       count(*) FILTER (WHERE status IN ('active','trialing')
			                          AND current_period_end < now() + interval '7 days'),
			       count(*) FILTER (WHERE status = 'expired')
			  FROM subscriptions WHERE tenant_id = $1`, tenantID,
		).Scan(&o.Subscriptions.Active, &o.Subscriptions.Trialing,
			&o.Subscriptions.Expiring, &o.Subscriptions.Expired); err != nil {
			return err
		}

		// 实际收入只认 platform_revenue 的全部分录净额：credit - debit。
		// 报表调整来自独立追加表，只影响展示值，不回写账本。
		var revenueTimezone string
		if err := tx.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenantID).Scan(&revenueTimezone); err != nil {
			return err
		}
		for _, currency := range []string{"CNY", "USD"} {
			r := RevenueRow{Currency: currency}
			if err := tx.QueryRow(ctx, `
				WITH params AS (
				 SELECT (now() AT TIME ZONE $3)::date AS today
				), actual AS (
				 SELECT coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
				          FILTER(WHERE (lt.occurred_at AT TIME ZONE $3)::date>=p.today),0)::bigint,
				        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
				          FILTER(WHERE (lt.occurred_at AT TIME ZONE $3)::date>=p.today-6),0)::bigint,
				        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
				          FILTER(WHERE (lt.occurred_at AT TIME ZONE $3)::date>=p.today-29),0)::bigint,
				        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END),0)::bigint
				   FROM ledger_entries le
				   JOIN ledger_transactions lt ON lt.id=le.transaction_id AND lt.tenant_id=le.tenant_id
				   JOIN ledger_accounts la ON la.id=le.account_id AND la.tenant_id=le.tenant_id
				   CROSS JOIN params p
				  WHERE le.tenant_id=$1 AND le.currency=$2 AND la.account_type='platform_revenue'
				), adj AS (
				 SELECT coalesce(sum(amount) FILTER(WHERE effective_on=p.today),0)::bigint,
				        coalesce(sum(amount) FILTER(WHERE effective_on>=p.today-6),0)::bigint,
				        coalesce(sum(amount) FILTER(WHERE effective_on>=p.today-29),0)::bigint,
				        coalesce(sum(amount),0)::bigint
				   FROM revenue_report_adjustments, params p WHERE tenant_id=$1 AND currency=$2
				) SELECT * FROM actual CROSS JOIN adj`, tenantID, currency, revenueTimezone).
				Scan(&r.ActualToday, &r.Actual7Days, &r.Actual30Days, &r.ActualTotal,
					&r.AdjustmentToday, &r.Adjustment7Days, &r.Adjustment30Days, &r.AdjustmentTotal); err != nil {
				return err
			}
			r.Today = r.ActualToday + r.AdjustmentToday
			r.Last7Days = r.Actual7Days + r.Adjustment7Days
			r.Last30 = r.Actual30Days + r.Adjustment30Days
			r.Total = r.ActualTotal + r.AdjustmentTotal
			o.Revenue = append(o.Revenue, r)
		}

		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE status IN ('paid','fulfilled')
			                          AND paid_at >= date_trunc('day', now())),
			       count(*) FILTER (WHERE status IN ('draft','pending_payment')),
			       count(*) FILTER (WHERE status IN ('cancelled','expired')
			                          AND updated_at >= date_trunc('day', now()))
			  FROM orders WHERE tenant_id = $1`, tenantID,
		).Scan(&o.Orders.Paid, &o.Orders.Pending, &o.Orders.Failed); err != nil {
			return err
		}

		return tx.QueryRow(ctx,
			`SELECT count(*) FROM app.verify_ledger_all()`).Scan(&o.LedgerDrift)
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return &o, nil
}

//==============================================================================
// 用户
//==============================================================================

type UserRow struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name"`
	Status      string  `json:"status"`
	RiskLevel   string  `json:"risk_level"`
	// GroupName 决定这个用户能看到哪些套餐、能用哪些券、按什么价买
	GroupName   string     `json:"group_name"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
	SubCount    int        `json:"subscription_count"`
	ActiveSub   *string    `json:"active_plan"`
	Balance     int64      `json:"balance"`
	Currency    string     `json:"currency"`
}

type ListUsersInput struct {
	Query  string
	Status string
	Limit  int
	Offset int
}

func (s *Service) ListUsers(ctx context.Context, tenantID string, in ListUsersInput) ([]UserRow, int64, error) {
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 25
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	out := []UserRow{}
	var total int64

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// ILIKE 前后都加通配会让索引失效，但管理端搜索量小且结果集有上限，
		// 这里优先保证「输入片段就能搜到」的可用性。数据量上来后改全文索引。
		q := "%" + strings.ToLower(strings.TrimSpace(in.Query)) + "%"
		if strings.TrimSpace(in.Query) == "" {
			q = "%"
		}
		status := in.Status
		if status == "" {
			status = "%"
		}

		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM users
			 WHERE tenant_id = $1
			   AND (lower(email) LIKE $2 OR lower(coalesce(display_name,'')) LIKE $2)
			   AND status::text LIKE $3`,
			tenantID, q, status).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
			       u.created_at, u.last_login_at,
			       (SELECT count(*) FROM subscriptions s WHERE s.user_id = u.id),
			       (SELECT pl.name FROM subscriptions s
			          JOIN plans pl ON pl.id = s.plan_id
			         WHERE s.user_id = u.id AND s.status IN ('active','trialing')
			         ORDER BY s.created_at DESC LIMIT 1),
			       coalesce((SELECT -la.balance_signed FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 0),
			       coalesce((SELECT la.currency FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
                                    AND la.account_type = 'user_balance' LIMIT 1), 'CNY')
			  FROM users u
			 WHERE u.tenant_id = $1
			   AND (lower(u.email) LIKE $2 OR lower(coalesce(u.display_name,'')) LIKE $2)
			   AND u.status::text LIKE $3
			 ORDER BY u.created_at DESC
			 LIMIT $4 OFFSET $5`,
			tenantID, q, status, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var r UserRow
			if err := rows.Scan(&r.ID, &r.Email, &r.DisplayName, &r.Status, &r.RiskLevel,
				&r.CreatedAt, &r.LastLoginAt, &r.SubCount, &r.ActiveSub,
				&r.Balance, &r.Currency); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}
	return out, total, nil
}

type UserDetail struct {
	UserRow
	EmailVerified bool              `json:"email_verified"`
	Subscriptions []SubscriptionRow `json:"subscriptions"`
	Orders        []OrderRow        `json:"recent_orders"`
	Roles         []string          `json:"roles"`
}

type SubscriptionRow struct {
	ID          string     `json:"id"`
	PlanName    string     `json:"plan_name"`
	PlanVersion int        `json:"plan_version"`
	Status      string     `json:"status"`
	PeriodEnd   *time.Time `json:"current_period_end"`
	Amount      int64      `json:"amount"`
	Currency    string     `json:"currency"`
	AutoRenew   bool       `json:"auto_renew"`
}

func (s *Service) GetUser(ctx context.Context, tenantID, userID string) (*UserDetail, error) {
	var d UserDetail

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var verifiedAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT u.id, u.email, u.display_name, u.status, u.risk_level,
			       u.created_at, u.last_login_at, u.email_verified_at,
			       coalesce((SELECT -la.balance_signed FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 0),
			       coalesce((SELECT la.currency FROM ledger_accounts la
			                  WHERE la.owner_user_id = u.id
			                    AND la.account_type = 'user_balance' LIMIT 1), 'CNY'),
			       coalesce((SELECT g.name FROM user_groups g
			                  WHERE g.id = u.user_group_id), '')
			  FROM users u WHERE u.tenant_id = $1 AND u.id = $2`,
			tenantID, userID,
		).Scan(&d.ID, &d.Email, &d.DisplayName, &d.Status, &d.RiskLevel,
			&d.CreatedAt, &d.LastLoginAt, &verifiedAt, &d.Balance, &d.Currency,
			&d.GroupName)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		d.EmailVerified = verifiedAt != nil

		d.Subscriptions = []SubscriptionRow{}
		srows, err := tx.Query(ctx, `
			SELECT s.id, pl.name, pv.version, s.status, s.current_period_end,
			       s.snapshot_amount, s.snapshot_currency, s.auto_renew
			  FROM subscriptions s
			  JOIN plans pl ON pl.id = s.plan_id
			  JOIN plan_versions pv ON pv.id = s.plan_version_id
			 WHERE s.tenant_id = $1 AND s.user_id = $2
			 ORDER BY s.created_at DESC`, tenantID, userID)
		if err != nil {
			return err
		}
		for srows.Next() {
			var r SubscriptionRow
			if err := srows.Scan(&r.ID, &r.PlanName, &r.PlanVersion, &r.Status,
				&r.PeriodEnd, &r.Amount, &r.Currency, &r.AutoRenew); err != nil {
				srows.Close()
				return err
			}
			d.Subscriptions = append(d.Subscriptions, r)
		}
		srows.Close()
		if err := srows.Err(); err != nil {
			return err
		}

		d.Orders = []OrderRow{}
		orows, err := tx.Query(ctx, `
			SELECT o.id, o.order_no, o.kind, o.status, o.currency,
			       o.total_amount, o.payable_amount, o.paid_amount, o.refunded_amount,
			       o.created_at, o.paid_at, u.email
			  FROM orders o JOIN users u ON u.id = o.user_id
			 WHERE o.tenant_id = $1 AND o.user_id = $2
			 ORDER BY o.created_at DESC LIMIT 20`, tenantID, userID)
		if err != nil {
			return err
		}
		for orows.Next() {
			var r OrderRow
			if err := orows.Scan(&r.ID, &r.OrderNo, &r.Kind, &r.Status, &r.Currency,
				&r.TotalAmount, &r.PayableAmount, &r.PaidAmount, &r.RefundedAmount,
				&r.CreatedAt, &r.PaidAt, &r.UserEmail); err != nil {
				orows.Close()
				return err
			}
			d.Orders = append(d.Orders, r)
		}
		orows.Close()
		if err := orows.Err(); err != nil {
			return err
		}

		d.Roles = []string{}
		rrows, err := tx.Query(ctx, `
			SELECT r.code FROM role_bindings rb JOIN roles r ON r.id = rb.role_id
			 WHERE rb.tenant_id = $1 AND rb.user_id = $2
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			 ORDER BY r.code`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var c string
			if err := rrows.Scan(&c); err != nil {
				return err
			}
			d.Roles = append(d.Roles, c)
		}
		return rrows.Err()
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return nil, err
		}
		return nil, httpx.Internal(err)
	}
	return &d, nil
}

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
			ct, err := tx.Exec(ctx, `
				UPDATE sessions SET revoked_at = now(), revoked_reason = $3
				 WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
				tenantID, userID, "账号状态变更为 "+status)
			if err != nil {
				return err
			}
			revoked = ct.RowsAffected()
			if _, err := tx.Exec(ctx, `
				UPDATE refresh_tokens SET status = 'revoked'
				 WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`,
				tenantID, userID); err != nil {
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

//==============================================================================
// 订单
//==============================================================================

type OrderRow struct {
	ID             string     `json:"id"`
	OrderNo        string     `json:"order_no"`
	UserEmail      string     `json:"user_email"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	Currency       string     `json:"currency"`
	TotalAmount    int64      `json:"total_amount"`
	PayableAmount  int64      `json:"payable_amount"`
	PaidAmount     int64      `json:"paid_amount"`
	RefundedAmount int64      `json:"refunded_amount"`
	CreatedAt      time.Time  `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at"`

	// 首个订单项的套餐快照。列表页要回答的第一个问题是「这单买的什么」，
	// 以前只能点进详情才知道。取快照而不是现在的套餐名：套餐改名或下架
	// 之后，历史订单显示的仍然是下单当时那个名字。
	PlanName      string `json:"plan_name"`
	Interval      string `json:"interval"`
	IntervalCount int32  `json:"interval_count"`
	// ItemCount > 1 时列表只显示第一项，由前端提示还有几项。
	ItemCount int32 `json:"item_count"`
}

type ListOrdersInput struct {
	Query  string
	Status string
	// From / To 按下单时间过滤，都是闭区间，为 nil 表示不限。
	// 对账时最常用的就是「这个月的单」，原来只能靠翻页找。
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}

func (s *Service) ListOrders(ctx context.Context, tenantID string, in ListOrdersInput) ([]OrderRow, int64, error) {
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 25
	}
	out := []OrderRow{}
	var total int64

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		q := "%" + strings.ToLower(strings.TrimSpace(in.Query)) + "%"
		if strings.TrimSpace(in.Query) == "" {
			q = "%"
		}
		status := in.Status
		if status == "" {
			status = "%"
		}

		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM orders o JOIN users u ON u.id = o.user_id
			 WHERE o.tenant_id = $1
			   AND (lower(o.order_no) LIKE $2 OR lower(u.email) LIKE $2)
			   AND o.status::text LIKE $3
			   AND ($4::timestamptz IS NULL OR o.created_at >= $4)
			   AND ($5::timestamptz IS NULL OR o.created_at < $5)`,
			tenantID, q, status, in.From, in.To).Scan(&total); err != nil {
			return err
		}

		// 套餐名走 LATERAL 取首项而不是 GROUP BY 聚合：一单多项时
		// 聚合出来的是拼接串，长度不可控，会把表格挤变形。取第一项
		// 加个「等 N 项」，想看全部就点详情。
		rows, err := tx.Query(ctx, `
			SELECT o.id, o.order_no, u.email, o.kind, o.status, o.currency,
			       o.total_amount, o.payable_amount, o.paid_amount, o.refunded_amount,
			       o.created_at, o.paid_at,
			       COALESCE(it.snapshot_plan_name, ''), COALESCE(it.snapshot_interval, ''),
			       COALESCE(it.snapshot_interval_count, 0), COALESCE(it.n, 0)
			  FROM orders o JOIN users u ON u.id = o.user_id
			  LEFT JOIN LATERAL (
			    SELECT i.snapshot_plan_name, i.snapshot_interval, i.snapshot_interval_count,
			           count(*) OVER () AS n
			      FROM order_items i
			     WHERE i.tenant_id = o.tenant_id AND i.order_id = o.id
			     ORDER BY i.created_at, i.id
			     LIMIT 1
			  ) it ON true
			 WHERE o.tenant_id = $1
			   AND (lower(o.order_no) LIKE $2 OR lower(u.email) LIKE $2)
			   AND o.status::text LIKE $3
			   AND ($4::timestamptz IS NULL OR o.created_at >= $4)
			   AND ($5::timestamptz IS NULL OR o.created_at < $5)
			 ORDER BY o.created_at DESC
			 LIMIT $6 OFFSET $7`,
			tenantID, q, status, in.From, in.To, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var r OrderRow
			if err := rows.Scan(&r.ID, &r.OrderNo, &r.UserEmail, &r.Kind, &r.Status,
				&r.Currency, &r.TotalAmount, &r.PayableAmount, &r.PaidAmount,
				&r.RefundedAmount, &r.CreatedAt, &r.PaidAt,
				&r.PlanName, &r.Interval, &r.IntervalCount, &r.ItemCount); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}
	return out, total, nil
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
			       (SELECT dpv.id FROM plan_versions dpv
			         WHERE dpv.tenant_id=pl.tenant_id AND dpv.plan_id=pl.id AND dpv.status='draft'
			         LIMIT 1),
			       pv.version, pv.max_devices,
			       (SELECT qd.limit_value FROM quota_definitions qd
			         WHERE qd.plan_version_id = pv.id AND qd.metric = 'traffic.bytes' LIMIT 1),
			       (SELECT count(*) FROM subscriptions s
			         WHERE s.plan_id = pl.id AND s.status IN ('active','trialing')),
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
				&p.CurrentVersionID, &p.DraftVersionID, &p.Version, &p.MaxDevices,
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

//==============================================================================
// 支付渠道
//==============================================================================

type ProviderRow struct {
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Adapter      string   `json:"adapter"`
	DisplayName  string   `json:"display_name"`
	Enabled      bool     `json:"enabled"`
	AcceptingNew bool     `json:"accepting_new"`
	HasCreds     bool     `json:"has_credentials"`
	BaseURL      string   `json:"base_url"`
	Currencies   []string `json:"currencies"`
}

func (s *Service) ListProviders(ctx context.Context, tenantID string) ([]ProviderRow, error) {
	out := []ProviderRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, code, adapter, display_name, enabled, accepting_new,
			       credentials_encrypted IS NOT NULL,
			       coalesce(config->>'base_url', ''),
			       coalesce(supported_currencies, '{}')
			  FROM payment_providers WHERE tenant_id = $1 ORDER BY code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ProviderRow
			if err := rows.Scan(&p.ID, &p.Code, &p.Adapter, &p.DisplayName,
				&p.Enabled, &p.AcceptingNew, &p.HasCreds, &p.BaseURL,
				&p.Currencies); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// SetProviderEnabled 启停渠道。
// accepting_new 单独控制：渠道故障时先停收单但保留回调处理能力（PAY-009）。
func (s *Service) SetProviderEnabled(ctx context.Context, tenantID, actorID, code string, enabled, acceptingNew bool) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var id string
		var beforeEnabled, beforeAccepting bool
		if err := tx.QueryRow(ctx,
			`SELECT id, enabled, accepting_new FROM payment_providers
			  WHERE tenant_id = $1 AND code = $2 FOR UPDATE`,
			tenantID, code).Scan(&id, &beforeEnabled, &beforeAccepting); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		if _, err := tx.Exec(ctx,
			`UPDATE payment_providers SET enabled = $2, accepting_new = $3 WHERE id = $1`,
			id, enabled, acceptingNew); err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "payment_provider.toggle", ResourceType: "payment_provider", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			BeforeDigest: map[string]any{"enabled": beforeEnabled, "accepting_new": beforeAccepting},
			AfterDigest:  map[string]any{"enabled": enabled, "accepting_new": acceptingNew},
			RequestID:    httpx.RequestIDFrom(ctx),
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

//==============================================================================
// 审计
//==============================================================================

type AuditRow struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	ActorKind    string    `json:"actor_kind"`
	ActorEmail   *string   `json:"actor_email"`
	Action       string    `json:"action"`
	ResourceType *string   `json:"resource_type"`
	ResourceID   *string   `json:"resource_id"`
	APIDomain    *string   `json:"api_domain"`
	Outcome      string    `json:"outcome"`
	// Reason 从 after_digest 里取：审计表没有独立列，
	// 但「为什么这么改」是管理端排查时最想先看到的一列
	Reason *string `json:"reason"`
}

// auditCond 是审计查询的筛选条件，计数与取行共用同一份。
// 分开写迟早会出现「总数按全量算、列表按筛选取」，翻到后面全是空页。
const auditCond = `
			 WHERE a.tenant_id = $1
			   AND ($2 = '' OR a.action LIKE $2 || '%')
			   AND ($3 = '' OR a.actor_kind = $3)
			   AND ($4 = '' OR a.outcome = $4)`

// AuditFilter 是审计日志的可选筛选条件。零值表示不筛。
//
// 自动任务（order.expired 之类）的量远大于人工操作——压测跑完这张表
// 一万五千条，翻开全是它。没有筛选的话，这份日志实际上没法用来追查。
type AuditFilter struct {
	// ActionPrefix 按动作前缀匹配。动作是 order.expired / payment.succeeded
	// 这种带命名空间的串，前缀能一次圈定一整类。
	ActionPrefix string
	// ActorKind 区分 system 与 user，把自动任务和人工操作分开看。
	ActorKind string
	Outcome   string
}

func (s *Service) ListAudit(ctx context.Context, tenantID string, limit, offset int, f AuditFilter) ([]AuditRow, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []AuditRow{}
	var total int64

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_events a`+auditCond,
			tenantID, f.ActionPrefix, f.ActorKind, f.Outcome).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT a.id, a.occurred_at, a.actor_kind, u.email, a.action,
			       a.resource_type, a.resource_id::text, a.api_domain, a.outcome,
			       a.after_digest->>'reason' 
			  FROM audit_events a
			  LEFT JOIN users u ON u.id = a.actor_id
			 `+auditCond+`
			 ORDER BY a.occurred_at DESC
			 LIMIT $5 OFFSET $6`,
			tenantID, f.ActionPrefix, f.ActorKind, f.Outcome, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r AuditRow
			if err := rows.Scan(&r.ID, &r.OccurredAt, &r.ActorKind, &r.ActorEmail,
				&r.Action, &r.ResourceType, &r.ResourceID, &r.APIDomain,
				&r.Outcome, &r.Reason); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, httpx.Internal(err)
	}
	return out, total, nil
}

//==============================================================================
// 系统
//==============================================================================

type SwitchRow struct {
	Code      string  `json:"code"`
	Enabled   bool    `json:"enabled"`
	Essential bool    `json:"essential"`
	Reason    *string `json:"reason"`
}

func (s *Service) ListSwitches(ctx context.Context, tenantID string) ([]SwitchRow, error) {
	out := []SwitchRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT code, enabled, essential, reason FROM feature_switches
			  WHERE tenant_id = $1 ORDER BY essential DESC, code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r SwitchRow
			if err := rows.Scan(&r.Code, &r.Enabled, &r.Essential, &r.Reason); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// SetSwitch 切换降级开关（NFR-008）。
// essential 的三项由数据库触发器挡住，这里不重复判断，
// 让唯一的真相来源留在约束里 —— 但要把数据库的报错翻译成人话。
func (s *Service) SetSwitch(ctx context.Context, tenantID, actorID, code string, enabled bool, reason string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var before bool
		if err := tx.QueryRow(ctx,
			`SELECT enabled FROM feature_switches WHERE tenant_id = $1 AND code = $2 FOR UPDATE`,
			tenantID, code).Scan(&before); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE feature_switches SET enabled = $3, reason = $4
			 WHERE tenant_id = $1 AND code = $2`,
			tenantID, code, enabled, nullIfEmpty(reason)); err != nil {
			if db.IsCheckViolation(err) || db.IsInsufficientPrivilege(err) {
				return httpx.New(httpx.CodeConflict,
					fmt.Sprintf("开关 %s 不允许该操作：%s", code, db.Message(err)))
			}
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "feature_switch.toggle", ResourceType: "feature_switch",
			APIDomain: "admin", Outcome: "success",
			BeforeDigest: map[string]any{"enabled": before},
			AfterDigest:  map[string]any{"enabled": enabled, "reason": reason},
			RequestID:    httpx.RequestIDFrom(ctx),
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

func nullIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
