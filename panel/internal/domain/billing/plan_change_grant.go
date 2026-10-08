package billing

// 套餐卡遇到不同套餐：在原订阅上换成卡上的套餐（用户 2026-10-07 定），链接不变。
//
// 套餐卡没有订单（兑换必须和「标记码已用」同一个事务，见 GiftGranter.GrantPlan），
// 所以这里不经 CreatePlanChange，而是用同一份折算（loadProrationBasis / prorationCredit）
// 算出原订阅的剩余价值，再交给同一份履约 applyPlanChangeTx。卡本身算 0 元：
// 剩余价值全额退进余额，退款分录与 plan_changed 事件挂在卡密上，由迁移 00134 的证据
// 守卫核对（兑换流水、金额、科目）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// PlanGrant 是一次套餐卡兑换的结果。
type PlanGrant struct {
	SubscriptionID string
	// Mode：new 新开订阅；renewed 在同套餐订阅上续一期；changed 在原订阅上换成卡上的套餐
	Mode string
	// BalanceRefund 是换套餐时原套餐退进余额的剩余价值（最小货币单位），币种 RefundCurrency
	BalanceRefund  int64
	RefundCurrency string
}

// 套餐卡兑换的三种落地方式。
const (
	PlanGrantNew     = "new"
	PlanGrantRenewed = "renewed"
	PlanGrantChanged = "changed"
)

// grantPlanChange 用套餐卡把原订阅换成卡上的套餐（无订单）。调用方已锁住订阅行。
//
// 新套餐取当前发布版本；价格档按卡上绑的，没绑就取套餐第一个在售价格（与
// grantPlanDirect 同），只用来定新周期长度与订阅快照，不收钱。
func (s *Service) grantPlanChange(ctx context.Context, tx pgx.Tx,
	tenantID, userID, subID, planID, priceID, codeID string) (PlanGrant, error) {
	if codeID == "" {
		return PlanGrant{}, errors.New("gift plan change needs the gift card code")
	}
	// 订阅上挂着未完结的续费或变更单时不换：那张单按下单时的订阅算好的剩余价值会失效
	if err := ensureNoOpenSubscriptionOrder(ctx, tx, tenantID, subID); err != nil {
		if errors.Is(err, ErrSubscriptionOrderOpen) {
			return PlanGrant{}, ErrPlanChangeGiftOrderOpen
		}
		return PlanGrant{}, err
	}
	var (
		curPlanID              string
		snapshotCurrency       string
		periodStart, periodEnd *time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT plan_id::text, snapshot_currency::text, current_period_start, current_period_end
		  FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`,
		tenantID, subID, userID).Scan(&curPlanID, &snapshotCurrency, &periodStart, &periodEnd); err != nil {
		return PlanGrant{}, fmt.Errorf("读取要换套餐的订阅: %w", err)
	}
	if curPlanID == planID {
		return PlanGrant{}, errors.New("gift plan change targets the subscription's own plan")
	}

	var price *string
	if priceID != "" {
		price = &priceID
	}
	a := planChangeApply{
		TenantID: tenantID, SubID: subID, UserID: userID, PlanID: planID,
		GiftCodeID: codeID, ActorKind: "user", ActorID: &userID,
	}
	var (
		versionID, versionStatus string
		frozen                   bool
		priceCurrency            *string
	)
	err := tx.QueryRow(ctx, `
		SELECT coalesce(p.current_version_id::text, ''), coalesce(pv.status, ''),
		       pv.frozen_at IS NOT NULL,
		       pr.id::text, pr.currency::text, coalesce(pr.unit_amount, 0),
		       coalesce(pr.billing_interval, 'month'), coalesce(pr.interval_count, 1)
		  FROM plans p
		  LEFT JOIN plan_versions pv
		    ON pv.tenant_id = p.tenant_id AND pv.id = p.current_version_id AND pv.plan_id = p.id
		  LEFT JOIN prices pr
		    ON pr.tenant_id = p.tenant_id AND pr.product_id = p.product_id
		   AND pr.id = coalesce($3::uuid,
		         (SELECT x.id FROM prices x
		           WHERE x.tenant_id = p.tenant_id AND x.product_id = p.product_id
		             AND x.status = 'active'
		           ORDER BY x.created_at LIMIT 1))
		 WHERE p.tenant_id = $1 AND p.id = $2::uuid AND p.status = 'active'
		 FOR SHARE OF p`,
		tenantID, planID, price).Scan(&versionID, &versionStatus, &frozen,
		&a.PriceID, &priceCurrency, &a.SnapshotAmount, &a.Interval, &a.IntervalCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanGrant{}, httpx.New(httpx.CodeValidationFailed,
			"这张卡绑定的套餐已经下架了，请联系客服")
	}
	if err != nil {
		return PlanGrant{}, fmt.Errorf("读取套餐卡的套餐: %w", err)
	}
	if versionID == "" || versionStatus != "published" || !frozen {
		return PlanGrant{}, httpx.New(httpx.CodeValidationFailed, "这张卡绑定的套餐还没有发布版本")
	}
	a.PlanVersionID = versionID
	a.SnapshotCurrency = snapshotCurrency
	if priceCurrency != nil {
		a.SnapshotCurrency = *priceCurrency
	}

	// 剩余价值与门户改套餐同一口径；卡本身 0 元，所以全额退进余额（币种跟原订阅的付费单走）
	if periodStart != nil && periodEnd != nil {
		basis, err := loadProrationBasis(ctx, tx, tenantID, subID, *periodStart, *periodEnd)
		if err != nil {
			return PlanGrant{}, err
		}
		a.ProrationCredit = prorationCredit(basis, time.Now().UTC())
		a.Refund = a.ProrationCredit
		a.RefundCurrency = basis.Currency
	}
	if a.Refund > 0 && a.RefundCurrency == "" {
		return PlanGrant{}, errors.New("gift plan change refund has no currency")
	}
	if _, err := applyPlanChangeTx(ctx, tx, a); err != nil {
		return PlanGrant{}, err
	}
	return PlanGrant{SubscriptionID: subID, Mode: PlanGrantChanged,
		BalanceRefund: a.Refund, RefundCurrency: a.RefundCurrency}, nil
}
