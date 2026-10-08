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
	// Expect 是确认时带回的报价，见 Expectation
	Expect *Expectation

	// --- 以下仅供后台人工开单（manual_order.go）用，门户改套餐一律留空 ---
	//
	// 用户已有别的套餐的订阅时，人工开单不再新开订阅、换链接，而是在原订阅上开一张
	// 变更单（2026-10-07 规则）。与人工续费同一套字段：ManualActor 非空即人工变更，
	// 幂等声明属于管理员、scope 是人工开单的 order_create（00134 放开这一组合）；
	// ManualGrant 全额减免当场履约（新价算 0 元，剩余价值全额退进余额）；Offline 建单
	// 后在同一事务里按线下渠道结清。审计 order.manual_created 与订单写在同一个事务里。
	ManualGrant      bool
	ManualReason     string
	ManualActor      string
	Offline          *OfflineReceipt
	ManualSettlement string
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

// planChangeQuote 是一次变更的全部定价与快照（建单用）。
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
	CreditDetail  *CreditDetail
}

// quotePlanChange 锁住订阅并算出变更到目标套餐与价格的全部金额（建单用；报价走
// Service.Quote，同样三步读取但不锁）。剩余价值的时间比例按 asOf 算（报价时刻，见 quoteTime）。
//
// 锁序与续费一致：订阅 → 套餐 → 价格 → 优惠券，账本科目留给调用方最后锁。
func quotePlanChange(ctx context.Context, tx pgx.Tx, tenantID string,
	in PlanChangeInput, now, asOf time.Time) (*planChangeQuote, error) {

	src, err := loadChangeSourceTx(ctx, tx, tenantID, in.UserID, in.SubscriptionID, true)
	if err != nil {
		return nil, err
	}
	if in.PlanID == src.PlanID {
		return nil, ErrPlanChangeSamePlan
	}
	target, err := loadChangeTargetTx(ctx, tx, tenantID, in.PlanID, in.PriceID,
		src.UserGroupID, in.ManualActor != "", now)
	if err != nil {
		return nil, err
	}
	price := target.Prices[0]
	snap, err := loadPlanSnapshotTx(ctx, tx, tenantID, in.PlanID, target.PlanVersionID, target.ProductID)
	if err != nil {
		return nil, err
	}
	q := planChangeQuote{
		PlanVersionID: target.PlanVersionID, ProductID: target.ProductID,
		ProductName: snap.ProductName, PlanName: target.PlanName, PlanVersionNo: snap.PlanVersionNo,
		Interval: price.Interval, IntervalCount: price.IntervalCount,
		Entitlements: snap.Entitlements, Quotas: snap.Quotas,
	}
	q.Currency = price.Currency

	// 剩余价值：周期边界缺失（从未开通过的订阅）就没有可折的东西。
	if q.ProrationCredit, q.CreditDetail, err = src.credit(asOf, q.Currency); err != nil {
		return nil, err
	}

	// 优惠券按新套餐的价格算（与新购同一口径），升级、降级都能用；
	// 降级时折扣让差额变大，多出来的一并退进余额（2026-09-24 用户拍板）。
	q.Subtotal = price.UnitAmount
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
	q.CurrentPeriodEnd = src.PeriodEnd
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

// CreatePlanChange 建一张变更套餐订单（kind='upgrade'）。无需外部付款时当场履约。
// 后台人工开单遇到不同套餐时也走这里（in.ManualActor 非空，见 PlanChangeInput）。
func (s *Service) CreatePlanChange(ctx context.Context, tenantID string,
	in PlanChangeInput) (*PlanChangeOrderOutput, error) {
	// 人工变更的幂等声明属于发起开单的管理员（与人工续费同理）
	claimActor, claimScope := in.UserID, PlanChangeIdempotencyScope
	if in.ManualActor != "" {
		claimActor, claimScope = in.ManualActor, CheckoutIdempotencyScope
	}
	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, claimActor, claimScope,
	); err != nil {
		return nil, fmt.Errorf("create plan change: %w", err)
	}

	var out PlanChangeOrderOutput
	err := s.pool.InTxSerializableRetry(ctx, dbScope(tenantID, claimActor), func(tx pgx.Tx) error {
		now := time.Now().UTC()
		asOf, err := quoteTime(in.Expect, now)
		if err != nil {
			return err
		}
		q, err := quotePlanChange(ctx, tx, tenantID, in, now, asOf)
		if err != nil {
			return err
		}
		// 人工赠送：新价全额减免（与人工续费、人工新购同一写法），剩余价值于是全部
		// 退进余额。折算口径不变，只是新价这一侧算 0 元。
		if in.ManualGrant {
			q.waiveNewPrice()
		}
		// 余额经 purchase.ApplyBalance 收尾（最低付款额、Forced；付不了的回 422，只有换套餐的零头能免）
		// 只有门户换套餐抵扣后的零头能免（用户 8.1 第 1 题），后台开单不免
		bal, err := balancePlan(ctx, tx, tenantID, in.UserID, q.Currency, q.Total, in.UseBalance,
			balanceOpts{Offline: in.Offline != nil, Manual: in.ManualActor != "", AllowWaive: in.ManualActor == ""})
		if err != nil {
			return err
		}
		if err := checkExpectation(in.Expect, q.Total, bal); err != nil {
			return err
		}
		if bal.Waived > 0 {
			q.Discount, q.Total = waiveIntoDiscount(q.Coupon, q.Discount, q.Total, bal.Waived)
		}
		balanceApplied, payable := bal.Applied, bal.Payable
		if in.Offline != nil && payable == 0 {
			return httpx.New(httpx.CodeConflict, "原套餐的剩余价值已经抵完新价，这张单不需要支付，请改用赠送")
		}
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
				 subscription_id, idempotency_key_id, manual_reason, created_by)
			VALUES ($1, $2, $3::uuid, 'upgrade', 'pending_payment', $4,
			        $5, $6, 0, $7, $8, $9, $10, now() + interval '30 minutes', $11,
			        $12::uuid, $13::uuid, $14, $15::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, q.Currency, q.Subtotal, q.Discount,
			q.ProrationCredit, q.Total, balanceApplied, payable, couponID(q.Coupon),
			in.SubscriptionID, in.Claim.ID,
			nullIfEmpty(in.ManualReason), nullIfEmpty(in.ManualActor),
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
				ManualGrant: in.ManualGrant, SmallDueWaived: bal.Waived,
			}); err != nil {
				return err
			}
			status = "fulfilled"
		}

		if err := idempotencybind.BindResource(ctx, tx, in.Claim, "order", orderID); err != nil {
			return err
		}
		// 人工变更「线下已收款」：订单与幂等绑定都已就位，在同一事务里按线下渠道
		// 结清，走与标记已支付相同的 settlePaymentTx（同人工续费）
		var settled *PaymentWebhookOutput
		if in.Offline != nil {
			if settled, status, err = s.settleManualOfflineTx(ctx, tx, tenantID, orderID,
				q.Currency, payable, in.ManualActor, *in.Offline); err != nil {
				return err
			}
		}

		out = PlanChangeOrderOutput{
			CreateOrderOutput: CreateOrderOutput{
				OrderID: orderID, OrderNo: orderNo, Currency: q.Currency,
				DiscountAmount: q.Discount, TotalAmount: q.Total,
				BalanceApplied: balanceApplied, PayableAmount: payable, Status: status,
				settlement: settled,
			},
			ProrationCredit: q.ProrationCredit, BalanceRefund: q.BalanceRefund,
		}
		prepared, err := httpx.PrepareJSON(http.StatusCreated, out)
		if err != nil {
			return err
		}
		out.prepared = prepared
		if err := idempotencybind.CompleteSuccessJSON(
			ctx, tx, in.Claim, "order", orderID, prepared,
		); err != nil {
			return err
		}
		if in.ManualActor != "" {
			// 人工变更的审计与订单同一事务：没有审计的人工单等于没人负责
			entry := manualPlanChangeAudit(in, out, q)
			entry.RequestID = httpx.RequestIDFrom(ctx)
			if err := audit.Write(ctx, tx, tenantID, entry); err != nil {
				return err
			}
		} else if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &in.UserID,
			Action: "subscription.plan_change_created", ResourceType: "order",
			ResourceID: &orderID, APIDomain: "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"subscription_id": in.SubscriptionID, "order_no": orderNo,
				"to_plan_id": in.PlanID, "direction": q.Direction,
				"proration_credit": q.ProrationCredit, "total": q.Total,
				"balance_refund": q.BalanceRefund, "currency": q.Currency,
				"small_due_waived": bal.Waived,
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
