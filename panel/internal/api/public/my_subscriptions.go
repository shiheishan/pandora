// [INPUT]: 依赖 platform 的 db/httpx；读 subscriptions / plans / plan_versions / prices / users / quota_balances / subscription_online_devices / traffic_pack_grants
// [OUTPUT]: 对外提供 handlers.listSubscriptions
// [POS]: api/public 的「我的订阅」（契约门户-02 GET v1/me/subscriptions）：从 handlers.go 拆出，订阅带设备、配额周期、重置、可续费与续费价、流量包余量
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type myQuotaView struct {
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

type myRenewalPrice struct {
	ID              string `json:"id"`
	Currency        string `json:"currency"`
	UnitAmount      int64  `json:"unit_amount"`
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	// Available 与续费下单（billing/renewal.go）的价格检查同一口径：价格仍在售、
	// 属于本套餐的产品、组价与用户组相符、在有效期内
	Available bool `json:"available"`
}

type mySubscriptionView struct {
	ID string `json:"id"`
	// 续费界面靠这两个 ID 定位套餐与当前价格档，
	// 好把「同一套餐下的其它周期」列出来给用户选
	PlanID      string        `json:"plan_id"`
	PriceID     string        `json:"price_id"`
	PlanName    string        `json:"plan_name"`
	PlanVersion int           `json:"plan_version"`
	Status      string        `json:"status"`
	PeriodStart *time.Time    `json:"current_period_start"`
	PeriodEnd   *time.Time    `json:"current_period_end"`
	Currency    string        `json:"currency"`
	Amount      int64         `json:"amount"`
	Quotas      []myQuotaView `json:"quotas"`
	// DeviceLimit 是生效上限：订阅覆盖 → 套餐版本上限；null 表示不限
	DeviceLimit        *int            `json:"device_limit"`
	OnlineDevices      int             `json:"online_devices"`
	QuotaResetStrategy string          `json:"quota_reset_strategy"`
	NextResetAt        *time.Time      `json:"next_reset_at"`
	Renewable          bool            `json:"renewable"`
	RenewalPrice       *myRenewalPrice `json:"renewal_price"`
	// PackRemainingBytes 是用户的流量包余量：流量包挂在用户上，几条订阅显示同一个数
	PackRemainingBytes int64 `json:"pack_remaining_bytes"`
}

func (h *handlers) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.PrincipalFrom(ctx)
	out := []mySubscriptionView{}

	err := h.d.Pool.InTx(ctx, db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			var packRemaining int64
			if err := tx.QueryRow(ctx, `
				SELECT coalesce(sum(granted_bytes - consumed_bytes), 0)::bigint FROM traffic_pack_grants
				 WHERE tenant_id = $1 AND user_id = $2 AND consumed_bytes < granted_bytes`,
				p.TenantID, p.UserID).Scan(&packRemaining); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `
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
				       s.status IN ('active','trialing','grace','past_due') AND pl.allow_renewal,
				       pr.id::text, pr.currency::text, pr.unit_amount, pr.billing_interval, pr.interval_count,
				       coalesce(pr.status = 'active' AND pr.currency IN ('CNY','USD')
				                AND pr.product_id = pl.product_id
				                AND (pr.user_group_id IS NULL OR pr.user_group_id = u.user_group_id)
				                AND (pr.valid_from IS NULL OR pr.valid_from <= now())
				                AND (pr.valid_until IS NULL OR pr.valid_until > now()), false)
				  FROM subscriptions s
				  JOIN plans pl         ON pl.id = s.plan_id
				  JOIN plan_versions pv ON pv.id = s.plan_version_id
				  JOIN users u          ON u.tenant_id = s.tenant_id AND u.id = s.user_id
				  LEFT JOIN prices pr   ON pr.tenant_id = s.tenant_id AND pr.id = s.price_id
				  LEFT JOIN subscription_online_devices od
				         ON od.tenant_id = s.tenant_id AND od.subscription_id = s.id
				 WHERE s.tenant_id = $1 AND s.user_id = $2
				 ORDER BY s.created_at DESC`,
				p.TenantID, p.UserID)
			if err != nil {
				return err
			}
			for rows.Next() {
				v := mySubscriptionView{Quotas: []myQuotaView{}, PackRemainingBytes: packRemaining}
				var priceID, priceCurrency, interval *string
				var unitAmount *int64
				var intervalCount *int
				var available bool
				if err := rows.Scan(&v.ID, &v.PlanID, &v.PriceID,
					&v.PlanName, &v.PlanVersion, &v.Status,
					&v.PeriodStart, &v.PeriodEnd, &v.Currency, &v.Amount,
					&v.DeviceLimit, &v.OnlineDevices, &v.QuotaResetStrategy, &v.NextResetAt,
					&v.Renewable, &priceID, &priceCurrency, &unitAmount, &interval, &intervalCount,
					&available); err != nil {
					rows.Close()
					return err
				}
				if priceID != nil {
					v.RenewalPrice = &myRenewalPrice{ID: *priceID, Currency: *priceCurrency,
						UnitAmount: *unitAmount, BillingInterval: *interval, IntervalCount: *intervalCount,
						Available: available}
				}
				out = append(out, v)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}

			for i := range out {
				qrows, err := tx.Query(ctx, `
					SELECT metric, limit_value, consumed, remaining,
					       period, period_start, period_end, granted_addon, adjusted
					  FROM quota_balances
					 WHERE tenant_id = $1 AND subscription_id = $2
					 ORDER BY metric, period_start DESC`,
					p.TenantID, out[i].ID)
				if err != nil {
					return err
				}
				quotas, err := pgx.CollectRows(qrows, pgx.RowToStructByPos[myQuotaView])
				if err != nil {
					return err
				}
				out[i].Quotas = append(out[i].Quotas, quotas...)
			}
			return nil
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, map[string]any{"subscriptions": out})
}
