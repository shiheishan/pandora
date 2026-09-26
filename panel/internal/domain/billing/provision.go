// [INPUT]: 依赖 platform/db 与 platform/httpx，读套餐版本快照与配额定义
// [OUTPUT]: 包内提供 provisionSpec、provisionSubscription、initQuotaBalances、grantPlanDirect
// [POS]: billing 开订阅与建配额的唯一实现：从 checkout.go 拆出。下单履约（快照来自订单）与礼品卡套餐兑换（快照来自套餐当前版本）必须产出同样的订阅与凭据，所以共用这里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// provisionSpec 描述一次订阅开通所需的全部快照信息。
//
// 抽出来是因为有两条路会开通订阅：订单履约（快照来自 order_items）
// 和礼品卡的套餐兑换（快照来自套餐当前版本）。这两条路必须产出
// 完全一致的订阅、配额和凭据 —— 复制一份实现的话，
// 日后改了一边忘了另一边，症状会是「兑换来的套餐少了个凭据」
// 这种要查很久的问题。
type provisionSpec struct {
	PlanID        string
	PlanVersionID string
	PriceID       *string
	Currency      string
	UnitAmount    int64
	Interval      string
	IntervalCount int16
	// ActorKind 写进订阅事件，用来区分这次开通是支付换来的还是赠送的
	ActorKind string
	// OrderID 可空：礼品卡兑换没有订单
	OrderID *string
}

// provisionSubscription 创建并激活订阅，初始化配额，签发订阅凭据。
func (s *Service) provisionSubscription(ctx context.Context, tx pgx.Tx,
	tenantID, userID string, spec provisionSpec) (string, error) {

	now := time.Now().UTC()
	periodEnd := addInterval(now, spec.Interval, int(spec.IntervalCount))

	// 订阅先建为 pending，再走状态机转到 active。
	// 不直接插入 active —— 让每一次激活都经过 SUB-004 的转换校验并留下事件。
	var subID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO subscriptions
			(tenant_id, user_id, plan_id, plan_version_id, price_id, status,
			 current_period_start, current_period_end,
			 snapshot_currency, snapshot_amount)
		VALUES ($1,$2,$3,$4,$5,'pending',$6,$7,$8,$9)
		RETURNING id`,
		tenantID, userID, spec.PlanID, spec.PlanVersionID, spec.PriceID,
		now, periodEnd, spec.Currency, spec.UnitAmount).Scan(&subID); err != nil {
		return "", fmt.Errorf("创建订阅: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE subscriptions SET status = 'active' WHERE id = $1 AND status = 'pending'`, subID)
	if err != nil {
		return "", fmt.Errorf("激活订阅: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("subscription activation transition lost")
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status,
			 actor_kind, order_id, payload)
		VALUES ($1,$2,'activated','pending','active',$3,$4,$5)`,
		tenantID, subID, spec.ActorKind, spec.OrderID,
		map[string]any{"period_end": periodEnd}); err != nil {
		return "", err
	}

	if err := initQuotaBalances(ctx, tx, tenantID, subID, spec.PlanVersionID,
		now, periodEnd); err != nil {
		return "", err
	}

	// --- 签发订阅凭据（XBD-002）---
	credToken, err := crypto.NewToken(32)
	if err != nil {
		return "", err
	}
	// 密文供面板展示。aad 绑定订阅 ID —— 把某条密文搬到别人的记录上
	// 会直接解密失败，光有数据库写权限伪造不出一条能用的凭据。
	var sealed []byte
	if s.envelope != nil {
		sealed, err = s.envelope.Seal([]byte(credToken), []byte(subID))
		if err != nil {
			return "", fmt.Errorf("加密订阅凭据: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_credentials
			(tenant_id, subscription_id, user_id, token_hash, token_prefix,
			 scope, expires_at, token_encrypted)
		VALUES ($1,$2,$3,$4,$5,'subscription',$6,$7)`,
		tenantID, subID, userID, crypto.HashToken(credToken),
		credToken[:8], periodEnd, sealed); err != nil {
		return "", fmt.Errorf("签发订阅凭据: %w", err)
	}

	return subID, nil
}

// initQuotaBalances 按套餐版本的配额定义给订阅补齐配额行（USE-005），已有同
// 指标同周期的行跳过。新开订阅从这里建整套；变更套餐（plan_change.go）先原地
// 重置已有的行，再从这里补上新套餐多出来的指标。
//
// 注意 $4 必须显式转型：在 CASE 的一个分支是裸 NULL 时，
// PostgreSQL 无从推断参数类型，会退化成 text 并与 timestamptz 列冲突。
func initQuotaBalances(ctx context.Context, tx pgx.Tx, tenantID, subID,
	planVersionID string, periodStart, periodEnd time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO quota_balances
			(tenant_id, subscription_id, metric, period, period_start, period_end,
			 granted, limit_value)
		SELECT $1, $2, qd.metric, qd.period, $3::timestamptz,
		       CASE WHEN qd.period = 'total'
		            THEN NULL::timestamptz
		            ELSE $4::timestamptz END,
		       coalesce(qd.limit_value, 0), qd.limit_value
		  FROM quota_definitions qd
		 WHERE qd.plan_version_id = $5
		   AND NOT EXISTS (SELECT 1 FROM quota_balances qb
		                    WHERE qb.subscription_id = $2 AND qb.metric = qd.metric
		                      AND qb.period = qd.period)`,
		tenantID, subID, periodStart, periodEnd, planVersionID); err != nil {
		return fmt.Errorf("初始化配额: %w", err)
	}
	return nil
}

// grantPlanDirect 不经过订单直接开通一个套餐，供礼品卡的套餐卡使用。
//
// 快照取套餐的当前版本与在售价格 —— 礼品卡没有下单那一刻，
// 只能以兑换时的套餐定义为准。
func (s *Service) grantPlanDirect(ctx context.Context, tx pgx.Tx,
	tenantID, userID, planID, priceID string) (string, error) {

	var spec provisionSpec
	spec.PlanID = planID
	var price *string
	if priceID != "" {
		price = &priceID
	}

	err := tx.QueryRow(ctx, `
		SELECT p.current_version_id::text,
		       coalesce(pr.currency::text, 'CNY'),
		       coalesce(pr.unit_amount, 0),
		       coalesce(pr.billing_interval, 'month'),
		       coalesce(pr.interval_count, 1)
		  FROM plans p
		  LEFT JOIN prices pr
		    ON pr.tenant_id = p.tenant_id
		   AND pr.product_id = p.product_id
		   AND pr.id = coalesce($3::uuid,
		         (SELECT x.id FROM prices x
		           WHERE x.tenant_id = p.tenant_id AND x.product_id = p.product_id
		             AND x.status = 'active'
		           ORDER BY x.created_at LIMIT 1))
		 WHERE p.tenant_id = $1 AND p.id = $2::uuid AND p.status = 'active'`,
		tenantID, planID, price).Scan(&spec.PlanVersionID, &spec.Currency,
		&spec.UnitAmount, &spec.Interval, &spec.IntervalCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.New(httpx.CodeValidationFailed,
			"这张卡绑定的套餐已经下架了，请联系客服")
	}
	if err != nil {
		return "", err
	}
	if spec.PlanVersionID == "" {
		return "", httpx.New(httpx.CodeValidationFailed, "这张卡绑定的套餐还没有发布版本")
	}
	spec.PriceID = price
	spec.ActorKind = "system"

	return s.provisionSubscription(ctx, tx, tenantID, userID, spec)
}
