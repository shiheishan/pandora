// [INPUT]: 依赖 platform 的 db/httpx/audit，依赖 billing 的销售能力注入与 ParseOrderStatuses 订单状态白名单
// [OUTPUT]: 对外提供 Service、NewService，概览、用户（ListUsers / GetUser / SetUserStatus）、订单（ListOrders，OrderRow 唯一查询形状）、套餐与渠道、降级开关
// [POS]: domain/adminops 的主服务：后台读写用例的入口，其余同包文件按专题扩展它；套餐目录在 catalog.go / plan_wizard*.go，订单详情在 order_detail.go，审计在 audit.go；订单行的品名对流量包订单取订单项商品名；revokeUserLogins 是停用账号即下线的唯一实现，改状态与 risk.go 的批量停用共用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
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
			       count(*) FILTER (WHERE status = 'expired'),
			       count(*) FILTER (WHERE status IN ('active','trialing')
			                          AND created_at >= now() - interval '7 days')
			  FROM subscriptions WHERE tenant_id = $1`, tenantID,
		).Scan(&o.Subscriptions.Active, &o.Subscriptions.Trialing,
			&o.Subscriptions.Expiring, &o.Subscriptions.Expired, &o.Subscriptions.New7Days); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds')
			  FROM nodes
			 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status <> 'retired'`, tenantID,
		).Scan(&o.Nodes.Total, &o.Nodes.Online); err != nil {
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
			var adjYesterday int64
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
				        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END),0)::bigint,
				        coalesce(sum(CASE WHEN le.direction='credit' THEN le.amount ELSE -le.amount END)
				          FILTER(WHERE (lt.occurred_at AT TIME ZONE $3)::date=p.today-1),0)::bigint
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
				) SELECT * FROM actual CROSS JOIN adj`, tenantID, currency, revenueTimezone).
				Scan(&r.ActualToday, &r.Actual7Days, &r.Actual30Days, &r.ActualTotal, &r.ActualYesterday,
					&r.AdjustmentToday, &r.Adjustment7Days, &r.Adjustment30Days, &r.AdjustmentTotal, &adjYesterday); err != nil {
				return err
			}
			r.Yesterday = r.ActualYesterday + adjYesterday
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
	BalanceApplied int64      `json:"balance_applied"`
	CreatedAt      time.Time  `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at"`
	// 收款渠道：最近一笔入账的渠道，没有入账时取最近一次支付尝试的渠道；
	// 两者都没有（全额余额、赠送、人工单）时为空，列表按余额 / 人工兜底显示。
	ProviderCode *string `json:"provider_code"`
	ProviderName *string `json:"provider_name"`

	// 首个订单项的套餐快照。列表页要回答的第一个问题是「这单买的什么」，
	// 以前只能点进详情才知道。取快照而不是现在的套餐名：套餐改名或下架
	// 之后，历史订单显示的仍然是下单当时那个名字。
	PlanName      string `json:"plan_name"`
	Interval      string `json:"interval"`
	IntervalCount int32  `json:"interval_count"`
	// ItemCount > 1 时列表只显示第一项，由前端提示还有几项。
	ItemCount int32 `json:"item_count"`
}

// orderRowSelectSQL 是 OrderRow 的唯一查询形状，调用方只拼 WHERE / ORDER / LIMIT。
//
// 套餐名走 LATERAL 取首项而不是 GROUP BY 聚合：一单多项时聚合出来的是
// 拼接串，长度不可控，会把表格挤变形。取第一项加个「等 N 项」，想看全部
// 就点详情。count(*) OVER () 在 LIMIT 之前算，所以 n 是全部项数。
const orderRowSelectSQL = `
			SELECT o.id, o.order_no, u.email, o.kind, o.status, o.currency,
			       o.total_amount, o.payable_amount, o.paid_amount, o.refunded_amount,
			       o.balance_applied, o.created_at, o.paid_at, pv.code, pv.display_name,
			       COALESCE(it.snapshot_plan_name, ''), COALESCE(it.snapshot_interval, ''),
			       COALESCE(it.snapshot_interval_count, 0), COALESCE(it.n, 0)
			  FROM orders o JOIN users u ON u.id = o.user_id
			  LEFT JOIN LATERAL (
			    -- 入账优先于支付尝试：换过渠道的单，钱最终从哪个渠道进来才算数
			    SELECT pp.code, pp.display_name
			      FROM ((SELECT p.provider_id, 0 AS rank FROM payments p
			              WHERE p.tenant_id = o.tenant_id AND p.order_id = o.id
			              ORDER BY p.paid_at DESC, p.id DESC LIMIT 1)
			            UNION ALL
			            (SELECT pi.provider_id, 1 FROM payment_intents pi
			              WHERE pi.tenant_id = o.tenant_id AND pi.order_id = o.id
			              ORDER BY pi.created_at DESC, pi.id DESC LIMIT 1)) c
			      JOIN payment_providers pp ON pp.tenant_id = o.tenant_id AND pp.id = c.provider_id
			     ORDER BY c.rank LIMIT 1
			  ) pv ON true
			  LEFT JOIN LATERAL (
			    -- 流量包订单没有套餐名，用订单项上的商品名（流量包名）顶上
			    SELECT coalesce(i.snapshot_plan_name, i.snapshot_product_name) AS snapshot_plan_name,
			           i.snapshot_interval, i.snapshot_interval_count,
			           count(*) OVER () AS n
			      FROM order_items i
			     WHERE i.tenant_id = o.tenant_id AND i.order_id = o.id
			     ORDER BY i.created_at, i.id
			     LIMIT 1
			  ) it ON true`

func scanOrderRow(row pgx.Row) (OrderRow, error) {
	var r OrderRow
	err := row.Scan(&r.ID, &r.OrderNo, &r.UserEmail, &r.Kind, &r.Status,
		&r.Currency, &r.TotalAmount, &r.PayableAmount, &r.PaidAmount,
		&r.RefundedAmount, &r.BalanceApplied, &r.CreatedAt, &r.PaidAt,
		&r.ProviderCode, &r.ProviderName, &r.PlanName, &r.Interval, &r.IntervalCount, &r.ItemCount)
	return r, err
}

type ListOrdersInput struct {
	Query string
	// Status 逗号分隔多值，按枚举白名单精确匹配（billing.ParseOrderStatuses），
	// 未知值回 400；以前原样当 LIKE 模式，% 与 _ 都是通配符。
	Status string
	// UserID 精确筛一个用户的订单（用户详情 › 订单 tab），空 = 不限。
	UserID string
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
	// 负 offset 曾经直接进 SQL 报错回 500
	in.Offset = max(in.Offset, 0)
	statuses, err := billing.ParseOrderStatuses(in.Status)
	if err != nil {
		return nil, 0, err
	}
	var userID *string
	if u := strings.TrimSpace(in.UserID); u != "" {
		if _, err := uuid.Parse(u); err != nil {
			return nil, 0, httpx.New(httpx.CodeBadRequest, "用户 ID 格式不正确")
		}
		userID = &u
	}
	out := []OrderRow{}
	var total int64

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		q := "%" + strings.ToLower(strings.TrimSpace(in.Query)) + "%"
		if strings.TrimSpace(in.Query) == "" {
			q = "%"
		}
		const where = `
			 WHERE o.tenant_id = $1
			   AND (lower(o.order_no) LIKE $2 OR lower(u.email) LIKE $2)
			   AND ($3::text[] IS NULL OR o.status::text = ANY($3))
			   AND ($4::timestamptz IS NULL OR o.created_at >= $4)
			   AND ($5::timestamptz IS NULL OR o.created_at < $5)
			   AND ($6::uuid IS NULL OR o.user_id = $6)`

		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM orders o JOIN users u ON u.id = o.user_id`+where,
			tenantID, q, statuses, in.From, in.To, userID).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, orderRowSelectSQL+where+`
			 ORDER BY o.created_at DESC
			 LIMIT $7 OFFSET $8`,
			tenantID, q, statuses, in.From, in.To, userID, in.Limit, in.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			r, err := scanOrderRow(rows)
			if err != nil {
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
	// 渠道卡统计（后台-05）。Today 是租户时区今天成功入账的金额，按币种分开
	// ——与挂账合计同一个理由，分和美分不能相加。SuccessRate24h 是近 24 小时
	// 进入终态的支付尝试里成功的比例，没有样本为 nil；LastCallbackAt 是最近
	// 一条渠道回调（payment_events）的接收时间。
	Today          map[string]int64 `json:"today"`
	SuccessRate24h *float64         `json:"success_rate_24h"`
	LastCallbackAt *time.Time       `json:"last_callback_at"`
}

func (s *Service) ListProviders(ctx context.Context, tenantID string) ([]ProviderRow, error) {
	out := []ProviderRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT pp.id, pp.code, pp.adapter, pp.display_name, pp.enabled, pp.accepting_new,
			       pp.credentials_encrypted IS NOT NULL,
			       coalesce(pp.config->>'base_url', ''),
			       coalesce(pp.supported_currencies, '{}'),
			       coalesce((SELECT jsonb_object_agg(d.currency, d.amount)
			                   FROM (SELECT p.currency::text AS currency, sum(p.amount) AS amount
			                           FROM payments p
			                          WHERE p.tenant_id = pp.tenant_id AND p.provider_id = pp.id
			                            AND p.status = 'succeeded'
			                            AND (p.paid_at AT TIME ZONE t.timezone)::date
			                                = (now() AT TIME ZONE t.timezone)::date
			                          GROUP BY p.currency) d), '{}'::jsonb),
			       (SELECT (count(*) FILTER (WHERE pi.status = 'succeeded'))::float8
			               / nullif(count(*), 0)
			          FROM payment_intents pi
			         WHERE pi.tenant_id = pp.tenant_id AND pi.provider_id = pp.id
			           AND pi.status IN ('succeeded','failed','cancelled','expired')
			           AND pi.updated_at > now() - interval '24 hours'),
			       (SELECT max(e.received_at) FROM payment_events e
			         WHERE e.tenant_id = pp.tenant_id AND e.provider_id = pp.id)
			  FROM payment_providers pp
			  JOIN tenants t ON t.id = pp.tenant_id
			 WHERE pp.tenant_id = $1 ORDER BY pp.code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ProviderRow
			if err := rows.Scan(&p.ID, &p.Code, &p.Adapter, &p.DisplayName,
				&p.Enabled, &p.AcceptingNew, &p.HasCreds, &p.BaseURL,
				&p.Currencies, &p.Today, &p.SuccessRate24h, &p.LastCallbackAt); err != nil {
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
