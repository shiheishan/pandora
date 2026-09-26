// [INPUT]: 依赖 order_holds.go 的预留父节点与余额冻结、coupon.go 的券校验与核销、reservations.go 的资源预留、settlement.go 的 settlePaymentTx 与 fulfillOrder（零元单当场履约），依赖 platform/db、platform/httpx、middleware 与 idempotencybind 的幂等声明与绑定
// [OUTPUT]: 对外提供 CreateOrderInput / CreateOrderOutput、CheckoutIdempotencyScope、Service.CreateOrder；包内提供 catalogGroupAllowed、catalogPriceCurrentlyValid、captureZeroPayOrder
// [POS]: billing 的新购下单：一个事务里校验目录（用户组、价格窗口、只收 CNY / USD）、预留库存与限购、冻结余额、核销券、绑定幂等键并写出预制响应；零元单（赠送、全额抵扣）建单即捕获并履约。服务骨架在 service.go，结算主链在 settlement.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/idempotencybind"
)

//------------------------------------------------------------------------------
// 下单（SUB-001 价格快照 / XBD-011 可见性 / XBD-012 购买限制）
//------------------------------------------------------------------------------

type CreateOrderInput struct {
	UserID  string
	PlanID  string
	PriceID string
	Claim   middleware.IdempotencyClaim
	// 用余额抵扣的金额（XBD-014）
	UseBalance int64
	// CouponCode 是可选的优惠码
	CouponCode string

	// --- 以下仅供管理端人工单（XBD-015）使用，用户端一律留空 ---
	//
	// ManualActor / ManualReason 标出「这是管理员开的单」；ManualGrant 另外
	// 决定是否全额减免当场履约。只给前两者就是一张交给用户去付的待支付单。
	//
	// 人工单走的是和普通下单完全相同的这条路：同样占库存、同样受限购约束、
	// 同样建预留图、同样产生订单项快照。区别只有两点 —— 全额减免因而
	// payable=0（于是复用既有的零元单捕获直接履约），以及 kind='manual'
	// 且必须带理由。
	//
	// 之所以不另写一条"管理员开单"的路径：那意味着把库存、限购、快照、
	// 履约这些逻辑再实现一遍，两条路一旦分叉，赠送出去的订阅就会和
	// 正常购买的行为不一致，而这种不一致往往几个月后才被发现。
	ManualGrant  bool
	ManualReason string
	ManualActor  string
	// Offline 只给人工单「线下已收款」用（与 ManualGrant 互斥）：建单之后在
	// 同一个事务里按线下渠道结清，走的是与标记已支付完全相同的结算链路
	// （settlePaymentTx），收入、佣金、履约一个不少；建单、入账与幂等记录
	// 要么一起生效，要么一起回滚，不会留下「单建了、钱没记」的中间态。
	Offline *OfflineReceipt
}

type CreateOrderOutput struct {
	DiscountAmount int64  `json:"discount_amount"`
	OrderID        string `json:"order_id"`
	OrderNo        string `json:"order_no"`
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	BalanceApplied int64  `json:"balance_applied"`
	PayableAmount  int64  `json:"payable_amount"`
	Status         string `json:"status"`

	prepared httpx.PreparedResponse
	// settlement 是线下已收款在建单事务里结算的结果，只有 Offline 时非空
	settlement *PaymentWebhookOutput
}

func (o *CreateOrderOutput) PreparedResponse() httpx.PreparedResponse {
	return o.prepared
}

const CheckoutIdempotencyScope = "order_create"

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

func (s *Service) CreateOrder(ctx context.Context, tenantID string, in CreateOrderInput) (*CreateOrderOutput, error) {
	// 幂等声明绑定的是发起请求的人，不是订单归属的人。
	// 普通下单两者相同；人工单是管理员替用户开的，发起人是管理员 ——
	// 拿目标用户去校验会一律报「actor 不匹配」。
	claimActor := in.UserID
	if in.ManualActor != "" {
		// 人工单（赠送或待用户支付）都由管理员发起
		claimActor = in.ManualActor
	}
	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, claimActor, CheckoutIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	var out CreateOrderOutput
	// 事务的 actor 必须是真正发起请求的人：idempotency_keys 的 RLS 是
	// actor_id = current_actor_id()，人工单里 claim 属于管理员，
	// 把 actor 设成目标用户会让绑定阶段一行都看不到（表现为「claim lost」）。
	// orders / subscriptions 这些表的策略只校验租户，所以换成管理员不影响可见性。
	scope := db.Scope{TenantID: tenantID, ActorID: claimActor}

	// 用序列化隔离：库存与限购次数的检查-更新之间不能有写偏斜（XBD-012 验收
	// 「并发购买不突破库存和次数限制」）。调用方需要能重试 40001。
	err := s.pool.InTxSerializableRetry(ctx, scope, func(tx pgx.Tx) error {
		// --- 取套餐与当前版本 ---
		var (
			planName        string
			planStatus      string
			visibility      string
			visibleGroupIDs []string
			visibleFrom     *time.Time
			visibleUntil    *time.Time
			allowNew        bool
			purchaseLimit   *int
			stockTotal      *int
			stockReserved   int
			stockSold       int
			currentVersion  *string
			productID       string
		)
		err := tx.QueryRow(ctx, `
			SELECT name, status, visibility, visible_group_ids::text[],
			       visible_from, visible_until, allow_new_purchase,
			       purchase_limit_per_user, stock_total, stock_reserved, stock_sold,
			       current_version_id, product_id
			  FROM plans
			 WHERE tenant_id = $1 AND id = $2
			 FOR UPDATE`,
			tenantID, in.PlanID).Scan(&planName, &planStatus, &visibility,
			&visibleGroupIDs, &visibleFrom, &visibleUntil, &allowNew,
			&purchaseLimit, &stockTotal, &stockReserved, &stockSold, &currentVersion, &productID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		// XBD-011：不可见套餐不能通过直接 API 下单
		if planStatus != "active" || visibility == "hidden" || visibility == "invite_only" {
			return httpx.NotFoundOrForbidden()
		}
		now := time.Now().UTC()
		if (visibleFrom != nil && visibleFrom.After(now)) ||
			(visibleUntil != nil && !visibleUntil.After(now)) {
			return httpx.NotFoundOrForbidden()
		}
		var userGroupID *string
		if err := tx.QueryRow(ctx, `SELECT user_group_id FROM users
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.UserID).Scan(&userGroupID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if visibility == "group" && !catalogGroupAllowed(userGroupID, visibleGroupIDs) {
			return httpx.NotFoundOrForbidden()
		}
		if !allowNew {
			return httpx.New(httpx.CodeConflict, "该套餐当前不接受新购")
		}
		if currentVersion == nil {
			return httpx.New(httpx.CodeConflict, "该套餐尚未发布可用版本")
		}

		// XBD-012：限购次数
		if purchaseLimit != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO plan_purchase_counters
					(tenant_id, plan_id, user_id, purchased, reserved)
				VALUES ($1, $2, $3, 0, 0)
				ON CONFLICT (tenant_id, plan_id, user_id) DO NOTHING`,
				tenantID, in.PlanID, in.UserID); err != nil {
				return err
			}
			var purchased, reserved int
			if err := tx.QueryRow(ctx, `
				SELECT purchased, reserved FROM plan_purchase_counters
				 WHERE tenant_id = $1 AND plan_id = $2 AND user_id = $3
				 FOR UPDATE`,
				tenantID, in.PlanID, in.UserID).Scan(&purchased, &reserved); err != nil {
				return err
			}
			if purchased+reserved >= *purchaseLimit {
				return httpx.New(httpx.CodeConflict, "已达到该套餐的限购次数")
			}
		}

		// 库存
		if stockTotal != nil && stockSold+stockReserved >= *stockTotal {
			return httpx.New(httpx.CodeConflict, "该套餐已售罄")
		}

		// --- 取价格并快照 ---
		var (
			currency        string
			unitAmount      int64
			interval        string
			intervalCount   int16
			priceStatus     string
			priceGroupID    *string
			priceValidFrom  *time.Time
			priceValidUntil *time.Time
		)
		err = tx.QueryRow(ctx, `
			SELECT currency, unit_amount, billing_interval, interval_count, status,
			       user_group_id, valid_from, valid_until
			  FROM prices
			 WHERE tenant_id = $1 AND id = $2 AND product_id = $3
			   AND currency IN ('CNY','USD')
			 FOR UPDATE`,
			tenantID, in.PriceID, productID).Scan(
			&currency, &unitAmount, &interval, &intervalCount, &priceStatus,
			&priceGroupID, &priceValidFrom, &priceValidUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if priceStatus != "active" {
			return httpx.New(httpx.CodeConflict, "该价格已下架")
		}

		// --- 权益与配额快照（SUB-002「可解释用户最终获得的每一项权益来源」）---
		if priceGroupID != nil && (userGroupID == nil || *userGroupID != *priceGroupID) {
			return httpx.NotFoundOrForbidden()
		}
		if !catalogPriceCurrentlyValid(priceValidFrom, priceValidUntil, now) {
			return httpx.New(httpx.CodeConflict, "该价格当前不在有效期内")
		}

		entitlements, err := jsonAgg(ctx, tx, `
			SELECT coalesce(jsonb_agg(jsonb_build_object('code', code, 'value', value)), '[]'::jsonb)
			  FROM entitlements WHERE plan_version_id = $1`, *currentVersion)
		if err != nil {
			return err
		}
		quotas, err := jsonAgg(ctx, tx, `
			SELECT coalesce(jsonb_agg(jsonb_build_object(
			         'metric', metric, 'limit', limit_value,
			         'unit', unit, 'period', period)), '[]'::jsonb)
			  FROM quota_definitions WHERE plan_version_id = $1`, *currentVersion)
		if err != nil {
			return err
		}

		var planVersionNo int
		var planVersionStatus string
		var frozenAt *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT version,status,frozen_at FROM plan_versions
			  WHERE tenant_id=$1 AND id=$2::uuid AND plan_id=$3::uuid FOR SHARE`,
			tenantID, *currentVersion, in.PlanID).
			Scan(&planVersionNo, &planVersionStatus, &frozenAt); err != nil {
			return err
		}
		if planVersionStatus != "published" || frozenAt == nil {
			return httpx.New(httpx.CodeConflict, "套餐当前版本未完成发布")
		}

		var productName string
		if err := tx.QueryRow(ctx,
			`SELECT name FROM products WHERE tenant_id = $1 AND id = $2`,
			tenantID, productID).Scan(&productName); err != nil {
			return err
		}

		// --- 金额计算 ---
		subtotal := unitAmount

		// 优惠券在余额抵扣之前生效：先打折再用余额，
		// 顺序反过来会让余额被折扣「吃掉」—— 用户付了同样的钱，
		// 余额却多扣了，而这在账面上完全看不出问题。
		coupon, err := applyCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode,
			in.PlanID, currency, subtotal)
		if err != nil {
			return err
		}
		var discount int64
		if coupon != nil {
			discount = coupon.Discount
		}
		// 人工单：全额减免，payable 归零后由下方既有的零元单分支直接履约。
		// 不把原价记进 total —— 赠送没有收入，计进去会虚增营收报表。
		if in.ManualGrant {
			discount = subtotal
		}

		total := subtotal - discount
		balanceApplied := in.UseBalance
		if balanceApplied < 0 {
			balanceApplied = 0
		}
		if balanceApplied > total {
			balanceApplied = total
		}

		payable := total - balanceApplied
		if in.Offline != nil && payable == 0 {
			return httpx.New(httpx.CodeConflict, "这张订单不需要支付，请改用赠送")
		}

		var holdAccounts balanceHoldAccounts
		if balanceApplied > 0 {
			if holdAccounts, err = prepareBalanceHold(ctx, tx, tenantID, in.UserID,
				currency, balanceApplied, payable); err != nil {
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
				 subtotal_amount, discount_amount, tax_amount,
				 total_amount, balance_applied, payable_amount, expires_at, coupon_id,
				 idempotency_key_id, manual_reason, created_by)
			VALUES ($1, $2, $3, $12, 'pending_payment', $4,
			        $5, $6, 0, $7, $8, $9, now() + interval '30 minutes', $10,
			        $11::uuid, $13, $14::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, currency,
			subtotal, discount, total, balanceApplied, payable,
			couponID(coupon), in.Claim.ID,
			orderKindFor(in), nullIfEmpty(in.ManualReason), nullIfEmpty(in.ManualActor),
		).Scan(&orderID, &expiresAt); err != nil {
			return err
		}

		reservationID, err := insertHeldReservation(ctx, tx, tenantID, orderID,
			in.UserID, in.Claim.ID, expiresAt)
		if err != nil {
			return err
		}

		// 核销放在订单落库之后：redemptions 上有 order_id 外键，
		// 提前写会指向一个还不存在的订单
		if err := redeemCoupon(ctx, tx, tenantID, in.UserID, orderID, coupon, currency, reservationID); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items
				(tenant_id, order_id, product_id, price_id, plan_id, plan_version_id,
				 snapshot_product_name, snapshot_plan_name, snapshot_plan_version,
				 snapshot_interval, snapshot_interval_count,
				 snapshot_entitlements, snapshot_quotas,
				 quantity, unit_amount, line_amount, currency)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,1,$14,$14,$15)`,
			tenantID, orderID, productID, in.PriceID, in.PlanID, *currentVersion,
			productName, planName, planVersionNo, interval, intervalCount,
			entitlements, quotas, unitAmount, currency); err != nil {
			return err
		}

		// 占用库存与限购计数
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_stock_reservations
				(tenant_id, reservation_id, order_id, plan_id, quantity)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, 1)`,
			tenantID, reservationID, orderID, in.PlanID,
		); err != nil {
			return err
		}
		stockTag, err := tx.Exec(ctx,
			`UPDATE plans SET stock_reserved = stock_reserved + 1
			  WHERE tenant_id = $1 AND id = $2
			    AND (stock_total IS NULL OR stock_sold + stock_reserved < stock_total)`,
			tenantID, in.PlanID)
		if err != nil {
			return err
		}
		if stockTag.RowsAffected() != 1 {
			return httpx.New(httpx.CodeConflict, "该套餐已售罄")
		}
		if purchaseLimit != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE plan_purchase_counters
				   SET reserved = reserved + 1, updated_at = now()
				 WHERE tenant_id = $1 AND plan_id = $2 AND user_id = $3`,
				tenantID, in.PlanID, in.UserID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO order_purchase_limit_reservations
					(tenant_id, reservation_id, order_id, plan_id, user_id, quantity)
				VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 1)`,
				tenantID, reservationID, orderID, in.PlanID, in.UserID,
			); err != nil {
				return err
			}
		}

		if balanceApplied > 0 {
			if err := postBalanceHold(ctx, tx, tenantID, reservationID, orderID,
				in.UserID, currency, balanceApplied, holdAccounts); err != nil {
				return err
			}
		}

		status := "pending_payment"
		if payable == 0 {
			if err := s.captureZeroPayOrder(ctx, tx, zeroPayCapture{
				TenantID: tenantID, UserID: in.UserID, OrderID: orderID,
				ReservationID: reservationID, BusinessRequestID: in.Claim.ID,
				Kind: "new", PlanID: in.PlanID, Currency: currency, TotalAmount: total,
				BalanceApplied: balanceApplied, HoldAccountID: holdAccounts.HoldID,
				HasPurchaseLimit: purchaseLimit != nil, Coupon: coupon,
			}); err != nil {
				return err
			}
			status = "fulfilled"
		}

		if err := idempotencybind.BindResource(
			ctx, tx, in.Claim, "order", orderID,
		); err != nil {
			return err
		}

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &in.UserID,
			Action: "order.created", ResourceType: "order", ResourceID: &orderID,
			AfterDigest: map[string]any{
				"order_no": orderNo, "total": total, "currency": currency,
				"plan_version": planVersionNo,
			},
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		}); err != nil {
			return err
		}

		// 插件事件和订单写在同一个事务里：订单回滚了却已经通知了插件，
		// 插件那边就会有一笔面板里不存在的订单，而这种不一致没法自动修。
		if err := plugin.EmitOrderCreated(ctx, tx, tenantID, orderID, orderNo,
			in.UserID, currency, total); err != nil {
			return err
		}

		// 线下已收款：订单与幂等绑定都已就位，接着在同一事务里结清。
		// 放在 order.created 之后，插件先看到建单、再看到付款，与在线支付的顺序一致。
		var settled *PaymentWebhookOutput
		if in.Offline != nil {
			settled = &PaymentWebhookOutput{}
			if err := s.settlePaymentTx(ctx, tx, tenantID, offlinePaymentInput(
				orderID, currency, payable, in.ManualActor, *in.Offline), settled); err != nil {
				return err
			}
			// 新建的订单不可能已经有过这笔事件或收款；不是正常结算就是出了错
			if !settled.Processed || settled.AlreadyHandled || settled.PaymentID == "" {
				return errors.New("offline settlement of a new manual order did not capture")
			}
			if err := tx.QueryRow(ctx, `SELECT status FROM orders
				WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, orderID).Scan(&status); err != nil {
				return err
			}
		}

		// 幂等记录最后写：重放拿到的必须是这次请求最终的样子（线下已收款即已履约）
		out = CreateOrderOutput{
			DiscountAmount: discount,
			OrderID:        orderID, OrderNo: orderNo, Currency: currency,
			TotalAmount: total, BalanceApplied: balanceApplied,
			PayableAmount: payable, Status: status,
			settlement: settled,
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
	// 与 HandlePaymentWebhook 同一口径：事务提交后、确实开了订阅才通知节点；
	// 线下已收款看结算结果，零元单（含赠送）看是否已履约
	if out.settlement != nil && out.settlement.SubscriptionID != "" {
		s.notifyUsersChanged(ctx, tenantID)
	} else {
		s.notifyIfFulfilled(ctx, tenantID, out.Status)
	}
	return &out, nil
}

type zeroPayCapture struct {
	TenantID          string
	UserID            string
	OrderID           string
	ReservationID     string
	BusinessRequestID string
	// Kind 是 new 或 addon：新购要结转库存并开订阅，流量包没有库存、履约是发余额
	Kind             string
	PlanID           string
	Currency         string
	TotalAmount      int64
	BalanceApplied   int64
	HoldAccountID    string
	HasPurchaseLimit bool
	Coupon           *couponMatch
}

// captureZeroPayOrder converts every held resource to captured and fulfils a
// zero-payable order before the surrounding create-order transaction commits.
func (s *Service) captureZeroPayOrder(ctx context.Context, tx pgx.Tx, in zeroPayCapture) error {
	var captureTxnID string
	if in.BalanceApplied > 0 {
		if in.BalanceApplied != in.TotalAmount || in.HoldAccountID == "" {
			return errors.New("zero-pay order has an invalid balance hold")
		}
		revenueAccountID, err := EnsureAccount(ctx, tx, in.TenantID,
			AccountPlatformRevenue, in.Currency, nil, "main")
		if err != nil {
			return err
		}
		captureTxnID, err = Post(ctx, tx, in.TenantID, Posting{
			Kind: "order_paid", Currency: in.Currency,
			SourceType: "order", SourceID: &in.OrderID,
			Memo: "zero-pay order balance capture", ActorKind: "user", ActorID: &in.UserID,
			Entries: []Entry{
				{AccountID: in.HoldAccountID, Direction: Debit, Amount: in.BalanceApplied,
					Description: "capture order balance hold"},
				{AccountID: revenueAccountID, Direction: Credit, Amount: in.TotalAmount,
					Description: "order revenue"},
			},
		})
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE balance_holds
			   SET status = 'captured', capture_txn_id = $4::uuid, captured_at = now()
			 WHERE tenant_id = $1 AND order_id = $2::uuid
			   AND reservation_id = $3::uuid AND status = 'held'`,
			in.TenantID, in.OrderID, in.ReservationID, captureTxnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order balance hold capture lost")
		}
	}

	if in.Kind != "new" && in.Kind != "addon" {
		return fmt.Errorf("zero-pay capture does not support order kind %q", in.Kind)
	}
	var tag pgconn.CommandTag
	var err error
	if in.Kind == "new" {
		tag, err = tx.Exec(ctx, `
			UPDATE plans
			   SET stock_reserved = stock_reserved - 1, stock_sold = stock_sold + 1
			 WHERE tenant_id = $1 AND id = $2::uuid AND stock_reserved > 0`,
			in.TenantID, in.PlanID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order stock capture lost")
		}
	}

	if in.HasPurchaseLimit {
		tag, err = tx.Exec(ctx, `
			UPDATE plan_purchase_counters
			   SET reserved = reserved - 1, purchased = purchased + 1, updated_at = now()
			 WHERE tenant_id = $1 AND plan_id = $2::uuid AND user_id = $3::uuid
			   AND reserved > 0`, in.TenantID, in.PlanID, in.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order purchase-limit capture lost")
		}
	}

	if in.Coupon != nil {
		tag, err = tx.Exec(ctx, `
			UPDATE coupons
			   SET reserved_count = reserved_count - 1,
			       redeemed_count = redeemed_count + 1, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid AND reserved_count > 0`,
			in.TenantID, in.Coupon.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order coupon counter capture lost")
		}
		tag, err = tx.Exec(ctx, `
			UPDATE coupon_redemptions
			   SET status = 'captured', captured_at = now()
			 WHERE tenant_id = $1 AND order_id = $2::uuid
			   AND reservation_id = $3::uuid AND status = 'held'`,
			in.TenantID, in.OrderID, in.ReservationID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("zero-pay order coupon capture lost")
		}
	}

	tag, err = tx.Exec(ctx, `
		UPDATE order_reservations
		   SET state = 'captured', captured_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND order_id = $3::uuid
		   AND state = 'held'`, in.TenantID, in.ReservationID, in.OrderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay order parent reservation capture lost")
	}
	tag, err = tx.Exec(ctx, `
		INSERT INTO order_reservation_events
			(tenant_id, reservation_id, order_id, from_state, to_state,
			 event_kind, business_request_id, actor_kind, actor_id)
		VALUES ($1, $2::uuid, $3::uuid, 'held', 'captured',
		        'capture', $4::uuid, 'user', $5::uuid)`,
		in.TenantID, in.ReservationID, in.OrderID, in.BusinessRequestID, in.UserID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay terminal capture event was not inserted")
	}

	tag, err = tx.Exec(ctx, `
		UPDATE orders
		   SET status = 'paid', paid_amount = total_amount, paid_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'pending_payment'`,
		in.TenantID, in.OrderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay order paid transition lost")
	}
	subID := ""
	if in.Kind == "addon" {
		if _, err := fulfillTrafficPackOrder(ctx, tx, in.TenantID, in.OrderID, in.UserID); err != nil {
			return err
		}
	} else if subID, err = s.fulfillOrder(ctx, tx, in.TenantID, in.OrderID, in.UserID); err != nil {
		return err
	}
	// 零元单也算一次支付完成：插件那边不该因为金额是 0 就漏掉这笔。
	return plugin.EmitOrderPaid(ctx, tx, in.TenantID, in.OrderID, in.UserID,
		in.Kind, "", 0, subID)
}
