// [INPUT]: 依赖 plan_change_quote.go 的剩余价值折算、order_holds.go 的预留父节点与余额冻结、coupon.go 的 applyCoupon/redeemCoupon、renewal.go 的 captureZeroPaySubscriptionOrder、checkout.go 的 initQuotaBalances/addInterval、traffic_reset.go 的 LogTrafficReset，依赖 middleware 幂等声明、platform/audit、platform/idempotencybind、domain/plugin
// [OUTPUT]: 对外提供 PlanChangeIdempotencyScope、PlanChangeInput、PlanChangePreview、PlanChangeOrderOutput、PreviewPlanChange、CreatePlanChange；包内提供 fulfillPlanChangeLocked、ensureNoOpenSubscriptionOrder
// [POS]: billing 的变更套餐（D-E-2，迁移 00071）：在原订阅上换套餐，订单 kind='upgrade'；结算经 settlement.go 的回调、零元单经 renewal.go 的订阅单捕获，都落到本文件的履约
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 变更套餐是在原订阅上换一个套餐，不是开第二条订阅（D-E-2）：
//
//	金额   新价（优惠后）减原订阅的剩余价值（plan_change_quote.go）：
//	       为正就补差价；为负就 total = 0，差额在履约时退进余额
//	周期   从履约当天按新套餐的计费周期起算
//	配额   按新套餐版本重置上限与周期，已用量清零并留重置日志；新套餐没有的指标变成不限量
//	凭据   不动（设计「订阅地址不变」），只把有效期跟到新周期末
//	流量包 挂在用户身上，不受影响（D-E-1）
//
// 升级与降级都是 kind='upgrade'（契约定名）；方向只在响应里给前端看，
// 由金额决定：要补钱是 upgrade，不补或退钱是 downgrade。
//
// 剩余价值在下单那一刻算定、写进订单（orders.proration_credit_amount），
// 之后随订单冻结。为了让这个数在付款前不失效，同一条订阅同时只允许一张
// 未完结的续费或变更单（迁移 00071 的唯一索引，这里先给出可读的 409）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/idempotencybind"
)

const PlanChangeIdempotencyScope = "subscription_change_plan_create"

var (
	ErrPlanChangeSamePlan    = httpx.New(httpx.CodeConflict, "与当前套餐相同，请使用续费")
	ErrPlanChangeNotAllowed  = httpx.New(httpx.CodeConflict, "目标套餐不允许变更")
	ErrPlanChangeSubStatus   = httpx.New(httpx.CodeConflict, "当前订阅状态不能变更")
	ErrPlanChangeCurrency    = httpx.New(httpx.CodeConflict, "变更套餐不能更换币种")
	ErrSubscriptionOrderOpen = httpx.New(httpx.CodeConflict,
		"这条订阅还有未完成的续费或变更套餐订单，请先支付或取消")
)

type PlanChangeInput struct {
	UserID         string
	SubscriptionID string
	PlanID         string
	PriceID        string
	UseBalance     int64
	CouponCode     string
	// Claim 只有下单要，试算不落库也不占幂等键。
	Claim middleware.IdempotencyClaim
}

// PlanChangePreview 是试算结果，也是下单时冻结进订单的那组数。
type PlanChangePreview struct {
	Direction       string `json:"direction"`
	Currency        string `json:"currency"`
	Subtotal        int64  `json:"subtotal"`
	ProrationCredit int64  `json:"proration_credit"`
	Discount        int64  `json:"discount"`
	Total           int64  `json:"total"`
	// BalanceRefund 是降级时退进余额的差额，其余情况为 0。
	BalanceRefund    int64      `json:"balance_refund"`
	CurrentPeriodEnd *time.Time `json:"current_period_end"`
	NewPeriodStart   time.Time  `json:"new_period_start"`
	NewPeriodEnd     time.Time  `json:"new_period_end"`
	// CouponFace 是所用优惠码的券面，与优惠码试算同形（R76），没用码时为 null
	CouponFace *CouponFace `json:"coupon"`
}

// PlanChangeOrderOutput 与新购下单同形，另加折算与退余额两项。
type PlanChangeOrderOutput struct {
	CreateOrderOutput
	ProrationCredit int64 `json:"proration_credit"`
	BalanceRefund   int64 `json:"balance_refund"`
}

// planChangeQuote 是一次变更的全部定价与快照，试算与下单共用。
type planChangeQuote struct {
	PlanChangePreview
	PlanVersionID string
	ProductID     string
	ProductName   string
	PlanName      string
	PlanVersionNo int
	Interval      string
	IntervalCount int16
	Entitlements  []byte
	Quotas        []byte
	Coupon        *couponMatch
}

// quotePlanChange 锁住订阅并算出变更到目标套餐与价格的全部金额。
//
// 锁序与续费一致：订阅 → 套餐 → 价格 → 优惠券，账本科目留给调用方最后锁。
func quotePlanChange(ctx context.Context, tx pgx.Tx, tenantID string,
	in PlanChangeInput, now time.Time) (*planChangeQuote, error) {

	for _, id := range []string{in.SubscriptionID, in.PlanID, in.PriceID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, httpx.NotFoundOrForbidden()
		}
	}
	var (
		curPlanID              string
		status                 string
		periodStart, periodEnd *time.Time
	)
	err := tx.QueryRow(ctx, `
		SELECT plan_id::text, status, current_period_start, current_period_end
		  FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
		 FOR UPDATE`, tenantID, in.SubscriptionID, in.UserID).
		Scan(&curPlanID, &status, &periodStart, &periodEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	if !subscriptionAcceptsPaidChange(status) {
		return nil, ErrPlanChangeSubStatus
	}
	if in.PlanID == curPlanID {
		return nil, ErrPlanChangeSamePlan
	}
	if err := ensureNoOpenSubscriptionOrder(ctx, tx, tenantID, in.SubscriptionID); err != nil {
		return nil, err
	}

	q := planChangeQuote{}
	var (
		planStatus, visibility    string
		visibleGroupIDs           []string
		visibleFrom, visibleUntil *time.Time
		allowUpgrade              bool
		currentVersion            *string
	)
	err = tx.QueryRow(ctx, `
		SELECT name, status, visibility, visible_group_ids::text[],
		       visible_from, visible_until, allow_upgrade, current_version_id::text,
		       product_id::text
		  FROM plans
		 WHERE tenant_id = $1 AND id = $2::uuid
		 FOR SHARE`, tenantID, in.PlanID).Scan(&q.PlanName, &planStatus, &visibility,
		&visibleGroupIDs, &visibleFrom, &visibleUntil, &allowUpgrade, &currentVersion,
		&q.ProductID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	// 可见性与新购同一套规则（XBD-011）：看不到的套餐也换不过去。
	if planStatus != "active" || visibility == "hidden" || visibility == "invite_only" ||
		(visibleFrom != nil && visibleFrom.After(now)) ||
		(visibleUntil != nil && !visibleUntil.After(now)) {
		return nil, httpx.NotFoundOrForbidden()
	}
	var userGroupID *string
	if err := tx.QueryRow(ctx, `SELECT user_group_id::text FROM users
		WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, in.UserID).Scan(&userGroupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if visibility == "group" && !catalogGroupAllowed(userGroupID, visibleGroupIDs) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if !allowUpgrade {
		return nil, ErrPlanChangeNotAllowed
	}
	if currentVersion == nil {
		return nil, httpx.New(httpx.CodeConflict, "该套餐尚未发布可用版本")
	}
	q.PlanVersionID = *currentVersion

	var (
		unitAmount      int64
		priceStatus     string
		priceGroupID    *string
		priceValidFrom  *time.Time
		priceValidUntil *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT currency::text, unit_amount, billing_interval, interval_count, status,
		       user_group_id::text, valid_from, valid_until
		  FROM prices
		 WHERE tenant_id = $1 AND id = $2::uuid AND product_id = $3::uuid
		   AND currency IN ('CNY','USD')
		 FOR UPDATE`, tenantID, in.PriceID, q.ProductID).Scan(&q.Currency, &unitAmount,
		&q.Interval, &q.IntervalCount, &priceStatus, &priceGroupID,
		&priceValidFrom, &priceValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	if priceGroupID != nil && (userGroupID == nil || *userGroupID != *priceGroupID) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if priceStatus != "active" {
		return nil, httpx.New(httpx.CodeConflict, "该价格已下架")
	}
	if !catalogPriceCurrentlyValid(priceValidFrom, priceValidUntil, now) {
		return nil, httpx.New(httpx.CodeConflict, "该价格当前不在有效期内")
	}

	var versionStatus string
	var frozenAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT version, status, frozen_at FROM plan_versions
		 WHERE tenant_id = $1 AND id = $2::uuid AND plan_id = $3::uuid FOR SHARE`,
		tenantID, q.PlanVersionID, in.PlanID).Scan(&q.PlanVersionNo, &versionStatus,
		&frozenAt); err != nil {
		return nil, err
	}
	if versionStatus != "published" || frozenAt == nil {
		return nil, httpx.New(httpx.CodeConflict, "套餐当前版本未完成发布")
	}
	if q.Entitlements, err = jsonAgg(ctx, tx, `
		SELECT coalesce(jsonb_agg(jsonb_build_object('code', code, 'value', value)), '[]'::jsonb)
		  FROM entitlements WHERE plan_version_id = $1`, q.PlanVersionID); err != nil {
		return nil, err
	}
	if q.Quotas, err = jsonAgg(ctx, tx, `
		SELECT coalesce(jsonb_agg(jsonb_build_object(
		         'metric', metric, 'limit', limit_value,
		         'unit', unit, 'period', period)), '[]'::jsonb)
		  FROM quota_definitions WHERE plan_version_id = $1`, q.PlanVersionID); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT name FROM products
		WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, q.ProductID).Scan(&q.ProductName); err != nil {
		return nil, err
	}

	// 剩余价值：周期边界缺失（从未开通过的订阅）就没有可折的东西。
	if periodStart != nil && periodEnd != nil {
		basis, err := loadProrationBasis(ctx, tx, tenantID, in.SubscriptionID,
			*periodStart, *periodEnd)
		if err != nil {
			return nil, err
		}
		if basis.Currency != "" && basis.Currency != q.Currency {
			return nil, ErrPlanChangeCurrency
		}
		q.ProrationCredit = prorationCredit(basis, now)
	}

	// 优惠券按新套餐的价格算（与新购同一口径），升级、降级都能用；
	// 降级时折扣让差额变大，多出来的一并退进余额（2026-09-24 用户拍板）。
	q.Subtotal = unitAmount
	if q.Coupon, err = applyCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode,
		in.PlanID, q.Currency, q.Subtotal); err != nil {
		return nil, err
	}
	if q.Coupon != nil {
		q.Discount = q.Coupon.Discount
	}
	q.CouponFace = q.Coupon.face()
	q.Total = orderTotal(q.Subtotal, q.Discount, q.ProrationCredit, 0)
	q.BalanceRefund = max(q.ProrationCredit-(q.Subtotal-q.Discount), 0)
	q.Direction = "downgrade"
	if q.Total > 0 {
		q.Direction = "upgrade"
	}
	q.CurrentPeriodEnd = periodEnd
	q.NewPeriodStart = now
	q.NewPeriodEnd = addInterval(now, q.Interval, int(q.IntervalCount))
	return &q, nil
}

// ensureNoOpenSubscriptionOrder 拒绝在同一条订阅上叠第二张未完结的续费或变更单。
// 数据库有同义的唯一索引兜底（00071），这里先给一个用户看得懂的 409。
func ensureNoOpenSubscriptionOrder(ctx context.Context, tx pgx.Tx, tenantID, subID string) error {
	var open bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM orders
		                WHERE tenant_id = $1 AND subscription_id = $2::uuid
		                  AND kind IN ('renewal', 'upgrade')
		                  AND status IN ('draft', 'pending_payment', 'processing', 'paid'))`,
		tenantID, subID).Scan(&open); err != nil {
		return err
	}
	if open {
		return ErrSubscriptionOrderOpen
	}
	return nil
}

// PreviewPlanChange 试算变更套餐，不写库。
func (s *Service) PreviewPlanChange(ctx context.Context, tenantID string,
	in PlanChangeInput) (*PlanChangePreview, error) {
	var out PlanChangePreview
	err := s.pool.InTx(ctx, dbScope(tenantID, in.UserID), func(tx pgx.Tx) error {
		q, err := quotePlanChange(ctx, tx, tenantID, in, time.Now().UTC())
		if err != nil {
			return err
		}
		out = q.PlanChangePreview
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}

// CreatePlanChange 建一张变更套餐订单（kind='upgrade'）。无需外部付款时当场履约。
func (s *Service) CreatePlanChange(ctx context.Context, tenantID string,
	in PlanChangeInput) (*PlanChangeOrderOutput, error) {
	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, in.UserID, PlanChangeIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("create plan change: %w", err)
	}

	var out PlanChangeOrderOutput
	err := s.pool.InTxSerializableRetry(ctx, dbScope(tenantID, in.UserID), func(tx pgx.Tx) error {
		q, err := quotePlanChange(ctx, tx, tenantID, in, time.Now().UTC())
		if err != nil {
			return err
		}
		balanceApplied := min(max(in.UseBalance, 0), q.Total)
		payable := q.Total - balanceApplied
		var holdAccounts balanceHoldAccounts
		if balanceApplied > 0 {
			if holdAccounts, err = prepareBalanceHold(ctx, tx, tenantID, in.UserID,
				q.Currency, balanceApplied, payable); err != nil {
				return err
			}
		}

		orderNo, err := newOrderNo()
		if err != nil {
			return err
		}
		var orderID string
		var expiresAt time.Time
		if err := tx.QueryRow(ctx, `
			INSERT INTO orders
				(tenant_id, order_no, user_id, kind, status, currency,
				 subtotal_amount, discount_amount, tax_amount, proration_credit_amount,
				 total_amount, balance_applied, payable_amount, expires_at, coupon_id,
				 subscription_id, idempotency_key_id)
			VALUES ($1, $2, $3::uuid, 'upgrade', 'pending_payment', $4,
			        $5, $6, 0, $7, $8, $9, $10, now() + interval '30 minutes', $11,
			        $12::uuid, $13::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, q.Currency, q.Subtotal, q.Discount,
			q.ProrationCredit, q.Total, balanceApplied, payable, couponID(q.Coupon),
			in.SubscriptionID, in.Claim.ID,
		).Scan(&orderID, &expiresAt); err != nil {
			if db.IsUniqueViolation(err) {
				return ErrSubscriptionOrderOpen
			}
			return err
		}

		reservationID, err := insertHeldReservation(ctx, tx, tenantID, orderID,
			in.UserID, in.Claim.ID, expiresAt)
		if err != nil {
			return err
		}
		if err := redeemCoupon(ctx, tx, tenantID, in.UserID, orderID, q.Coupon,
			q.Currency, reservationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items
				(tenant_id, order_id, product_id, price_id, plan_id, plan_version_id,
				 snapshot_product_name, snapshot_plan_name, snapshot_plan_version,
				 snapshot_interval, snapshot_interval_count,
				 snapshot_entitlements, snapshot_quotas,
				 quantity, unit_amount, line_amount, currency)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7, $8, $9,
			        $10, $11, $12, $13, 1, $14, $14, $15)`,
			tenantID, orderID, q.ProductID, in.PriceID, in.PlanID, q.PlanVersionID,
			q.ProductName, q.PlanName, q.PlanVersionNo, q.Interval, q.IntervalCount,
			q.Entitlements, q.Quotas, q.Subtotal, q.Currency); err != nil {
			return err
		}
		if balanceApplied > 0 {
			if err := postBalanceHold(ctx, tx, tenantID, reservationID, orderID,
				in.UserID, q.Currency, balanceApplied, holdAccounts); err != nil {
				return err
			}
		}

		status := "pending_payment"
		if payable == 0 {
			var couponIDPtr *string
			if q.Coupon != nil {
				id := q.Coupon.ID
				couponIDPtr = &id
			}
			if err := s.captureZeroPaySubscriptionOrder(ctx, tx, zeroPaySubscriptionCapture{
				Kind: "upgrade", TenantID: tenantID, UserID: in.UserID, OrderID: orderID,
				SubscriptionID: in.SubscriptionID, ReservationID: reservationID,
				BusinessRequestID: in.Claim.ID, Currency: q.Currency,
				SubtotalAmount: q.Subtotal, DiscountAmount: q.Discount,
				TotalAmount: q.Total, BalanceApplied: balanceApplied,
				CouponID: couponIDPtr, HoldAccountID: holdAccounts.HoldID,
				RevenueAccountID: holdAccounts.RevenueID, ProrationCredit: q.ProrationCredit,
			}); err != nil {
				return err
			}
			status = "fulfilled"
		}

		out = PlanChangeOrderOutput{
			CreateOrderOutput: CreateOrderOutput{
				OrderID: orderID, OrderNo: orderNo, Currency: q.Currency,
				DiscountAmount: q.Discount, TotalAmount: q.Total,
				BalanceApplied: balanceApplied, PayableAmount: payable, Status: status,
			},
			ProrationCredit: q.ProrationCredit, BalanceRefund: q.BalanceRefund,
		}
		prepared, err := httpx.PrepareJSON(http.StatusCreated, out)
		if err != nil {
			return err
		}
		out.prepared = prepared
		if err := idempotencybind.BindResource(ctx, tx, in.Claim, "order", orderID); err != nil {
			return err
		}
		if err := idempotencybind.CompleteSuccessJSON(
			ctx, tx, in.Claim, "order", orderID, prepared,
		); err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &in.UserID,
			Action: "subscription.plan_change_created", ResourceType: "order",
			ResourceID: &orderID, APIDomain: "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"subscription_id": in.SubscriptionID, "order_no": orderNo,
				"to_plan_id": in.PlanID, "direction": q.Direction,
				"proration_credit": q.ProrationCredit, "total": q.Total,
				"balance_refund": q.BalanceRefund, "currency": q.Currency,
			},
		}); err != nil {
			return err
		}
		if err := plugin.EmitOrderCreated(ctx, tx, tenantID, orderID, orderNo,
			in.UserID, q.Currency, q.Total); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		if db.IsSerializationFailure(err) {
			return nil, httpx.New(httpx.CodeConflict, "请求冲突，请重试")
		}
		return nil, httpx.Internal(err)
	}
	s.notifyIfFulfilled(ctx, tenantID, out.Status)
	return &out, nil
}

// fulfillPlanChangeLocked 把已支付的变更单落到订阅上。调用方已按
// 订单 → 订阅 的顺序锁住两者，并已把订单转到 paid。
func (s *Service) fulfillPlanChangeLocked(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, userID, subID string) (string, error) {

	var (
		orderSubID, currency                string
		subtotal, discount, prorationCredit int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(subscription_id::text, ''), currency::text,
		       subtotal_amount, discount_amount, proration_credit_amount
		  FROM orders
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
		   AND kind = 'upgrade' AND status = 'paid'`,
		tenantID, orderID, userID).Scan(&orderSubID, &currency, &subtotal, &discount,
		&prorationCredit); err != nil {
		return "", fmt.Errorf("读取变更订单: %w", err)
	}
	if subID == "" || orderSubID != subID {
		return "", errors.New("plan change order is not bound to the locked subscription")
	}

	var (
		planID, planVersionID, interval string
		priceID                         *string
		intervalCount                   int16
		unitAmount                      int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT plan_id::text, plan_version_id::text, price_id::text,
		       snapshot_interval, snapshot_interval_count, unit_amount
		  FROM order_items
		 WHERE tenant_id = $1 AND order_id = $2::uuid AND plan_id IS NOT NULL`,
		tenantID, orderID).Scan(&planID, &planVersionID, &priceID, &interval,
		&intervalCount, &unitAmount); err != nil {
		return "", fmt.Errorf("读取变更订单行: %w", err)
	}

	var fromPlanID, fromVersionID, oldStatus string
	var oldEnd *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT plan_id::text, plan_version_id::text, status, current_period_end
		  FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`,
		tenantID, subID, userID).Scan(&fromPlanID, &fromVersionID, &oldStatus,
		&oldEnd); err != nil {
		return "", err
	}

	now := time.Now().UTC()
	newEnd := addInterval(now, interval, int(intervalCount))

	// 配额按新套餐重新起算，清零前的用量先留日志。配额行不能删：人工调整记录
	// （00006 的配额调整表）挂在行上且只许追加（DATA-003）。新套餐没有的指标把
	// 上限置空 —— 上限为空在节点下发与扣量里本来就是「不限量」，与新开一条
	// 该套餐的订阅（没有这一行）效果相同。
	type usedQuota struct {
		metric   string
		consumed int64
	}
	var before []usedQuota
	rows, err := tx.Query(ctx, `
		SELECT metric, consumed FROM quota_balances
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid ORDER BY id`, tenantID, subID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var u usedQuota
		if err := rows.Scan(&u.metric, &u.consumed); err != nil {
			rows.Close()
			return "", err
		}
		before = append(before, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET status = 'active', plan_id = $3::uuid, plan_version_id = $4::uuid,
		       price_id = $5::uuid, current_period_start = $6, current_period_end = $7,
		       snapshot_currency = $8, snapshot_amount = $9, grace_end = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`,
		tenantID, subID, planID, planVersionID, priceID, now, newEnd, currency,
		unitAmount); err != nil {
		return "", fmt.Errorf("变更订阅套餐: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE quota_balances qb
		   SET limit_value = qd.limit_value,
		       granted = coalesce(qd.limit_value, 0),
		       consumed = 0,
		       period_start = $4,
		       period_end = CASE WHEN qb.period = 'total' THEN NULL::timestamptz
		                         ELSE $5::timestamptz END,
		       notified_thresholds = '{}',
		       overage_applied_at = NULL,
		       updated_at = now()
		  FROM quota_balances cur
		  LEFT JOIN quota_definitions qd
		    ON qd.plan_version_id = $3::uuid
		   AND qd.metric = cur.metric AND qd.period = cur.period
		 WHERE qb.id = cur.id
		   AND cur.tenant_id = $1 AND cur.subscription_id = $2::uuid`,
		tenantID, subID, planVersionID, now, newEnd); err != nil {
		return "", fmt.Errorf("按新套餐重置配额: %w", err)
	}
	// 新套餐多出来的指标补上配额行（已有的在上面改过，这里跳过）。
	if err := initQuotaBalances(ctx, tx, tenantID, subID, planVersionID, now, newEnd); err != nil {
		return "", err
	}
	for _, u := range before {
		if err := LogTrafficReset(ctx, tx, tenantID, subID, userID, u.metric,
			"plan_change", u.consumed, nil, ""); err != nil {
			return "", fmt.Errorf("记录变更重置: %w", err)
		}
	}
	// 凭据不换，只跟着新周期走。
	if _, err := tx.Exec(ctx, `
		UPDATE subscription_credentials SET expires_at = $3
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid AND status = 'active'`,
		tenantID, subID, newEnd); err != nil {
		return "", fmt.Errorf("延长凭据有效期: %w", err)
	}

	// 降级：剩余价值抵完新价还有余，差额退进余额。那部分钱此前已记成平台收入，
	// 这里冲回收入、记成欠用户的余额（负债），不是凭空造钱。
	refund := max(prorationCredit-(subtotal-discount), 0)
	if refund > 0 {
		accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, []ledgerAccountSpec{
			{Key: "revenue", AccountType: AccountPlatformRevenue, Currency: currency, OwnerRef: "main"},
			{Key: "balance", AccountType: AccountUserBalance, Currency: currency, UserID: &userID},
		})
		if err != nil {
			return "", err
		}
		if _, err := Post(ctx, tx, tenantID, Posting{
			Kind: "plan_change_refund", Currency: currency,
			SourceType: "order", SourceID: &orderID,
			Memo: "plan change remaining value to balance", ActorKind: "system",
			Entries: []Entry{
				{AccountID: accounts["revenue"], Direction: Debit, Amount: refund,
					Description: "reverse unused plan revenue"},
				{AccountID: accounts["balance"], Direction: Credit, Amount: refund,
					Description: "plan change refund to balance"},
			},
		}); err != nil {
			return "", err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status,
			 actor_kind, order_id, payload)
		VALUES ($1, $2::uuid, 'plan_changed', $3, 'active', 'payment', $4::uuid, $5)`,
		tenantID, subID, oldStatus, orderID, map[string]any{
			"from_plan_id": fromPlanID, "from_plan_version_id": fromVersionID,
			"to_plan_id": planID, "to_plan_version_id": planVersionID,
			"previous_end": oldEnd, "period_end": newEnd,
			"proration_credit": prorationCredit, "balance_refund": refund,
		}); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'paid'`, tenantID, orderID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("plan change fulfilment transition lost")
	}
	return subID, nil
}
