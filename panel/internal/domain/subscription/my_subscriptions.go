package subscription

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/period"
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
	// PackRemainingBytes 是挂在这一份上的流量包余量（购买模型统一：流量包按份挂）
	PackRemainingBytes int64 `json:"pack_remaining_bytes"`
	// Label 是用户起的备注名，没起为 null
	Label *string `json:"label"`
	// ClientName 是这一份在 App 里显示的配置名（ProfileName），与订阅下载的
	// Content-Disposition 同一来源
	ClientName string `json:"client_name"`
	// Changeable 与付费换套餐、续费同一口径（billing.subscriptionAcceptsPaidChange）：
	// 生效中的四种状态，外加过期 30 天内、窗口没关的；不看套餐是否允许续费
	Changeable bool `json:"changeable"`
	// RenewUntil 是按 renewal_price 续一期会到哪天：生效中的从当前到期日起算，已过期的
	// 从现在起算（与履约 renewalBase 同口径）；不能续（不可续或价格不可用）时为 null
	RenewUntil *time.Time `json:"renew_until"`
}

// MySubscriptionList 是门户「我的套餐」：每一份，加上还没加到任何一份的流量包余量。
type MySubscriptionList struct {
	Subscriptions []MySubscription
	// UnattachedPackBytes 是「未分配」的流量包余量：没有订阅时兑换的送流量卡。门户据它提示
	// 「有 xG 流量包还没加到任何一份」
	UnattachedPackBytes int64
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
// 每一份的流量包余量按订阅走一个 LATERAL（每份一次 idx_traffic_pack_grants_open_sub 探测），
// 不读转移流水；未分配的余量与订阅无关，写成不相关子查询，整条语句只算一次（InitPlan）。
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
	       s.status IN ('active','trialing','grace','past_due')
	        OR (s.status = 'expired' AND s.renewal_closed_at IS NULL),
	       pr.id::text, pr.currency::text, pr.unit_amount, pr.billing_interval, pr.interval_count,
	       coalesce(pr.status = 'active' AND pr.currency IN ('CNY','USD')
	                AND pr.product_id = pl.product_id
	                AND (pr.user_group_id IS NULL OR pr.user_group_id = u.user_group_id)
	                AND (pr.valid_from IS NULL OR pr.valid_from <= now())
	                AND (pr.valid_until IS NULL OR pr.valid_until > now()), false),
	       s.label,
	       pk.pack_left,
	       (` + unattachedPacksSQL + `)
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
	  CROSS JOIN LATERAL (
	        SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint AS pack_left
	          FROM traffic_pack_grants g
	         WHERE g.tenant_id = s.tenant_id AND g.subscription_id = s.id
	           AND g.consumed_bytes < g.granted_bytes
	  ) pk
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

// unattachedPacksSQL 是本人「未分配」的流量包余量，$1 租户、$2 本人。
const unattachedPacksSQL = `SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
	          FROM traffic_pack_grants g
	         WHERE g.tenant_id = $1 AND g.user_id = $2 AND g.subscription_id IS NULL
	           AND g.consumed_bytes < g.granted_bytes`

// MySubscriptions 列出本人的全部订阅（新建在前）与未分配的流量包余量。
// 列表为空时 Subscriptions 是 []MySubscription{}。
//
// 有订阅时是两条查询：主查询（未分配余量作为 InitPlan 带出）加一次取齐的配额；
// 一份订阅都没有时主查询没有行，未分配余量另用一条小查询取。
func (s *Service) MySubscriptions(ctx context.Context, tenantID, userID string) (MySubscriptionList, error) {
	out := MySubscriptionList{Subscriptions: []MySubscription{}}
	now := time.Now()

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
					&v.Renewable, &v.Changeable, &priceID, &priceCurrency, &unitAmount, &interval, &intervalCount,
					&available, &v.Label, &v.PackRemainingBytes, &out.UnattachedPackBytes); err != nil {
					rows.Close()
					return err
				}
				if priceID != nil {
					v.RenewalPrice = &MyRenewalPrice{ID: *priceID, Currency: *priceCurrency,
						UnitAmount: *unitAmount, BillingInterval: *interval, IntervalCount: *intervalCount,
						Available: available}
				}
				v.RenewUntil = renewUntil(v.Renewable, v.RenewalPrice, v.PeriodEnd, now)
				out.Subscriptions = append(out.Subscriptions, v)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if len(out.Subscriptions) == 0 {
				return tx.QueryRow(ctx, unattachedPacksSQL, tenantID, userID).Scan(&out.UnattachedPackBytes)
			}

			ids := make([]string, len(out.Subscriptions))
			index := make(map[string]int, len(out.Subscriptions))
			for i := range out.Subscriptions {
				ids[i] = out.Subscriptions[i].ID
				index[out.Subscriptions[i].ID] = i
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
					out.Subscriptions[i].Quotas = append(out.Subscriptions[i].Quotas, q)
				}
			}
			return qrows.Err()
		})
	if err != nil {
		return MySubscriptionList{}, err
	}
	// 配置名：站点名按租户缓存（与订阅下载同一份），不在事务里读
	site := s.SiteName(ctx, tenantID)
	for i := range out.Subscriptions {
		v := &out.Subscriptions[i]
		label := ""
		if v.Label != nil {
			label = *v.Label
		}
		v.ClientName = ProfileName(site, label, v.PlanName)
	}
	return out, nil
}

// renewUntil 是按当前价格档续一期会到哪天：与履约（billing renewalBase）同口径，
// 到期日还在将来的接在到期日后，已经到期（或没有到期日）的从现在起算。
// 不可续、没有价格档或价格档不可用时为 nil。
func renewUntil(renewable bool, price *MyRenewalPrice, periodEnd *time.Time, now time.Time) *time.Time {
	if !renewable || price == nil || !price.Available {
		return nil
	}
	base := now.UTC()
	if periodEnd != nil && periodEnd.After(base) {
		base = periodEnd.UTC()
	}
	end := period.AddInterval(base, price.BillingInterval, price.IntervalCount).Truncate(time.Microsecond)
	return &end
}
