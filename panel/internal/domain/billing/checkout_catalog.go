package billing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func catalogGroupAllowed(userGroupID *string, allowed []string) bool {
	if userGroupID == nil {
		return false
	}
	for _, id := range allowed {
		if id == *userGroupID {
			return true
		}
	}
	return false
}

func catalogPriceCurrentlyValid(from, until *time.Time, now time.Time) bool {
	return (from == nil || !from.After(now)) && (until == nil || until.After(now))
}

// lockClause 在 lock 为真时给查询加上行锁子句。报价只读、不锁；建单用同一条查询加锁。
func lockClause(lock bool, clause string) string {
	if lock {
		return clause
	}
	return ""
}

// newPurchasePlan 是新购一个套餐时读出的套餐与当前版本（报价与建单共用）。
type newPurchasePlan struct {
	PlanID         string
	PlanName       string
	ProductID      string
	PurchaseLimit  *int
	StockTotal     *int
	StockReserved  int
	StockSold      int
	CurrentVersion string
	UserGroupID    *string
}

// loadNewPurchasePlanTx 读套餐并按新购口径校验：可见性（XBD-011）、用户组、接受新购、
// 已发布版本。lock 为真时锁住套餐行（建单要改库存计数）；报价不锁。
func loadNewPurchasePlanTx(ctx context.Context, tx pgx.Tx, tenantID, userID, planID string,
	lock bool, now time.Time) (*newPurchasePlan, error) {
	if _, err := uuid.Parse(planID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	p := newPurchasePlan{PlanID: planID}
	var (
		planStatus      string
		visibility      string
		visibleGroupIDs []string
		visibleFrom     *time.Time
		visibleUntil    *time.Time
		allowNew        bool
		currentVersion  *string
	)
	err := tx.QueryRow(ctx, `
		SELECT name, status, visibility, visible_group_ids::text[],
		       visible_from, visible_until, allow_new_purchase,
		       purchase_limit_per_user, stock_total, stock_reserved, stock_sold,
		       current_version_id, product_id
		  FROM plans
		 WHERE tenant_id = $1 AND id = $2`+lockClause(lock, `
		 FOR UPDATE`),
		tenantID, planID).Scan(&p.PlanName, &planStatus, &visibility,
		&visibleGroupIDs, &visibleFrom, &visibleUntil, &allowNew,
		&p.PurchaseLimit, &p.StockTotal, &p.StockReserved, &p.StockSold, &currentVersion, &p.ProductID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}

	// XBD-011：不可见套餐不能通过直接 API 下单
	if planStatus != "active" || visibility == "hidden" || visibility == "invite_only" {
		return nil, httpx.NotFoundOrForbidden()
	}
	if (visibleFrom != nil && visibleFrom.After(now)) ||
		(visibleUntil != nil && !visibleUntil.After(now)) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err := tx.QueryRow(ctx, `SELECT user_group_id FROM users
		WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, userID).Scan(&p.UserGroupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if visibility == "group" && !catalogGroupAllowed(p.UserGroupID, visibleGroupIDs) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if !allowNew {
		return nil, httpx.New(httpx.CodeConflict, "该套餐当前不接受新购")
	}
	if currentVersion == nil {
		return nil, httpx.New(httpx.CodeConflict, "该套餐尚未发布可用版本")
	}
	p.CurrentVersion = *currentVersion
	return &p, nil
}

// catalogPrice 是一个价格档。
type catalogPrice struct {
	ID            string
	Currency      string
	UnitAmount    int64
	Interval      string
	IntervalCount int16
}

// loadNewPurchasePriceTx 读一个价格档并按新购口径校验（在售、用户组、有效期）。
func loadNewPurchasePriceTx(ctx context.Context, tx pgx.Tx, tenantID, productID, priceID string,
	userGroupID *string, lock bool, now time.Time) (*catalogPrice, error) {
	if _, err := uuid.Parse(priceID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	pr := catalogPrice{ID: priceID}
	var (
		priceStatus     string
		priceGroupID    *string
		priceValidFrom  *time.Time
		priceValidUntil *time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT currency, unit_amount, billing_interval, interval_count, status,
		       user_group_id, valid_from, valid_until
		  FROM prices
		 WHERE tenant_id = $1 AND id = $2 AND product_id = $3
		   AND currency IN ('CNY','USD')`+lockClause(lock, `
		 FOR UPDATE`),
		tenantID, priceID, productID).Scan(
		&pr.Currency, &pr.UnitAmount, &pr.Interval, &pr.IntervalCount, &priceStatus,
		&priceGroupID, &priceValidFrom, &priceValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	if priceStatus != "active" {
		return nil, httpx.New(httpx.CodeConflict, "该价格已下架")
	}
	if priceGroupID != nil && (userGroupID == nil || *userGroupID != *priceGroupID) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if !catalogPriceCurrentlyValid(priceValidFrom, priceValidUntil, now) {
		return nil, httpx.New(httpx.CodeConflict, "该价格当前不在有效期内")
	}
	return &pr, nil
}

// listPlanPricesTx 列出用户现在能买的这个产品的全部价格档（报价按档各给一条），
// 口径同 loadNewPurchasePriceTx，按金额与周期排序。
func listPlanPricesTx(ctx context.Context, tx pgx.Tx, tenantID, productID string,
	userGroupID *string, now time.Time) ([]catalogPrice, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, currency, unit_amount, billing_interval, interval_count
		  FROM prices
		 WHERE tenant_id = $1 AND product_id = $2::uuid AND status = 'active'
		   AND currency IN ('CNY','USD')
		   AND (user_group_id IS NULL OR user_group_id = $3::uuid)
		   AND (valid_from IS NULL OR valid_from <= $4)
		   AND (valid_until IS NULL OR valid_until > $4)
		 ORDER BY unit_amount, interval_count, id
		 LIMIT 20`, tenantID, productID, userGroupID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catalogPrice
	for rows.Next() {
		var p catalogPrice
		if err := rows.Scan(&p.ID, &p.Currency, &p.UnitAmount, &p.Interval, &p.IntervalCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// planSnapshot 是写进订单行的权益与配额快照（SUB-002）与版本号。
type planSnapshot struct {
	Entitlements  []byte
	Quotas        []byte
	PlanVersionNo int
	ProductName   string
}

// loadPlanSnapshotTx 只在建单时取：权益、配额、版本（FOR SHARE，校验已发布并冻结）与产品名。
func loadPlanSnapshotTx(ctx context.Context, tx pgx.Tx, tenantID, planID, versionID,
	productID string) (*planSnapshot, error) {
	var sn planSnapshot
	var err error
	if sn.Entitlements, err = jsonAgg(ctx, tx, `
		SELECT coalesce(jsonb_agg(jsonb_build_object('code', code, 'value', value)), '[]'::jsonb)
		  FROM entitlements WHERE plan_version_id = $1`, versionID); err != nil {
		return nil, err
	}
	if sn.Quotas, err = jsonAgg(ctx, tx, `
		SELECT coalesce(jsonb_agg(jsonb_build_object(
		         'metric', metric, 'limit', limit_value,
		         'unit', unit, 'period', period)), '[]'::jsonb)
		  FROM quota_definitions WHERE plan_version_id = $1`, versionID); err != nil {
		return nil, err
	}
	var versionStatus string
	var frozenAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT version,status,frozen_at FROM plan_versions
		  WHERE tenant_id=$1 AND id=$2::uuid AND plan_id=$3::uuid FOR SHARE`,
		tenantID, versionID, planID).
		Scan(&sn.PlanVersionNo, &versionStatus, &frozenAt); err != nil {
		return nil, err
	}
	if versionStatus != "published" || frozenAt == nil {
		return nil, httpx.New(httpx.CodeConflict, "套餐当前版本未完成发布")
	}
	if err := tx.QueryRow(ctx,
		`SELECT name FROM products WHERE tenant_id = $1 AND id = $2`,
		tenantID, productID).Scan(&sn.ProductName); err != nil {
		return nil, err
	}
	return &sn, nil
}
