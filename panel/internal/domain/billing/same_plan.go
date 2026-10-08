package billing

// 同套餐只续不新开（用户 2026-10-07 规则 3；购买模型统一后只剩门户新购没带 new_copy 时拦截）。
//
// Pandora 的订阅链接挂在订阅上，一个用户可以有多条订阅：任何走成「新购」的路径都会
// 新开订阅、换链接。所以用户已有同一套餐的订阅（生效中，或过期 30 天内、原地续费
// 窗口没关）时：
//
//	门户新购     拒绝（409），门户改走续费（前端按同一口径路由，这里兜底）
//	礼品卡套餐卡 同款那份是默认选项（purchase.Options），选了就续一期（grantPlanRenewal）
//	后台人工开单 同上，Target 选 renew 时开续费单（CreateManualOrder → CreateRenewal）
//
// 换别的套餐走原订阅的「改套餐」（plan_change.go），由门户路由；过期超过 30 天的同套餐
// 订阅只能新购，换新链接（旧链接已被过期扫描吊销，见 expire.go）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ErrSamePlanUseRenewal 是用户已有同套餐订阅时又去新购的 409。
var ErrSamePlanUseRenewal = httpx.New(httpx.CodeConflict,
	"你已有这个套餐的订阅，请在原订阅上续费，订阅链接不变")

// renewableSamePlanSubscription 取用户在这个套餐上可以原地续费的订阅，没有返回空串。
// 生效中的优先，其次到期最晚。是否可续只经 subscriptionAcceptsPaidChange 判断。
// lock 为真时锁住选中的那一行（调用方随后要在它上面续费）。
func renewableSamePlanSubscription(ctx context.Context, tx pgx.Tx,
	tenantID, userID, planID string, lock bool) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, status, renewal_closed_at IS NOT NULL
		  FROM subscriptions
		 WHERE tenant_id = $1 AND user_id = $2::uuid AND plan_id = $3::uuid
		 ORDER BY (status <> 'expired') DESC, current_period_end DESC NULLS LAST,
		          created_at DESC`, tenantID, userID, planID)
	if err != nil {
		return "", err
	}
	subID := ""
	for rows.Next() {
		var id, status string
		var closed bool
		if err := rows.Scan(&id, &status, &closed); err != nil {
			rows.Close()
			return "", err
		}
		if subID == "" && subscriptionAcceptsPaidChange(status, closed) {
			subID = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || subID == "" || !lock {
		return subID, err
	}
	// 锁住后复核：读和锁之间状态可能刚变
	var status string
	var closed bool
	if err := tx.QueryRow(ctx, `
		SELECT status, renewal_closed_at IS NOT NULL FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, subID).Scan(&status, &closed); err != nil {
		return "", err
	}
	if !subscriptionAcceptsPaidChange(status, closed) {
		return "", nil
	}
	return subID, nil
}

// grantPlanRenewal 用套餐卡在同套餐订阅上续一期（无订单）。调用方已锁住订阅行。
//
// 价格档按卡上绑的，没绑就沿用订阅自己的，再没有就取套餐第一个在售价格（与
// grantPlanDirect 同）；套餐版本沿用订阅原来的（规则 4：沿用原套餐版本的权益）。
func (s *Service) grantPlanRenewal(ctx context.Context, tx pgx.Tx,
	tenantID, userID, subID, planID, priceID string) error {
	var price *string
	if priceID != "" {
		price = &priceID
	}
	g := renewalGrant{TenantID: tenantID, UserID: userID, SubscriptionID: subID,
		ActorKind: "system", Source: "gift_card"}
	err := tx.QueryRow(ctx, `
		SELECT s.plan_version_id::text, pr.id::text,
		       coalesce(pr.billing_interval, 'month'), coalesce(pr.interval_count, 1)
		  FROM subscriptions s
		  JOIN plans p ON p.tenant_id = s.tenant_id AND p.id = s.plan_id
		  LEFT JOIN prices pr
		    ON pr.tenant_id = p.tenant_id AND pr.product_id = p.product_id
		   AND pr.id = coalesce($4::uuid, s.price_id,
		         (SELECT x.id FROM prices x
		           WHERE x.tenant_id = p.tenant_id AND x.product_id = p.product_id
		             AND x.status = 'active'
		           ORDER BY x.created_at LIMIT 1))
		 WHERE s.tenant_id = $1 AND s.id = $2::uuid AND s.plan_id = $3::uuid`,
		tenantID, subID, planID, price).Scan(&g.PlanVersionID, &g.PriceID,
		&g.Interval, &g.IntervalCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.New(httpx.CodeValidationFailed, "这张卡绑定的套餐与订阅对不上，请联系客服")
	}
	if err != nil {
		return fmt.Errorf("读取套餐卡续费快照: %w", err)
	}
	g.PaidAt = time.Now().UTC()
	_, err = renewSubscriptionTx(ctx, tx, g)
	return err
}
