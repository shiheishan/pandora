package billing

// 落点（设计稿 2.3）：套餐卡、加时长卡、重置卡、送流量卡与后台开单「落到哪一份」由人选，
// 选项与默认值只由 purchase.Options 一处给出。这里取候选（一条 SQL）并给每个选项补上
// 「会发生什么」要的数（新到期日、原套餐没用完的部分）。

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/period"
)

// placementCandidatesSQL 一次取用户的全部订阅：套餐名、本期套餐流量（cycle / total 的
// traffic.bytes，不限量的那份上限记 0）与挂在这一份上的流量包余量。
const placementCandidatesSQL = `
	SELECT s.id::text, s.plan_id::text, pl.name, coalesce(s.label, ''),
	       s.status, s.renewal_closed_at IS NOT NULL, s.current_period_end,
	       coalesce(q.consumed, 0)::bigint, coalesce(q.cap, 0)::bigint, coalesce(pk.remaining, 0)::bigint
	  FROM subscriptions s
	  JOIN plans pl ON pl.tenant_id = s.tenant_id AND pl.id = s.plan_id
	  LEFT JOIN LATERAL (
	        SELECT sum(qb.consumed) AS consumed,
	               sum(qb.limit_value + qb.granted_addon + qb.adjusted) AS cap
	          FROM quota_balances qb
	         WHERE qb.tenant_id = s.tenant_id AND qb.subscription_id = s.id
	           AND qb.metric = 'traffic.bytes' AND qb.period IN ('cycle', 'total')
	           AND qb.limit_value IS NOT NULL) q ON true
	  LEFT JOIN LATERAL (
	        SELECT sum(g.granted_bytes - g.consumed_bytes) AS remaining
	          FROM traffic_pack_grants g
	         WHERE g.tenant_id = s.tenant_id AND g.subscription_id = s.id
	           AND g.consumed_bytes < g.granted_bytes) pk ON true
	 WHERE s.tenant_id = $1 AND s.user_id = $2::uuid
	 ORDER BY s.created_at, s.id
	 LIMIT 50` // 一个用户参与落点的订阅上限，按创建时间取

// candidateState 把订阅状态映射成落点规则的三档，口径只经 subscriptionAcceptsPaidChange。
func candidateState(status string, renewalClosed bool) purchase.State {
	switch {
	case !subscriptionAcceptsPaidChange(status, renewalClosed):
		return purchase.StateDead
	case status == "expired":
		return purchase.StateRevivable
	default:
		return purchase.StateLive
	}
}

// loadCandidatesTx 取用户的候选订阅（不加锁；落地时再锁选中的那一行复核）。
func loadCandidatesTx(ctx context.Context, tx pgx.Tx, tenantID, userID string) ([]purchase.Candidate, error) {
	rows, err := tx.Query(ctx, placementCandidatesSQL, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []purchase.Candidate
	for rows.Next() {
		var c purchase.Candidate
		var status string
		var closed bool
		if err := rows.Scan(&c.SubscriptionID, &c.PlanID, &c.PlanName, &c.Label, &status, &closed,
			&c.PeriodEnd, &c.TrafficUsed, &c.TrafficCap, &c.PackRemaining); err != nil {
			return nil, err
		}
		c.State = candidateState(status, closed)
		out = append(out, c)
	}
	return out, rows.Err()
}

// offerPeriod 是套餐卡 / 后台开单那个价格档的周期：给了价格档就用它，没给取套餐第一个在售
// 价格（与 grantPlanDirect 同），都没有按一个月。
func offerPeriod(ctx context.Context, tx pgx.Tx, tenantID, planID, priceID string) (string, int, error) {
	var price *string
	if priceID != "" {
		price = &priceID
	}
	var interval string
	var count int
	err := tx.QueryRow(ctx, `
		SELECT coalesce(pr.billing_interval, 'month'), coalesce(pr.interval_count, 1)
		  FROM plans p
		  LEFT JOIN prices pr
		    ON pr.tenant_id = p.tenant_id AND pr.product_id = p.product_id
		   AND pr.id = coalesce($3::uuid,
		         (SELECT x.id FROM prices x
		           WHERE x.tenant_id = p.tenant_id AND x.product_id = p.product_id
		             AND x.status = 'active'
		           ORDER BY x.created_at LIMIT 1))
		 WHERE p.tenant_id = $1 AND p.id = $2::uuid`, tenantID, planID, price).Scan(&interval, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return "month", 1, nil
	}
	return interval, count, err
}

// creditOf 是一份订阅此刻的剩余价值（换掉它时退回或抵扣的钱）与币种；没有周期边界为 0。
func creditOf(ctx context.Context, tx pgx.Tx, tenantID, subID string, now time.Time) (int64, string, error) {
	var start, end *time.Time
	if err := tx.QueryRow(ctx, `SELECT current_period_start, current_period_end
		FROM subscriptions WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, subID).Scan(&start, &end); err != nil {
		return 0, "", err
	}
	if start == nil || end == nil {
		return 0, "", nil
	}
	basis, err := loadProrationBasis(ctx, tx, tenantID, subID, *start, *end)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return 0, "", nil // 币种不一致之类：展示时不报价值，落地时会给出原因
		}
		return 0, "", err
	}
	credit, _ := prorationCreditDetail(basis, now)
	return credit, basis.Currency, nil
}

// placementsTx 给出一次落地的全部选项（含展示用的数）与默认 Key。
func placementsTx(ctx context.Context, tx pgx.Tx, tenantID, userID string, offer purchase.Offer,
	entrySub string) ([]purchase.Placement, string, error) {
	cands, err := loadCandidatesTx(ctx, tx, tenantID, userID)
	if err != nil {
		return nil, "", err
	}
	opts, def := purchase.Options(offer, cands, entrySub)
	byID := make(map[string]purchase.Candidate, len(cands))
	for _, c := range cands {
		byID[c.SubscriptionID] = c
	}
	now := time.Now().UTC()
	interval, count := "", 0
	if offer.Kind == purchase.OfferPlan {
		if interval, count, err = offerPeriod(ctx, tx, tenantID, offer.PlanID, offer.PriceID); err != nil {
			return nil, "", err
		}
	}
	views := make([]purchase.Placement, 0, len(opts))
	for _, o := range opts {
		v := purchase.Placement{Option: o}
		if c, ok := byID[o.SubscriptionID]; ok {
			v.Label, v.PlanID, v.PlanName, v.State, v.PeriodEnd = c.Label, c.PlanID, c.PlanName, c.State, c.PeriodEnd
			v.TrafficUsed, v.TrafficCap, v.PackRemaining = c.TrafficUsed, c.TrafficCap, c.PackRemaining
		}
		var end time.Time
		switch o.Kind {
		case purchase.KindNew:
			end = period.AddInterval(now, interval, count)
		case purchase.KindRenew:
			// 生效中的接在原到期日后；过期 30 天内的从现在起算（恢复使用）
			base := now
			if v.State == purchase.StateLive && v.PeriodEnd != nil && v.PeriodEnd.After(now) {
				base = *v.PeriodEnd
			}
			end = period.AddInterval(base, interval, count)
		case purchase.KindChange:
			end = period.AddInterval(now, interval, count)
			if v.Credit, v.Currency, err = creditOf(ctx, tx, tenantID, o.SubscriptionID, now); err != nil {
				return nil, "", err
			}
		case purchase.KindExtendDays:
			base := now
			if v.PeriodEnd != nil && v.PeriodEnd.After(now) {
				base = *v.PeriodEnd
			}
			if offer.Days > 0 {
				end = base.AddDate(0, 0, offer.Days)
			}
		}
		if !end.IsZero() {
			e := end
			v.NewPeriodEnd = &e
		}
		views = append(views, v)
	}
	return views, def, nil
}

// resolvePlacementTx 在兑换 / 开单的事务里重新取候选并校验选择：给了选择就 Match，没给且只有
// 一个选项就用它，否则 422。返回选中的选项；一个选项都没有时 ok=false。
func resolvePlacementTx(ctx context.Context, tx pgx.Tx, tenantID, userID string, offer purchase.Offer,
	choice *purchase.Choice) (purchase.Option, bool, error) {
	cands, err := loadCandidatesTx(ctx, tx, tenantID, userID)
	if err != nil {
		return purchase.Option{}, false, err
	}
	opts, _ := purchase.Options(offer, cands, "")
	opt, ok, err := purchase.Resolve(choice, opts)
	if err != nil {
		return purchase.Option{}, false, placementError(err)
	}
	return opt, ok, nil
}

// placementError 把 purchase 的哨兵错误翻成 422（文案在 purchase 里，可以直接给人看）。
func placementError(err error) error {
	switch {
	case errors.Is(err, purchase.ErrChoiceStale), errors.Is(err, purchase.ErrChoiceRequired):
		return httpx.Invalid(map[string]string{"choice": err.Error()})
	}
	return err
}

// lockPlacementSubscription 锁住选中的那一份并复核它属于用户、状态仍然允许这个用法：
// renew / change / extend_days 要可付费变更（生效中或可救回），reset / add_traffic 要生效中。
func lockPlacementSubscription(ctx context.Context, tx pgx.Tx, tenantID, userID string,
	opt purchase.Option) error {
	var status string
	var closed bool
	err := tx.QueryRow(ctx, `
		SELECT status, renewal_closed_at IS NOT NULL FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
		 FOR UPDATE`, tenantID, opt.SubscriptionID, userID).Scan(&status, &closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return placementError(purchase.ErrChoiceStale)
	}
	if err != nil {
		return err
	}
	state := candidateState(status, closed)
	switch opt.Kind {
	case purchase.KindResetTraffic, purchase.KindAddTraffic:
		if state != purchase.StateLive {
			return placementError(purchase.ErrChoiceStale)
		}
	default:
		if state == purchase.StateDead {
			return placementError(purchase.ErrChoiceStale)
		}
	}
	return nil
}
