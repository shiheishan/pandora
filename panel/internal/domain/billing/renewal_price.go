package billing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// renewalTarget 是续费一条订阅读出的订阅、套餐与价格（报价与建单共用）。
type renewalTarget struct {
	PlanID        string
	PlanVersionID string
	Status        string
	PeriodEnd     *time.Time
	CurPriceID    *string
	UserGroupID   *string
	Price         catalogPrice
}

// loadRenewalTargetTx 读要续的订阅并校验能不能续，再按续费口径读价格档：priceID 为空沿用
// 订阅自己的价格。lock 为真时（建单）锁订阅行、套餐行（FOR SHARE）与价格行，锁序同结算：
// 订阅 → 套餐 → 价格；报价不锁。
func loadRenewalTargetTx(ctx context.Context, tx pgx.Tx, tenantID, userID, subID, priceID string,
	lock bool, now time.Time) (*renewalTarget, error) {
	t, err := loadRenewalSourceTx(ctx, tx, tenantID, userID, subID, lock)
	if err != nil {
		return nil, err
	}
	if priceID == "" {
		if t.CurPriceID == nil {
			return nil, httpx.New(httpx.CodeConflict, "这条订阅没有关联价格，无法自动续费")
		}
		priceID = *t.CurPriceID
	}
	price, err := loadRenewalPriceTx(ctx, tx, tenantID, t.PlanID, priceID, t.UserGroupID, lock, now)
	if err != nil {
		return nil, err
	}
	t.Price = *price
	return t, nil
}

// loadRenewalSourceTx 读要续的订阅、套餐是否允许续费与用户组，并校验订阅状态（不读价格）。
func loadRenewalSourceTx(ctx context.Context, tx pgx.Tx, tenantID, userID, subID string,
	lock bool) (*renewalTarget, error) {
	if _, err := uuid.Parse(subID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var (
		t             renewalTarget
		renewalClosed bool
	)
	if err := tx.QueryRow(ctx, `
		SELECT plan_id::text, plan_version_id::text, price_id::text,
		       status, current_period_end, renewal_closed_at IS NOT NULL
		  FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`+lockClause(lock, `
		 FOR UPDATE`),
		tenantID, subID, userID).Scan(
		&t.PlanID, &t.PlanVersionID, &t.CurPriceID, &t.Status, &t.PeriodEnd, &renewalClosed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}

	var allowRenewal bool
	if err := tx.QueryRow(ctx, `SELECT allow_renewal FROM plans
		WHERE tenant_id=$1 AND id=$2::uuid`+lockClause(lock, ` FOR SHARE`), tenantID, t.PlanID).Scan(&allowRenewal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if !allowRenewal {
		return nil, httpx.New(httpx.CodeConflict, "该套餐当前不允许续费")
	}
	if err := tx.QueryRow(ctx, `SELECT user_group_id FROM users
		WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, userID).Scan(&t.UserGroupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}

	// 已取消、或过期超过原地续费窗口（30 天）的订阅不给续 —— 那种情况应该
	// 走重新购买，因为权益版本、价格、节点分组可能都已经变了。窗口内的已过期
	// 订阅照常续，沿用原套餐版本，链接不变（规则 4）
	if !subscriptionAcceptsPaidChange(t.Status, renewalClosed) {
		if t.Status == "expired" {
			return nil, ErrRenewalWindowClosed
		}
		return nil, ErrSubNotRenewable
	}
	if err := ensureNoOpenSubscriptionOrder(ctx, tx, tenantID, subID); err != nil {
		return nil, err
	}
	return &t, nil
}

// loadRenewalPriceTx 按续费口径读一个价格档：属于订阅套餐的产品、在售、用户组、有效期。
// 不符都回 ErrRenewPriceGone（续费页让用户重新选价格档）。
func loadRenewalPriceTx(ctx context.Context, tx pgx.Tx, tenantID, planID, priceID string,
	userGroupID *string, lock bool, now time.Time) (*catalogPrice, error) {
	if _, err := uuid.Parse(priceID); err != nil {
		return nil, ErrRenewPriceGone
	}
	pr := catalogPrice{ID: priceID}
	var (
		priceStatus  string
		priceGroupID *string
		validFrom    *time.Time
		validUntil   *time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT pr.currency::text, pr.unit_amount, pr.billing_interval,
		       pr.interval_count, pr.status, pr.user_group_id,
		       pr.valid_from, pr.valid_until
		  FROM prices pr
		  JOIN plans pl ON pl.product_id = pr.product_id AND pl.tenant_id = pr.tenant_id
		 WHERE pr.tenant_id = $1 AND pr.id = $2::uuid AND pl.id = $3::uuid
		   AND pr.currency IN ('CNY','USD')`+lockClause(lock, `
		 FOR UPDATE OF pr`),
		tenantID, priceID, planID).Scan(&pr.Currency, &pr.UnitAmount,
		&pr.Interval, &pr.IntervalCount, &priceStatus, &priceGroupID,
		&validFrom, &validUntil); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRenewPriceGone
		}
		return nil, err
	}
	if priceStatus != "active" {
		return nil, ErrRenewPriceGone
	}
	if priceGroupID != nil && (userGroupID == nil || *priceGroupID != *userGroupID) {
		return nil, ErrRenewPriceGone
	}
	if !catalogPriceCurrentlyValid(validFrom, validUntil, now) {
		return nil, ErrRenewPriceGone
	}
	return &pr, nil
}
