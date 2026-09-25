// [INPUT]: 依赖 reservations.go 的资源预留与科目锁、ledger.go 的记账、commission.go 的计提，依赖 platform/db、platform/httpx、middleware 的幂等声明
// [OUTPUT]: 对外提供 Service、CreateOrder、HandlePaymentWebhook 及其输入输出类型、CheckoutIdempotencyScope；包内提供 notifyUsersChanged / notifyIfFulfilled（提交后通知节点，零元单建单即履约也发）、provisionSubscription、initQuotaBalances（新开订阅与变更套餐共用的配额初始化）、addInterval
// [POS]: billing 的结账与支付回调主链路，回调按 kind 分派履约（upgrade 交 plan_change.go）；mark-paid（manual_order.go）与补偿查询（payments.go）都复用 HandlePaymentWebhook 与 PaymentWebhookOutput
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
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/idempotencybind"
)

type Service struct {
	pool *db.Pool
	// envelope 用于把订阅 token 可还原地存起来（面板要展示给用户）
	envelope *crypto.Envelope
	// onUsersChanged 在履约事务提交之后调用，告诉节点侧「可服务用户集合变了」。
	//
	// 用回调而不是直接持有 realtime.Hub：那是传输层，计费域不该反向依赖它。
	// 由各网关在装配时注入；为 nil 时行为与接线之前一致。
	onUsersChanged func(ctx context.Context, tenantID string)
}

func NewService(pool *db.Pool, envelope *crypto.Envelope) *Service {
	return &Service{pool: pool, envelope: envelope}
}

// SetUsersChangedNotifier 注入「用户集合已变化」的通知方式。
//
// 不接这个回调时，节点只能靠自己那轮 15 秒轮询发现新用户——付款成功到
// 真正能连上之间会空出十几秒，用户看到的是「付了钱连不上」。
func (s *Service) SetUsersChangedNotifier(fn func(ctx context.Context, tenantID string)) {
	s.onUsersChanged = fn
}

// notifyUsersChanged 只在事务提交之后调用。
//
// 放进事务里发信号，会出现事务回滚了、通知却已经发出去的情况：节点跑去拉
// 一份并不存在的变更，白跑一趟还可能把自己的版本号推歪。
func (s *Service) notifyUsersChanged(ctx context.Context, tenantID string) {
	if s.onUsersChanged == nil {
		return
	}
	s.onUsersChanged(ctx, tenantID)
}

// notifyIfFulfilled 给建单即履约的零元单发通知：赠送、余额或券全额抵扣、零元续费与
// 变更都在建单事务里开通或延长订阅，不经过支付回调，以前节点要等轮询才看到
func (s *Service) notifyIfFulfilled(ctx context.Context, tenantID, status string) {
	if status == "fulfilled" {
		s.notifyUsersChanged(ctx, tenantID)
	}
}

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

//------------------------------------------------------------------------------
// 支付回调（PAY-003 幂等 / PAY-005 账本 / SUB-004 订阅激活）
//------------------------------------------------------------------------------

type PaymentWebhookInput struct {
	ProviderCode      string
	ProviderEventID   string
	ProviderPaymentID string
	EventType         string
	// OrderID 与 OrderNo 二选一。
	// 易支付这类协议回调时只带对外短单号（out_trade_no），拿不到内部 UUID，
	// 故支持按 order_no 定位；两者都给时以 OrderID 为准。
	OrderID           string
	OrderNo           string
	Amount            int64
	Currency          string
	FeeAmount         int64
	RawPayload        map[string]any
	SignatureVerified bool
}

// PaymentWebhookOutput 是一次回调处理的结果。
//
// Processed 与 AlreadyHandled 互斥：前者表示这次真的推进了业务，后者表示这
// 笔钱此前已经入账、本次没有产生新的业务结果。
//
// 三个 ID 的语义按「本次是否产生」区分，别混为一谈：
//   - SubscriptionID、LedgerTxnID 只在 Processed 时有值，它们指向本次新建的
//     订阅与账务交易。
//   - PaymentID 在两种情况下都可能有值。Processed 时是本次记下的那笔支付；
//     AlreadyHandled 时则指向此前已记录的同一笔——渠道换个 event_id 重发
//     同一笔支付时，调用方需要它来对账，返回空反而丢了信息。
//
// 这段注释是补写的：原先没有任何地方写明 AlreadyHandled 时 PaymentID 该不该
// 有值，于是 unexpected_payment.go 那条路径返回了它，而 settlement 测试断言
// 它必须为空，两边各自成理，谁也不知道对方的约定。
//
// json tag 是后台 POST v1/orders/{id}/mark-paid 的响应形状（契约 snake_case）。
// SignatureFailed 只给回调入口用来决定是否回渠道 401，不对外暴露。
type PaymentWebhookOutput struct {
	Processed       bool   `json:"processed"`
	AlreadyHandled  bool   `json:"already_handled"`
	SignatureFailed bool   `json:"-"`
	PaymentID       string `json:"payment_id"`
	SubscriptionID  string `json:"subscription_id"`
	LedgerTxnID     string `json:"ledger_txn_id"`
}

// HandlePaymentWebhook 处理支付成功回调。
//
// PAY-003 验收「同一回调重复 100 次只产生一次业务结果」的实现路径：
// 第一步就往 payment_events 插入 (provider_id, provider_event_id)。
// 该组合有唯一约束，第 2..100 次直接撞约束返回 AlreadyHandled，
// 后面的记账与订阅激活根本不会执行。判重发生在数据库，而非应用的 if。
/* legacy pre-reservation settlement retained temporarily for review context
/*
func (s *Service) handlePaymentWebhookLegacy(ctx context.Context, tenantID string, in PaymentWebhookInput) (*PaymentWebhookOutput, error) {
	var out PaymentWebhookOutput
	scope := db.Scope{TenantID: tenantID}

	err := s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		var providerID string
		err := tx.QueryRow(ctx,
			`SELECT id FROM payment_providers WHERE tenant_id = $1 AND code = $2`,
			tenantID, in.ProviderCode).Scan(&providerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "未知的支付渠道")
		}
		if err != nil {
			return err
		}

		// --- 幂等闸门 ---
		// 同样必须走 ON CONFLICT DO NOTHING：若让唯一约束直接抛错，
		// 事务会变成 aborted，连提交都会失败，重复回调就会返回 500 而不是
		// 「已处理」。用零行返回表达冲突，事务始终健康。
		var eventID string
		err = tx.QueryRow(ctx, `
			INSERT INTO payment_events
				(tenant_id, provider_id, provider_event_id, event_type,
				 provider_payment_id, raw_payload, signature_verified)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (provider_id, provider_event_id) DO NOTHING
			RETURNING id`,
			tenantID, providerID, in.ProviderEventID, in.EventType,
			nullStr(in.ProviderPaymentID), in.RawPayload, in.SignatureVerified,
		).Scan(&eventID)

		if errors.Is(err, pgx.ErrNoRows) {
			out.AlreadyHandled = true
			return nil
		}
		if err != nil {
			return err
		}

		if !in.SignatureVerified {
			_, _ = tx.Exec(ctx, `
				UPDATE payment_events
				   SET processing_status = 'ignored',
				       processing_error = '签名校验未通过',
				       processed_at = now()
				 WHERE id = $1`, eventID)
			return httpx.New(httpx.CodeUnauthorized, "回调签名校验失败")
		}

		if in.EventType != "payment.succeeded" {
			_, _ = tx.Exec(ctx,
				`UPDATE payment_events SET processing_status = 'ignored', processed_at = now()
				  WHERE id = $1`, eventID)
			out.Processed = true
			return nil
		}

		// --- 取订单并加锁 ---
		var (
			orderID        string
			userID         string
			status         string
			currency       string
			payable        int64
			orderKind      string
			balanceApplied int64
			totalAmount    int64
		)
		// 按内部 UUID 或对外短单号定位。两条路径都在事务内取行锁，
		// 保证并发回调只有一个能推进订单状态。
		if in.OrderID != "" {
			err = tx.QueryRow(ctx, `
				SELECT id, user_id, status, currency, payable_amount, balance_applied,
				       total_amount, kind
				  FROM orders WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
				tenantID, in.OrderID).Scan(&orderID, &userID, &status, &currency,
				&payable, &balanceApplied, &totalAmount, &orderKind)
		} else if in.OrderNo != "" {
			err = tx.QueryRow(ctx, `
				SELECT id, user_id, status, currency, payable_amount, balance_applied,
				       total_amount, kind
				  FROM orders WHERE tenant_id = $1 AND order_no = $2 FOR UPDATE`,
				tenantID, in.OrderNo).Scan(&orderID, &userID, &status, &currency,
				&payable, &balanceApplied, &totalAmount, &orderKind)
		} else {
			return httpx.New(httpx.CodeBadRequest, "回调未携带订单标识")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "订单不存在")
		}
		if err != nil {
			return err
		}
		in.OrderID = orderID

		// PAY-001：订单已完成时不重复处理，但事件仍算已消费
		if status == "paid" || status == "fulfilled" {
			_, _ = tx.Exec(ctx,
				`UPDATE payment_events SET processing_status = 'ignored', processed_at = now()
				  WHERE id = $1`, eventID)
			out.AlreadyHandled = true
			return nil
		}
		if in.Currency != currency {
			return httpx.New(httpx.CodeConflict, "回调币种与订单不一致")
		}
		if in.Amount != payable {
			return httpx.New(httpx.CodeConflict,
				fmt.Sprintf("回调金额与应付金额不符（应付 %d，收到 %d）", payable, in.Amount))
		}

		// --- 终结支付意图 ---
		// 必须在插 payments 之前做：payment_intents 上有「一个订单只允许一个
		// 未终结意图」的部分唯一索引，留着 requires_action 会让这张单永远
		// 卡在「有在途支付」的状态，用户换渠道重付时被误判为重复。
		var intentID *string
		if err := tx.QueryRow(ctx, `
			UPDATE payment_intents
			   SET status = 'succeeded',
			       provider_ref = coalesce(provider_ref, $3)
			 WHERE tenant_id = $1 AND order_id = $2
			   AND status IN ('created', 'requires_action', 'processing')
			RETURNING id`,
			tenantID, in.OrderID, nullStr(in.ProviderPaymentID),
		).Scan(&intentID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// 查询补偿路径（PAY-009）可能没有任何在途意图，属正常情况，不报错。

		// --- 记录支付 ---
		var paymentID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO payments
				(tenant_id, order_id, provider_id, provider_payment_id,
				 payment_intent_id, currency, amount, fee_amount, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'succeeded')
			RETURNING id`,
			tenantID, in.OrderID, providerID, in.ProviderPaymentID,
			intentID, currency, in.Amount, in.FeeAmount).Scan(&paymentID); err != nil {
			return err
		}
		out.PaymentID = paymentID

		// --- 记账（PAY-005）---
		// 充值不是收入，而是平台对用户的负债。它必须在这里直接生成唯一一笔
		// 充值分录，不能先按商品订单确认收入、再在履约阶段重复确认渠道现金。
		var txnID string
		if orderKind == "topup" {
			txnID, err = s.postTopupPaid(ctx, tx, tenantID, topupPaidPosting{
				OrderID:      in.OrderID,
				UserID:       userID,
				Currency:     currency,
				Amount:       in.Amount,
				FeeAmount:    in.FeeAmount,
				ProviderCode: in.ProviderCode,
			})
		} else {
			txnID, err = s.postOrderPaid(ctx, tx, tenantID, orderPaidPosting{
				OrderID:        in.OrderID,
				UserID:         userID,
				Currency:       currency,
				ChannelAmount:  in.Amount,
				BalanceApplied: balanceApplied,
				FeeAmount:      in.FeeAmount,
				TotalAmount:    totalAmount,
				ProviderCode:   in.ProviderCode,
			})
		}
		if err != nil {
			return err
		}
		out.LedgerTxnID = txnID

		// --- 订单状态推进 ---
		if _, err := tx.Exec(ctx, `
			UPDATE orders
			   SET status = 'paid', paid_amount = $3, paid_at = now()
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, in.OrderID, totalAmount); err != nil {
			return err
		}

		// --- 履约 ---
		//
		// 充值单和购买单走的是同一条支付链路，区别只在这一步：
		// 买套餐需要履约；充值的余额增加已经包含在上面的唯一一笔支付分录中。
		// 拿充值单去跑 fulfillOrder 会因为找不到订单行上的套餐快照而失败。
		switch orderKind {
		case "topup":
			// 无额外履约；记账与余额增加已原子完成。
		case "renewal":
			// 续费落在已有订阅上：延长周期、重置周期配额，不新建订阅。
			// 走 fulfillOrder 会凭空多出第二条订阅，用户会看到两个订阅链接
			subID, err := s.fulfillRenewal(ctx, tx, tenantID, in.OrderID, userID)
			if err != nil {
				return err
			}
			out.SubscriptionID = subID
		default:
			subID, err := s.fulfillOrder(ctx, tx, tenantID, in.OrderID, userID)
			if err != nil {
				return err
			}
			out.SubscriptionID = subID
		}

		if err := plugin.EmitOrderPaid(ctx, tx, tenantID, in.OrderID, userID,
			orderKind, currency, totalAmount, out.SubscriptionID); err != nil {
			return err
		}
		if out.SubscriptionID != "" {
			if err := plugin.EmitSubscriptionProvisioned(ctx, tx, tenantID,
				out.SubscriptionID, userID, in.OrderID, orderKind); err != nil {
				return err
			}
		}

		// 充值不产生佣金：那只是把钱换个地方放，还没有产生任何消费。
		// 给充值计提等于同一笔钱在充值和下单时被算两次分成。
		if orderKind != "topup" {
			// 分销佣金按订单成交额计提，不是按渠道实收。
			// 用余额支付的部分同样是真金白银，只是先前已经进过账 ——
			// 按渠道实收算会让「用余额买」的推荐一分钱佣金都拿不到。
			if err := s.accrueCommission(ctx, tx, tenantID, in.OrderID, userID,
				currency, totalAmount); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE payment_events
			   SET processing_status = 'processed', processed_at = now()
			 WHERE id = $1`, eventID); err != nil {
			return err
		}

		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system",
			Action:    "payment.succeeded", ResourceType: "order", ResourceID: &in.OrderID,
			AfterDigest: map[string]any{
				"payment_id": paymentID, "amount": in.Amount,
				"currency": currency, "ledger_txn": txnID,
				// 充值单没有订阅，这里会是空串 —— 比塞一个假 ID 诚实
				"subscription_id": out.SubscriptionID, "order_kind": orderKind,
			},
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		}); err != nil {
			return err
		}

		out.Processed = true
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

*/

// HandlePaymentWebhook captures a successful payment and the order's complete
// held reservation graph in one transaction. The legacy implementation remains
// above only as historical source while this path is exercised by callers.
func (s *Service) HandlePaymentWebhook(ctx context.Context, tenantID string, in PaymentWebhookInput) (*PaymentWebhookOutput, error) {
	var out PaymentWebhookOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		out = PaymentWebhookOutput{}
		return s.settlePaymentTx(ctx, tx, tenantID, in, &out)
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	// 事务已提交，这时通知才对应一个真实存在的变更。
	// 只有确实产生了订阅才值得惊动节点——纯余额充值之类不涉及可服务用户。
	if out.SubscriptionID != "" {
		s.notifyUsersChanged(ctx, tenantID)
	}
	return &out, nil
}

// settlePaymentTx 是一笔收款的完整结算，在调用方的事务里执行：渠道回调与
// 标记已支付经 HandlePaymentWebhook 各开一个事务调用它；人工开单「线下已收款」
// 在建单的同一个事务里调用它，建单、入账、履约、幂等记录要么一起生效，
// 要么一起回滚（manual_order.go）。
func (s *Service) settlePaymentTx(ctx context.Context, tx pgx.Tx, tenantID string,
	in PaymentWebhookInput, out *PaymentWebhookOutput) error {

	if in.ProviderEventID == "" || in.ProviderPaymentID == "" {
		return httpx.New(httpx.CodeBadRequest, "payment event and payment identifiers are required")
	}
	if in.Amount <= 0 || in.FeeAmount < 0 || in.FeeAmount > in.Amount {
		return httpx.New(httpx.CodeBadRequest, "payment amount or fee is invalid")
	}

	var providerID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM payment_providers
		 WHERE tenant_id=$1 AND code=$2`, tenantID, in.ProviderCode).Scan(&providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.New(httpx.CodeNotFound, "unknown payment provider")
	}
	if err != nil {
		return err
	}

	forceConstraints := func() error {
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	}

	// Unique provider event gate is deliberately the first mutable operation.
	var eventID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payment_events
			(tenant_id,provider_id,provider_event_id,event_type,
			 provider_payment_id,raw_payload,signature_verified)
		VALUES ($1,$2::uuid,$3,$4,$5,$6,$7)
		ON CONFLICT (provider_id,provider_event_id) DO NOTHING
		RETURNING id::text`, tenantID, providerID, in.ProviderEventID,
		in.EventType, in.ProviderPaymentID, in.RawPayload, in.SignatureVerified).
		Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := forceConstraints(); err != nil {
			return err
		}
		out.AlreadyHandled = true
		return nil
	}
	if err != nil {
		return err
	}

	if !in.SignatureVerified {
		// 验签失败的事件必须留库可审计：把这条 payment_event 以
		// ignored 状态提交（证据保全），然后返回 nil 让事务真正
		// 提交——不能返回 error，否则整个事务回滚，伪造回调的
		// 原始报文和验签失败证据一条都留不下来。
		if _, err := tx.Exec(ctx, `
			UPDATE payment_events
			   SET processing_status='ignored', processed_at=now()
			 WHERE id=$1::uuid AND processing_status='pending'`, eventID); err != nil {
			return err
		}
		if err := forceConstraints(); err != nil {
			return err
		}
		out.Processed = true
		out.SignatureFailed = true
		return nil
	}
	if in.EventType != "payment.succeeded" {
		tag, err := tx.Exec(ctx, `
			UPDATE payment_events
			   SET processing_status='ignored', processed_at=now()
			 WHERE id=$1::uuid AND processing_status='pending'`, eventID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("ignored payment event transition lost")
		}
		if err := forceConstraints(); err != nil {
			return err
		}
		out.Processed = true
		return nil
	}

	var (
		orderID, userID, status, currency, orderKind string
		businessRequestID                            string
		subtotalAmount, discountAmount, taxAmount    int64
		payable, balanceApplied, totalAmount         int64
		prorationCredit                              int64
		couponID, idempotencyKeyID                   *string
	)
	query := `
		SELECT id::text,user_id::text,status,currency::text,subtotal_amount,
		       discount_amount,tax_amount,payable_amount,balance_applied,total_amount,
		       kind,business_request_id::text,coupon_id::text,idempotency_key_id::text,
		       proration_credit_amount
		  FROM orders WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`
	identifier := in.OrderID
	if identifier == "" {
		query = `
			SELECT id::text,user_id::text,status,currency::text,subtotal_amount,
			       discount_amount,tax_amount,payable_amount,balance_applied,total_amount,
			       kind,business_request_id::text,coupon_id::text,idempotency_key_id::text,
			       proration_credit_amount
			  FROM orders WHERE tenant_id=$1 AND order_no=$2 FOR UPDATE`
		identifier = in.OrderNo
	}
	if identifier == "" {
		return httpx.New(httpx.CodeBadRequest, "payment callback has no order identifier")
	}
	err = tx.QueryRow(ctx, query, tenantID, identifier).Scan(
		&orderID, &userID, &status, &currency, &subtotalAmount,
		&discountAmount, &taxAmount, &payable, &balanceApplied, &totalAmount,
		&orderKind, &businessRequestID, &couponID, &idempotencyKeyID, &prorationCredit)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.New(httpx.CodeNotFound, "order not found")
	}
	if err != nil {
		return err
	}
	in.OrderID = orderID
	if subscriptionBoundOrderKind(orderKind) && (idempotencyKeyID == nil ||
		*idempotencyKeyID != businessRequestID) {
		return errors.New("subscription-bound order is missing its exact idempotency linkage")
	}

	// Distinct provider events for the same provider payment must serialize
	// even before the unique payment row exists. The order row remains the
	// first lock; this key closes only the absent-provider-payment gap.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		providerID+":"+in.ProviderPaymentID); err != nil {
		return err
	}
	var recordedOrderID string
	err = tx.QueryRow(ctx, `
		SELECT order_id::text FROM payments
		 WHERE tenant_id=$1 AND provider_id=$2::uuid AND provider_payment_id=$3`,
		tenantID, providerID, in.ProviderPaymentID).Scan(&recordedOrderID)
	if err == nil {
		if recordedOrderID != orderID {
			return httpx.New(httpx.CodeConflict,
				"凭证号已用于其他订单")
		}
		if status != "paid" && status != "fulfilled" &&
			status != "cancelled" && status != "expired" {
			return errors.New("provider payment exists before order reached a terminal state")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if status == "paid" || status == "fulfilled" {
		quarantined, err := s.quarantineUnexpectedPayment(ctx, tx,
			tenantID, eventID, providerID, orderID, userID, status,
			in.ProviderCode, "excess_capture", in)
		if err != nil {
			return err
		}
		*out = *quarantined
		return nil
	}
	if status == "cancelled" || status == "expired" {
		quarantined, err := s.quarantineUnexpectedPayment(ctx, tx,
			tenantID, eventID, providerID, orderID, userID, status,
			in.ProviderCode, "released_order", in)
		if err != nil {
			return err
		}
		*out = *quarantined
		return nil
	}
	if status != "pending_payment" && status != "processing" {
		return httpx.New(httpx.CodeConflict, "order is not payable")
	}
	if in.Currency != currency || in.Amount != payable {
		return httpx.New(httpx.CodeConflict, "payment currency or amount does not match the order")
	}

	// Renewal and plan-change settlement must acquire the existing
	// subscription before any reservation child or ledger-account lock.
	// Their creation uses the same subscription -> coupon -> ledger order,
	// closing the cross-flow deadlock cycle without weakening new-order or
	// top-up settlement.
	var renewalSubscriptionID string
	if subscriptionBoundOrderKind(orderKind) {
		renewalSubscriptionID, err = lockOrderSubscriptionForSettlement(
			ctx, tx, tenantID, orderID, userID,
		)
		if err != nil {
			return err
		}
	}

	// Active payment intents are locked immediately after the order.
	var activeIntentIDs []string
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM payment_intents
		 WHERE tenant_id=$1 AND order_id=$2::uuid
		   AND status IN ('created','requires_action','processing')
		 ORDER BY id FOR UPDATE`, tenantID, orderID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		activeIntentIDs = append(activeIntentIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(activeIntentIDs) > 1 {
		return errors.New("order has more than one active payment intent")
	}

	locked, err := lockOrderReservationGraph(ctx, tx, reservationLockRequest{
		TenantID: tenantID, OrderID: orderID, UserID: userID, Kind: orderKind,
		Currency: currency, CouponID: couponID, SubtotalAmount: subtotalAmount,
		DiscountAmount: discountAmount, TaxAmount: taxAmount, TotalAmount: totalAmount,
		PayableAmount: payable, BalanceAmount: balanceApplied,
		ProrationCredit: prorationCredit,
	})
	if err != nil {
		return err
	}

	accountSpecs := []ledgerAccountSpec{{
		Key: "channel", AccountType: AccountChannelCash,
		Currency: currency, OwnerRef: in.ProviderCode,
	}}
	var existingAccountIDs []string
	var commissionReferrerID *string
	if orderKind == "topup" {
		accountSpecs = append(accountSpecs, ledgerAccountSpec{
			Key: "available", AccountType: AccountUserBalance,
			Currency: currency, UserID: &userID,
		})
	} else {
		accountSpecs = append(accountSpecs, ledgerAccountSpec{
			Key: "revenue", AccountType: AccountPlatformRevenue,
			Currency: currency, OwnerRef: "main",
		})
		if locked.Balance != nil {
			existingAccountIDs = append(existingAccountIDs,
				locked.Balance.AvailableAccountID, locked.Balance.HoldAccountID)
		}
		var referrerID string
		err := tx.QueryRow(ctx, `
			SELECT referrer_user_id::text FROM referrals
			 WHERE tenant_id=$1 AND referee_user_id=$2::uuid`,
			tenantID, userID).Scan(&referrerID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			commissionReferrerID = &referrerID
			accountSpecs = append(accountSpecs, ledgerAccountSpec{
				Key: "commission_pending", AccountType: AccountUserCommissionPending,
				Currency: currency, UserID: commissionReferrerID,
			})
		}
	}
	if in.FeeAmount > 0 {
		accountSpecs = append(accountSpecs, ledgerAccountSpec{
			Key: "fee", AccountType: AccountPlatformFeeExpense,
			Currency: currency, OwnerRef: in.ProviderCode,
		})
	}
	if _, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID,
		accountSpecs, existingAccountIDs...); err != nil {
		return err
	}

	var intentID *string
	if len(activeIntentIDs) == 1 {
		id := activeIntentIDs[0]
		tag, err := tx.Exec(ctx, `
			UPDATE payment_intents
			   SET status='succeeded',provider_ref=coalesce(provider_ref,$3)
			 WHERE tenant_id=$1 AND id=$2::uuid
			   AND status IN ('created','requires_action','processing')`,
			tenantID, id, in.ProviderPaymentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("active payment intent transition lost")
		}
		intentID = &id
	}

	var paymentID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payments
			(tenant_id,order_id,provider_id,provider_payment_id,
			 payment_intent_id,currency,amount,fee_amount,status)
		VALUES ($1,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,$8,'succeeded')
		RETURNING id::text`, tenantID, orderID, providerID,
		in.ProviderPaymentID, intentID, currency, in.Amount, in.FeeAmount).
		Scan(&paymentID)
	if err != nil {
		return err
	}
	out.PaymentID = paymentID

	var txnID string
	if orderKind == "topup" {
		txnID, err = s.postTopupPaid(ctx, tx, tenantID, topupPaidPosting{
			OrderID: orderID, UserID: userID, Currency: currency,
			Amount: in.Amount, FeeAmount: in.FeeAmount,
			ProviderCode: in.ProviderCode,
		})
	} else {
		holdAccountID := ""
		if locked.Balance != nil {
			holdAccountID = locked.Balance.HoldAccountID
		}
		txnID, err = s.postOrderPaid(ctx, tx, tenantID, orderPaidPosting{
			OrderID: orderID, UserID: userID, Currency: currency,
			ChannelAmount: in.Amount, BalanceApplied: balanceApplied,
			FeeAmount: in.FeeAmount, TotalAmount: totalAmount,
			ProviderCode: in.ProviderCode, HoldAccountID: holdAccountID,
		})
	}
	if err != nil {
		return err
	}
	out.LedgerTxnID = txnID
	// Commission accounts joined the same UUID-sorted lock set above. Keep
	// every settlement ledger write before reservation/order transitions.
	if orderKind != "topup" && commissionReferrerID != nil {
		if err := s.accrueCommission(ctx, tx, tenantID, orderID, userID,
			currency, totalAmount); err != nil {
			return err
		}
	}

	if err := captureLockedReservation(ctx, tx, tenantID, orderID, userID,
		businessRequestID, locked, txnID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders
		   SET status='paid',paid_amount=$3,paid_at=now()
		 WHERE tenant_id=$1 AND id=$2::uuid
		   AND status IN ('pending_payment','processing')`,
		tenantID, orderID, totalAmount)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("order paid transition lost")
	}

	switch orderKind {
	case "topup":
		// 余额入账已经在上面的结算分录里做完了，这里只把订单收尾。
		//
		// 早先这个分支是空的，充值单于是永远停在 status='paid'、
		// fulfilled_at IS NULL —— 钱到账了，订单却看起来像卡住了。
		// 两个后果：后台订单列表里充值单永远显示「已支付」而不是
		// 「已履约」，看不出到底完没完成；更麻烦的是「付了钱没履约」
		// 是排查卡单的标准查询，而每一张充值单都会命中它，真有一张
		// 入账失败卡在那里，会淹没在这堆假阳性里没人发现。
		//
		// 充值的履约就是余额落账那一刻，没有别的后续动作，所以在
		// 同一个事务里直接置为 fulfilled。
		if _, err := tx.Exec(ctx, `
			UPDATE orders
			   SET status='fulfilled', fulfilled_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND status='paid'`,
			tenantID, orderID); err != nil {
			return err
		}
	case "renewal":
		subscriptionID, err := s.fulfillRenewalLocked(ctx, tx, tenantID,
			orderID, userID, renewalSubscriptionID)
		if err != nil {
			return err
		}
		out.SubscriptionID = subscriptionID
	case "new":
		subscriptionID, err := s.fulfillOrder(ctx, tx, tenantID, orderID, userID)
		if err != nil {
			return err
		}
		out.SubscriptionID = subscriptionID
	case "addon":
		// 流量包：履约就是按购买时的容量快照发一笔用户级余额，没有订阅要开
		if _, err := fulfillTrafficPackOrder(ctx, tx, tenantID, orderID, userID); err != nil {
			return err
		}
	case "upgrade":
		// 要外部付款的变更单一定是补差价（total > 0），不会有退余额，
		// 退余额只发生在 plan_change.go 的零元单捕获里。
		subscriptionID, err := s.fulfillPlanChangeLocked(ctx, tx, tenantID,
			orderID, userID, renewalSubscriptionID)
		if err != nil {
			return err
		}
		out.SubscriptionID = subscriptionID
	default:
		return fmt.Errorf("unsupported paid order kind %q", orderKind)
	}

	// dedupe 用订单号：支付渠道会重投回调，同一笔订单只该通知插件一次。
	if err := plugin.EmitOrderPaid(ctx, tx, tenantID, orderID, userID,
		orderKind, currency, totalAmount, out.SubscriptionID); err != nil {
		return err
	}
	if out.SubscriptionID != "" {
		if err := plugin.EmitSubscriptionProvisioned(ctx, tx, tenantID,
			out.SubscriptionID, userID, orderID, orderKind); err != nil {
			return err
		}
	}

	tag, err = tx.Exec(ctx, `
		UPDATE payment_events
		   SET processing_status='processed',processed_at=now()
		 WHERE id=$1::uuid AND processing_status='pending'`, eventID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("payment event processed transition lost")
	}
	if err := audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "system", Action: "payment.succeeded",
		ResourceType: "order", ResourceID: &orderID,
		AfterDigest: map[string]any{
			"payment_id": paymentID, "amount": in.Amount, "currency": currency,
			"ledger_txn": txnID, "subscription_id": out.SubscriptionID,
			"order_kind": orderKind,
		},
		APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
	}); err != nil {
		return err
	}
	if err := forceConstraints(); err != nil {
		return err
	}
	out.Processed = true
	return nil
}

type orderPaidPosting struct {
	OrderID        string
	UserID         string
	Currency       string
	ChannelAmount  int64 // 外部渠道实收
	BalanceApplied int64 // 余额抵扣部分
	FeeAmount      int64 // 渠道手续费
	TotalAmount    int64
	ProviderCode   string
	HoldAccountID  string
}

// postOrderPaid 生成订单支付的完整分录。
//
//	借 渠道资金        = 实收 − 手续费
//	借 平台手续费支出   = 手续费
//	借 用户余额        = 余额抵扣（负债减少）
//	贷 平台收入        = 订单总额
//
// 四条加起来必然配平：(实收−手续费) + 手续费 + 余额抵扣 = 实收 + 余额抵扣 = 总额。
func (s *Service) postOrderPaid(ctx context.Context, tx pgx.Tx, tenantID string, p orderPaidPosting) (string, error) {
	channelAcct, err := EnsureAccount(ctx, tx, tenantID, AccountChannelCash,
		p.Currency, nil, p.ProviderCode)
	if err != nil {
		return "", err
	}
	revenueAcct, err := EnsureAccount(ctx, tx, tenantID, AccountPlatformRevenue,
		p.Currency, nil, "main")
	if err != nil {
		return "", err
	}

	entries := []Entry{}

	net := p.ChannelAmount - p.FeeAmount
	if net > 0 {
		entries = append(entries, Entry{
			AccountID: channelAcct, Direction: Debit, Amount: net,
			Description: "渠道净收款",
		})
	}
	if p.FeeAmount > 0 {
		feeAcct, err := EnsureAccount(ctx, tx, tenantID, AccountPlatformFeeExpense,
			p.Currency, nil, p.ProviderCode)
		if err != nil {
			return "", err
		}
		entries = append(entries, Entry{
			AccountID: feeAcct, Direction: Debit, Amount: p.FeeAmount,
			Description: "渠道手续费",
		})
	}
	if p.BalanceApplied > 0 {
		if p.HoldAccountID == "" {
			return "", errors.New("mixed payment is missing its locked balance-hold account")
		}
		entries = append(entries, Entry{
			AccountID: p.HoldAccountID, Direction: Debit, Amount: p.BalanceApplied,
			Description: "capture order balance hold",
		})
	} else if p.HoldAccountID != "" {
		return "", errors.New("order without balance applied supplied a hold account")
	}

	entries = append(entries, Entry{
		AccountID: revenueAcct, Direction: Credit, Amount: p.TotalAmount,
		Description: "订单收入",
	})

	orderID := p.OrderID
	return Post(ctx, tx, tenantID, Posting{
		Kind: "order_paid", Currency: p.Currency,
		SourceType: "order", SourceID: &orderID,
		ActorKind: "system", Entries: entries,
	})
}

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

// fulfillOrder 依据订单行的快照创建并激活订阅，同时初始化配额与订阅凭据。
func (s *Service) fulfillOrder(ctx context.Context, tx pgx.Tx, tenantID, orderID, userID string) (string, error) {
	var spec provisionSpec
	err := tx.QueryRow(ctx, `
		SELECT plan_id, plan_version_id, price_id, currency, unit_amount,
		       snapshot_interval, snapshot_interval_count
		  FROM order_items
		 WHERE tenant_id = $1 AND order_id = $2
		 ORDER BY created_at LIMIT 1`,
		tenantID, orderID).Scan(&spec.PlanID, &spec.PlanVersionID, &spec.PriceID,
		&spec.Currency, &spec.UnitAmount, &spec.Interval, &spec.IntervalCount)
	if err != nil {
		return "", fmt.Errorf("读取订单行: %w", err)
	}
	spec.ActorKind = "payment"
	spec.OrderID = &orderID

	subID, err := s.provisionSubscription(ctx, tx, tenantID, userID, spec)
	if err != nil {
		return "", err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now(), subscription_id = $3
		 WHERE tenant_id = $1 AND id = $2 AND status = 'paid'`,
		tenantID, orderID, subID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("order fulfilment transition lost")
	}

	return subID, nil
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

//------------------------------------------------------------------------------
// 辅助
//------------------------------------------------------------------------------

// addInterval 按计费周期推进时间。
//
// 用 AddDate 而非固定天数：AddDate 处理月末与闰年的规则是
// 「1月31日 + 1月 = 3月3日（平年）」，这与多数支付平台一致。
// SUB-010 要求的月末/闰年测试即针对此行为。
func addInterval(from time.Time, interval string, count int) time.Time {
	if count <= 0 {
		count = 1
	}
	switch interval {
	case "day":
		return from.AddDate(0, 0, count)
	case "week":
		return from.AddDate(0, 0, 7*count)
	case "month":
		return from.AddDate(0, count, 0)
	case "quarter":
		return from.AddDate(0, 3*count, 0)
	case "year":
		return from.AddDate(count, 0, 0)
	case "one_time":
		// 一次性商品没有周期，给一个远期哨兵值
		return from.AddDate(100, 0, 0)
	default:
		return from.AddDate(0, count, 0)
	}
}

func newOrderNo() (string, error) {
	suffix, err := crypto.NewToken(6)
	if err != nil {
		return "", err
	}
	return "AO" + time.Now().UTC().Format("20060102") + "-" + suffix[:8], nil
}

func jsonAgg(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]byte, error) {
	var out []byte
	if err := tx.QueryRow(ctx, query, args...).Scan(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// couponID 把可选的券转成可空的参数值。
func couponID(c *couponMatch) any {
	if c == nil {
		return nil
	}
	return c.ID
}

// orderKindFor 决定订单类型。
//
// 人工赠送单也是 'new'，不另起一个 kind。理由：整套预留图与数据库不变量
// 都是围绕 new / renewal / topup 三种形状写的 —— 新增一个 kind 意味着要把
// 库存预留、限购预留、订单项、事件链这些约束逐个教会它，漏一个就是运行时
// 500，而且是那种只在特定路径才暴露的 500。
//
// 而赠送单的形状和普通新购**完全一致**：同一个套餐、同一份订单项快照、
// 同样占库存、同样受限购。区别只在"谁开的"和"为什么开"，
// 这两件事由 created_by 与 manual_reason 记录，本来就独立于 kind。
// 要捞出所有人工单，条件是 created_by IS NOT NULL —— 比 kind='manual'
// 更贴近事实：它说的是"这单是管理员开的"。
func orderKindFor(in CreateOrderInput) string {
	return "new"
}

// nullIfEmpty 让空串落库为 NULL。
// orders 上有 CHECK：kind='manual' 时 manual_reason 必须非空且不短于 5 字。
// 普通订单必须把它留成 NULL 而不是空串，否则约束虽然过得去，
// 数据里却多出一批"理由是空字符串"的行，日后查人工单会把它们一起捞出来。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
