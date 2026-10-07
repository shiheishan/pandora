package subscription

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// MyQuota 是一条订阅的一项配额余额（周期内）。
type MyQuota struct {
	Metric    string `json:"metric"`
	Limit     *int64 `json:"limit"`
	Consumed  int64  `json:"consumed"`
	Remaining *int64 `json:"remaining"`
	// 周期与追加 / 调整：前端画「已用 / 总量」和「N 天后重置」要用
	Period       string     `json:"period"`
	PeriodStart  time.Time  `json:"period_start"`
	PeriodEnd    *time.Time `json:"period_end"`
	GrantedAddon int64      `json:"granted_addon"`
	Adjusted     int64      `json:"adjusted"`
}

// MyRenewalPrice 是订阅当前价格档，续费界面用它定位「同一套餐下的其它周期」。
type MyRenewalPrice struct {
	ID              string `json:"id"`
	Currency        string `json:"currency"`
	UnitAmount      int64  `json:"unit_amount"`
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	// Available 与续费下单（billing/renewal.go）的价格检查同一口径：价格仍在售、
	// 属于本套餐的产品、组价与用户组相符、在有效期内
	Available bool `json:"available"`
}

// MySubscription 是门户「我的订阅」里的一条；Quotas 恒为非 nil。
type MySubscription struct {
	ID string `json:"id"`
	// 续费界面靠这两个 ID 定位套餐与当前价格档，
	// 好把「同一套餐下的其它周期」列出来给用户选
	PlanID      string     `json:"plan_id"`
	PriceID     string     `json:"price_id"`
	PlanName    string     `json:"plan_name"`
	PlanVersion int        `json:"plan_version"`
	Status      string     `json:"status"`
	PeriodStart *time.Time `json:"current_period_start"`
	PeriodEnd   *time.Time `json:"current_period_end"`
	Currency    string     `json:"currency"`
	Amount      int64      `json:"amount"`
	Quotas      []MyQuota  `json:"quotas"`
	// DeviceLimit 是生效上限：订阅覆盖 → 套餐版本上限；null 表示不限
	DeviceLimit        *int            `json:"device_limit"`
	OnlineDevices      int             `json:"online_devices"`
	QuotaResetStrategy string          `json:"quota_reset_strategy"`
	NextResetAt        *time.Time      `json:"next_reset_at"`
	Renewable          bool            `json:"renewable"`
	RenewalPrice       *MyRenewalPrice `json:"renewal_price"`
	// PackRemainingBytes 是用户的流量包余量：流量包挂在用户上，几条订阅显示同一个数
	PackRemainingBytes int64 `json:"pack_remaining_bytes"`
}

// mySubscriptionsSQL 是「我的订阅」的主查询，$1 租户、$2 本人。
//
// renewable 与续费建单（billing.subscriptionAcceptsPaidChange）同口径：生效中的四种状态，
// 外加过期 30 天内、原地续费窗口没关的（规则 4），且套餐允许续费。
//
// 在线设备数按订阅走 LATERAL：od.subscription_id = s.id 在子查询里是外层参数，
// 一定会被推进视图，每条订阅只探 idx_node_alive_recent 里自己那一段。原先是
// LEFT JOIN 整个视图，PostgreSQL 不把 JOIN 条件推进带聚合的视图，只看一个人的
// 订阅也要把全站在线记录聚合一遍（5000 用户实测 3.38s/次）。
//
// 流量包余量挂在用户上、与订阅无关，写成不相关子查询，整条语句只算一次。
const mySubscriptionsSQL = `
	SELECT s.id, s.plan_id::text, COALESCE(s.price_id::text, ''),
	       pl.name, pv.version, s.status,
	       s.current_period_start, s.current_period_end,
	       s.snapshot_currency, s.snapshot_amount,
	       coalesce(s.device_limit, pv.max_devices),
	       coalesce(od.device_count, 0)::int,
	       pv.quota_reset_strategy,
	       (SELECT q.period_end FROM quota_balances q
	         WHERE q.tenant_id = s.tenant_id AND q.subscription_id = s.id
	           AND q.metric = 'traffic.bytes'
	         ORDER BY q.period_start DESC LIMIT 1),
	       (s.status IN ('active','trialing','grace','past_due')
	        OR (s.status = 'expired' AND s.renewal_closed_at IS NULL)) AND pl.allow_renewal,
	       pr.id::text, pr.currency::text, pr.unit_amount, pr.billing_interval, pr.interval_count,
	       coalesce(pr.status = 'active' AND pr.currency IN ('CNY','USD')
	                AND pr.product_id = pl.product_id
	                AND (pr.user_group_id IS NULL OR pr.user_group_id = u.user_group_id)
	                AND (pr.valid_from IS NULL OR pr.valid_from <= now())
	                AND (pr.valid_until IS NULL OR pr.valid_until > now()), false),
	       (SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
	          FROM traffic_pack_grants g
	         WHERE g.tenant_id = $1 AND g.user_id = $2 AND g.consumed_bytes < g.granted_bytes)
	  FROM subscriptions s
	  JOIN plans pl         ON pl.id = s.plan_id
	  JOIN plan_versions pv ON pv.id = s.plan_version_id
	  JOIN users u          ON u.tenant_id = s.tenant_id AND u.id = s.user_id
	  LEFT JOIN prices pr   ON pr.tenant_id = s.tenant_id AND pr.id = s.price_id
	  LEFT JOIN LATERAL (
	        SELECT od.device_count
	          FROM subscription_online_devices od
	         WHERE od.tenant_id = s.tenant_id AND od.subscription_id = s.id
	  ) od ON true
	 WHERE s.tenant_id = $1 AND s.user_id = $2
	 ORDER BY s.created_at DESC`

// myQuotasSQL 一次取齐这些订阅的全部配额行（原先每条订阅各查一次），
// 每条订阅内部的顺序与原来相同：按指标、周期起点倒序。
const myQuotasSQL = `
	SELECT subscription_id::text, metric, limit_value, consumed, remaining,
	       period, period_start, period_end, granted_addon, adjusted
	  FROM quota_balances
	 WHERE tenant_id = $1 AND subscription_id = ANY($2::uuid[])
	 ORDER BY subscription_id, metric, period_start DESC`

// MySubscriptions 列出本人的全部订阅（新建在前）。列表为空时返回 []MySubscription{}。
func (s *Service) MySubscriptions(ctx context.Context, tenantID, userID string) ([]MySubscription, error) {
	out := []MySubscription{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, mySubscriptionsSQL, tenantID, userID)
			if err != nil {
				return err
			}
			for rows.Next() {
				v := MySubscription{Quotas: []MyQuota{}}
				var priceID, priceCurrency, interval *string
				var unitAmount *int64
				var intervalCount *int
				var available bool
				if err := rows.Scan(&v.ID, &v.PlanID, &v.PriceID,
					&v.PlanName, &v.PlanVersion, &v.Status,
					&v.PeriodStart, &v.PeriodEnd, &v.Currency, &v.Amount,
					&v.DeviceLimit, &v.OnlineDevices, &v.QuotaResetStrategy, &v.NextResetAt,
					&v.Renewable, &priceID, &priceCurrency, &unitAmount, &interval, &intervalCount,
					&available, &v.PackRemainingBytes); err != nil {
					rows.Close()
					return err
				}
				if priceID != nil {
					v.RenewalPrice = &MyRenewalPrice{ID: *priceID, Currency: *priceCurrency,
						UnitAmount: *unitAmount, BillingInterval: *interval, IntervalCount: *intervalCount,
						Available: available}
				}
				out = append(out, v)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(out) == 0 {
				return nil
			}

			ids := make([]string, len(out))
			index := make(map[string]int, len(out))
			for i := range out {
				ids[i] = out[i].ID
				index[out[i].ID] = i
			}
			qrows, err := tx.Query(ctx, myQuotasSQL, tenantID, ids)
			if err != nil {
				return err
			}
			defer qrows.Close()
			for qrows.Next() {
				var subID string
				var q MyQuota
				if err := qrows.Scan(&subID, &q.Metric, &q.Limit, &q.Consumed, &q.Remaining,
					&q.Period, &q.PeriodStart, &q.PeriodEnd, &q.GrantedAddon, &q.Adjusted); err != nil {
					return err
				}
				if i, ok := index[subID]; ok {
					out[i].Quotas = append(out[i].Quotas, q)
				}
			}
			return qrows.Err()
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}
