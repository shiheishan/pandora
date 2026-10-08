package billing

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
	// RejectSamePlan 由门户新购设置（规则 3）：用户已有这个套餐、可以原地续费的订阅时
	// 拒绝新开（ErrSamePlanUseRenewal），门户改走续费（NewCopy 时不拦）；同一套餐已有未付款
	// 的新购单时回 409 order_pending。人工开单按 Target 分派（CreateManualOrder），不设它。
	RejectSamePlan bool
	// NewCopy 是门户「另买一份」的显式意图：设了它就不拦同套餐（RejectSamePlan 只在
	// !NewCopy 时生效），开出来的是一份新订阅、新链接。
	NewCopy bool
	// Label 是给新的一份起的备注名（经 purchase.NormalizeLabel），付款前存进
	// orders.subscription_label，履约建订阅时写上；会和已有一份在 App 里重名时必填。
	Label string
	// Expect 是确认时带回的报价，见 Expectation
	Expect *Expectation

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
		now := time.Now().UTC()
		// --- 取套餐与当前版本（与报价同一份校验，checkout_catalog.go）---
		plan, err := loadNewPurchasePlanTx(ctx, tx, tenantID, in.UserID, in.PlanID, true, now)
		if err != nil {
			return err
		}
		// 同套餐只续不新开（规则 3，same_plan.go）：门户新购遇到可原地续费的同套餐订阅
		// 拒绝，新开会换订阅链接；「另买一份」（NewCopy）是显式意图，不拦。只读不锁：这里
		// 已锁着套餐行，续费的锁序是先订阅后套餐，再锁订阅会交叉；序列化隔离兜住并发。
		if in.RejectSamePlan && !in.NewCopy {
			if samePlanSub, err := renewableSamePlanSubscription(ctx, tx, tenantID,
				in.UserID, in.PlanID, false); err != nil {
				return err
			} else if samePlanSub != "" {
				return ErrSamePlanUseRenewal
			}
		}
		label, err := newCopyLabel(ctx, tx, tenantID, in, plan.PlanName)
		if err != nil {
			return err
		}
		// 防重复下单：门户新购（RejectSamePlan 是门户新购的标记）同一套餐同时只能有一张未付款
		// 的新购单；人工单不受限。两个标签页同时点，靠序列化隔离保证只有一张成功：后提交的
		// 那个拿到 40001，重试时就能看到前一张
		if in.RejectSamePlan && in.ManualActor == "" {
			if err := ensureNoPendingNewOrder(ctx, tx, tenantID, in.UserID, in.PlanID, plan.PlanName); err != nil {
				return err
			}
		}

		// XBD-012：限购次数
		if plan.PurchaseLimit != nil {
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
			if purchased+reserved >= *plan.PurchaseLimit {
				return httpx.New(httpx.CodeConflict, "已达到该套餐的限购次数")
			}
		}

		// 库存
		if plan.StockTotal != nil && plan.StockSold+plan.StockReserved >= *plan.StockTotal {
			return httpx.New(httpx.CodeConflict, "该套餐已售罄")
		}

		// --- 取价格并快照 ---
		price, err := loadNewPurchasePriceTx(ctx, tx, tenantID, plan.ProductID, in.PriceID,
			plan.UserGroupID, true, now)
		if err != nil {
			return err
		}
		currency, unitAmount := price.Currency, price.UnitAmount
		interval, intervalCount := price.Interval, price.IntervalCount

		// --- 权益与配额快照（SUB-002「可解释用户最终获得的每一项权益来源」）---
		snap, err := loadPlanSnapshotTx(ctx, tx, tenantID, in.PlanID, plan.CurrentVersion, plan.ProductID)
		if err != nil {
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
		// 余额经 purchase.ApplyBalance 收尾（最低付款额、Forced、SmallDue 免单）
		bal, err := balancePlan(ctx, tx, tenantID, in.UserID, currency, total,
			in.UseBalance, in.Offline != nil)
		if err != nil {
			return err
		}
		if err := checkExpectation(in.Expect, total, bal); err != nil {
			return err
		}
		if bal.Waived > 0 {
			discount, total = waiveIntoDiscount(coupon, discount, total, bal.Waived)
		}
		balanceApplied, payable := bal.Applied, bal.Payable
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
				 idempotency_key_id, manual_reason, created_by, subscription_label)
			VALUES ($1, $2, $3, $12, 'pending_payment', $4,
			        $5, $6, 0, $7, $8, $9, now() + interval '30 minutes', $10,
			        $11::uuid, $13, $14::uuid, $15)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, currency,
			subtotal, discount, total, balanceApplied, payable,
			couponID(coupon), in.Claim.ID,
			orderKindFor(in), nullIfEmpty(in.ManualReason), nullIfEmpty(in.ManualActor),
			nullIfEmpty(label),
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
			tenantID, orderID, plan.ProductID, in.PriceID, in.PlanID, plan.CurrentVersion,
			snap.ProductName, plan.PlanName, snap.PlanVersionNo, interval, intervalCount,
			snap.Entitlements, snap.Quotas, unitAmount, currency); err != nil {
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
		if plan.PurchaseLimit != nil {
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
				HasPurchaseLimit: plan.PurchaseLimit != nil, Coupon: coupon,
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
				"plan_version": snap.PlanVersionNo, "new_copy": in.NewCopy,
				"small_due_waived": bal.Waived,
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
