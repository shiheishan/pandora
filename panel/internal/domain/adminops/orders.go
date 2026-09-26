// [INPUT]: 依赖 platform/db、httpx，依赖 domain/billing 的 ParseOrderStatuses 订单状态白名单
// [OUTPUT]: 对外提供 OrderRow、ListOrdersInput、Service.ListOrders；包内提供 orderRowSelectSQL / scanOrderRow
// [POS]: adminops 的后台订单列表：从 service.go 拆出。orderRowSelectSQL / scanOrderRow 是 OrderRow 的唯一查询形状，users.go 与 order_detail.go 复用；带余额抵扣、收款渠道（入账优先、其次最近一次支付尝试）与人工单标识，流量包订单的品名取订单项商品名
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
	// Manual 是人工单标识（R95），与详情「来源」同口径：manual_reason 非空即后台开的单。
	// 赠送单没有渠道，列表靠它显示「人工」而不是「—」；开单人仍只在详情里。
	Manual bool `json:"manual"`
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
			       COALESCE(it.snapshot_interval_count, 0), COALESCE(it.n, 0),
			       o.manual_reason IS NOT NULL
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
		&r.ProviderCode, &r.ProviderName, &r.PlanName, &r.Interval, &r.IntervalCount, &r.ItemCount,
		&r.Manual)
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
