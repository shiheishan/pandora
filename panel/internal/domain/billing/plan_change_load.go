package billing

// 换套餐的三步读取（设计稿 2.4）：报价（不锁、可按套餐或按订阅展开）与建单（锁）共用。
//
//	loadChangeSourceTx   原订阅：状态、周期、剩余价值的折算输入
//	loadChangeTargetsTx  目标套餐与价格档：报价一条 SQL 取多个套餐的全部价格
//	loadChangeTargetTx   建单的那一个套餐与价格，按锁序加锁（订阅 → 套餐 → 价格 → 券）
//	loadPlanSnapshotTx   权益与配额快照，只在建单时取（checkout_catalog.go）
//
// 后台开单（manual）跳过可见性与用户组（w6plan 遗留：「不查可见、要查允许变更」），
// 保留 allow_upgrade、已发布版本、价格在售与有效期、订阅状态。

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// changeSource 是要换掉的那一份。
type changeSource struct {
	SubscriptionID string
	PlanID         string
	Status         string
	PeriodEnd      *time.Time
	UserGroupID    *string
	// Basis 为空表示没有周期边界（从未开通），没有可折的东西
	Basis *prorationBasis
}

// loadChangeSourceTx 读原订阅并校验能不能换（subscriptionAcceptsPaidChange、没有未完结的
// 续费或变更单），再读剩余价值的折算输入。lock 为真时锁订阅行。
func loadChangeSourceTx(ctx context.Context, tx pgx.Tx, tenantID, userID, subID string,
	lock bool) (*changeSource, error) {
	if _, err := uuid.Parse(subID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	src := changeSource{SubscriptionID: subID}
	var periodStart *time.Time
	var renewalClosed bool
	err := tx.QueryRow(ctx, `
		SELECT plan_id::text, status, current_period_start, current_period_end,
		       renewal_closed_at IS NOT NULL
		  FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`+lockClause(lock, `
		 FOR UPDATE`), tenantID, subID, userID).
		Scan(&src.PlanID, &src.Status, &periodStart, &src.PeriodEnd, &renewalClosed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	// 过期 30 天内的订阅同样可以在原订阅上改套餐（规则 3、4），链接不变
	if !subscriptionAcceptsPaidChange(src.Status, renewalClosed) {
		if src.Status == "expired" {
			return nil, ErrRenewalWindowClosed
		}
		return nil, ErrPlanChangeSubStatus
	}
	if err := ensureNoOpenSubscriptionOrder(ctx, tx, tenantID, subID); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT user_group_id::text FROM users
		WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, userID).Scan(&src.UserGroupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if periodStart != nil && src.PeriodEnd != nil {
		basis, err := loadProrationBasis(ctx, tx, tenantID, subID, *periodStart, *src.PeriodEnd)
		if err != nil {
			return nil, err
		}
		src.Basis = &basis
	}
	return &src, nil
}

// credit 是原订阅在 asOf 时刻的剩余价值与明细；本期付费单的币种与新价不同就不能换。
func (src *changeSource) credit(asOf time.Time, currency string) (int64, *CreditDetail, error) {
	if src.Basis == nil {
		return 0, nil, nil
	}
	if src.Basis.Currency != "" && src.Basis.Currency != currency {
		return 0, nil, ErrPlanChangeCurrency
	}
	credit, d := prorationCreditDetail(*src.Basis, asOf)
	return credit, &d, nil
}

// changeTarget 是一个能换过去的套餐与它的价格档。
type changeTarget struct {
	PlanID        string
	PlanName      string
	ProductID     string
	PlanVersionID string
	Prices        []catalogPrice
}

// changePlanRow 是校验一个目标套餐要用的列。
type changePlanRow struct {
	Status, Visibility        string
	VisibleGroupIDs           []string
	VisibleFrom, VisibleUntil *time.Time
	AllowUpgrade              bool
	CurrentVersion            *string
}

// changePlanRefusal 是目标套餐不能换过去的原因（nil 表示可以）。与新购同一套可见性规则
// （XBD-011：看不到的套餐也换不过去）；后台开单跳过可见性与用户组。
func changePlanRefusal(r changePlanRow, userGroupID *string, manual bool, now time.Time) error {
	if r.Status != "active" {
		return httpx.NotFoundOrForbidden()
	}
	if !manual && (r.Visibility == "hidden" || r.Visibility == "invite_only" ||
		(r.VisibleFrom != nil && r.VisibleFrom.After(now)) ||
		(r.VisibleUntil != nil && !r.VisibleUntil.After(now)) ||
		(r.Visibility == "group" && !catalogGroupAllowed(userGroupID, r.VisibleGroupIDs))) {
		return httpx.NotFoundOrForbidden()
	}
	if !r.AllowUpgrade {
		return ErrPlanChangeNotAllowed
	}
	if r.CurrentVersion == nil {
		return httpx.New(httpx.CodeConflict, "该套餐尚未发布可用版本")
	}
	return nil
}

// changePriceRow 是校验一个价格档要用的列。
type changePriceRow struct {
	Status                string
	GroupID               *string
	ValidFrom, ValidUntil *time.Time
}

// changePriceRefusal 是价格档不能用的原因（nil 表示可以）；后台开单不看价格的用户组。
func changePriceRefusal(r changePriceRow, userGroupID *string, manual bool, now time.Time) error {
	if !manual && r.GroupID != nil && (userGroupID == nil || *userGroupID != *r.GroupID) {
		return httpx.NotFoundOrForbidden()
	}
	if r.Status != "active" {
		return httpx.New(httpx.CodeConflict, "该价格已下架")
	}
	if !catalogPriceCurrentlyValid(r.ValidFrom, r.ValidUntil, now) {
		return httpx.New(httpx.CodeConflict, "该价格当前不在有效期内")
	}
	return nil
}

// loadChangeTargetTx 读建单的目标套餐（FOR SHARE）与价格档（FOR UPDATE），锁序在订阅之后。
func loadChangeTargetTx(ctx context.Context, tx pgx.Tx, tenantID, planID, priceID string,
	userGroupID *string, manual bool, now time.Time) (*changeTarget, error) {
	for _, id := range []string{planID, priceID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, httpx.NotFoundOrForbidden()
		}
	}
	t := changeTarget{PlanID: planID}
	var r changePlanRow
	err := tx.QueryRow(ctx, `
		SELECT name, status, visibility, visible_group_ids::text[],
		       visible_from, visible_until, allow_upgrade, current_version_id::text,
		       product_id::text
		  FROM plans
		 WHERE tenant_id = $1 AND id = $2::uuid
		 FOR SHARE`, tenantID, planID).Scan(&t.PlanName, &r.Status, &r.Visibility,
		&r.VisibleGroupIDs, &r.VisibleFrom, &r.VisibleUntil, &r.AllowUpgrade, &r.CurrentVersion,
		&t.ProductID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	if err := changePlanRefusal(r, userGroupID, manual, now); err != nil {
		return nil, err
	}
	t.PlanVersionID = *r.CurrentVersion

	p := catalogPrice{ID: priceID}
	var pr changePriceRow
	err = tx.QueryRow(ctx, `
		SELECT currency::text, unit_amount, billing_interval, interval_count, status,
		       user_group_id::text, valid_from, valid_until
		  FROM prices
		 WHERE tenant_id = $1 AND id = $2::uuid AND product_id = $3::uuid
		   AND currency IN ('CNY','USD')
		 FOR UPDATE`, tenantID, priceID, t.ProductID).Scan(&p.Currency, &p.UnitAmount,
		&p.Interval, &p.IntervalCount, &pr.Status, &pr.GroupID, &pr.ValidFrom, &pr.ValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	if err := changePriceRefusal(pr, userGroupID, manual, now); err != nil {
		return nil, err
	}
	t.Prices = []catalogPrice{p}
	return &t, nil
}

// loadChangeTargetsTx 一条 SQL 取多个目标套餐与它们在售的价格档（报价用，不锁），按同样的
// 规则过滤掉不能换过去的套餐与价格档；planIDs 为空取全部套餐。excludePlan 是原订阅的套餐。
// 套餐按排序、价格档按金额排，最多 maxQuoteRows 个套餐。
func loadChangeTargetsTx(ctx context.Context, tx pgx.Tx, tenantID string, planIDs []string,
	excludePlan string, userGroupID *string, manual bool, now time.Time) ([]changeTarget, error) {
	var ids any
	if len(planIDs) > 0 {
		ids = planIDs
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id::text, p.name, p.product_id::text, p.status, p.visibility,
		       p.visible_group_ids::text[], p.visible_from, p.visible_until, p.allow_upgrade,
		       p.current_version_id::text,
		       coalesce(pv.status = 'published' AND pv.frozen_at IS NOT NULL, false),
		       pr.id::text, pr.currency::text, pr.unit_amount, pr.billing_interval, pr.interval_count,
		       pr.status, pr.user_group_id::text, pr.valid_from, pr.valid_until
		  FROM plans p
		  LEFT JOIN plan_versions pv
		    ON pv.tenant_id = p.tenant_id AND pv.id = p.current_version_id AND pv.plan_id = p.id
		  LEFT JOIN prices pr
		    ON pr.tenant_id = p.tenant_id AND pr.product_id = p.product_id
		   AND pr.status = 'active' AND pr.currency IN ('CNY','USD')
		 WHERE p.tenant_id = $1 AND ($2::uuid[] IS NULL OR p.id = ANY($2::uuid[]))
		   AND p.id IS DISTINCT FROM $3::uuid
		 ORDER BY p.sort_order, p.created_at, p.id, pr.unit_amount, pr.interval_count, pr.id`,
		tenantID, ids, nullIfEmpty(excludePlan))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []changeTarget
	index := map[string]int{}
	for rows.Next() {
		var (
			t         changeTarget
			r         changePlanRow
			published bool
			priceID   *string
			p         catalogPrice
			currency  *string
			amount    *int64
			interval  *string
			count     *int16
			pr        changePriceRow
			prStatus  *string
		)
		if err := rows.Scan(&t.PlanID, &t.PlanName, &t.ProductID, &r.Status, &r.Visibility,
			&r.VisibleGroupIDs, &r.VisibleFrom, &r.VisibleUntil, &r.AllowUpgrade, &r.CurrentVersion,
			&published, &priceID, &currency, &amount, &interval, &count,
			&prStatus, &pr.GroupID, &pr.ValidFrom, &pr.ValidUntil); err != nil {
			return nil, err
		}
		if changePlanRefusal(r, userGroupID, manual, now) != nil || !published {
			continue
		}
		at, seen := index[t.PlanID]
		if !seen {
			if len(out) >= maxQuoteRows {
				continue
			}
			t.PlanVersionID = *r.CurrentVersion
			out = append(out, t)
			at = len(out) - 1
			index[t.PlanID] = at
		}
		if priceID == nil {
			continue
		}
		pr.Status = *prStatus
		if changePriceRefusal(pr, userGroupID, manual, now) != nil {
			continue
		}
		p.ID, p.Currency, p.UnitAmount, p.Interval, p.IntervalCount = *priceID, *currency, *amount, *interval, *count
		out[at].Prices = append(out[at].Prices, p)
	}
	return out, rows.Err()
}
