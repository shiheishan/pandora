package billing

// 续费与周期滚动。
//
// 在这之前「续费」按钮只是跳到套餐页重新下单，结果是同一个人被开出第二条订阅：
// 两条各有各的订阅链接、各自的配额、各自的设备数。用户看到两个二维码，
// 不知道该用哪个；管理员看到两条记录，不知道该退哪条。
//
// 真正的续费是在原订阅上做加法：
//
//	周期     从当前周期末往后延，而不是从今天算 —— 提前几天续费不该损失那几天
//	配额     按 quota_definitions 的 period 决定：cycle 重置，total 保留
//	凭据     不动。换订阅链接等于逼所有设备重新导入一次
//	流量包   挂在用户身上（traffic_pack_grants），续费不碰它，余量原样保留（D-E-1）

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/idempotencybind"
)

var (
	ErrSubNotRenewable = httpx.New(httpx.CodeConflict, "这条订阅当前不能续费")
	ErrRenewPriceGone  = httpx.New(httpx.CodeConflict, "所选价格已下架，请重新选择")
)

const RenewalIdempotencyScope = "subscription_renewal_create"

type CreateRenewalInput struct {
	UserID         string
	SubscriptionID string
	// PriceID 可选。不填就沿用原订阅的价格，填了就是换周期续费
	// （比如月付转年付）—— 这时按新价格算钱、按新周期延长。
	PriceID    string
	UseBalance int64
	CouponCode string
	Claim      middleware.IdempotencyClaim

	// --- 以下仅供后台人工开单（manual_order.go）用，门户续费一律留空 ---
	//
	// 用户已有同套餐订阅时，人工开单不再新开订阅、换链接，而是在原订阅上开一张
	// 续费单（规则 3）。ManualActor 非空即人工续费：幂等声明属于管理员、scope 是
	// 人工开单的 order_create；ManualGrant 全额减免当场履约；Offline 建单后在同一
	// 事务里按线下渠道结清。审计 order.manual_created 与订单写在同一个事务里。
	ManualGrant  bool
	ManualReason string
	ManualActor  string
	Offline      *OfflineReceipt
	// ManualSettlement 只进审计摘要（grant / pending / offline）
	ManualSettlement string
}

// CreateRenewal 为一条已有订阅建续费订单。
func (s *Service) CreateRenewal(ctx context.Context, tenantID string,
	in CreateRenewalInput) (*CreateOrderOutput, error) {
	// 人工续费的幂等声明属于发起开单的管理员（与 CreateOrder 的人工单同理）
	claimActor, claimScope := in.UserID, RenewalIdempotencyScope
	if in.ManualActor != "" {
		claimActor, claimScope = in.ManualActor, CheckoutIdempotencyScope
	}
	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, claimActor, claimScope,
	); err != nil {
		return nil, fmt.Errorf("create renewal: %w", err)
	}

	var out CreateOrderOutput
	err := s.pool.InTxSerializable(ctx, dbScope(tenantID, claimActor), func(tx pgx.Tx) error {
		var (
			planID, planVersionID string
			curPriceID            *string
			status                string
			periodEnd             *time.Time
			renewalClosed         bool
		)
		if err := tx.QueryRow(ctx, `
			SELECT plan_id::text, plan_version_id::text, price_id::text,
			       status, current_period_end, renewal_closed_at IS NOT NULL
			  FROM subscriptions
			 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
			 FOR UPDATE`,
			tenantID, in.SubscriptionID, in.UserID).Scan(
			&planID, &planVersionID, &curPriceID, &status, &periodEnd, &renewalClosed); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		var allowRenewal bool
		if err := tx.QueryRow(ctx, `SELECT allow_renewal FROM plans
			WHERE tenant_id=$1 AND id=$2::uuid FOR SHARE`, tenantID, planID).Scan(&allowRenewal); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if !allowRenewal {
			return httpx.New(httpx.CodeConflict, "该套餐当前不允许续费")
		}
		var userGroupID *string
		if err := tx.QueryRow(ctx, `SELECT user_group_id FROM users
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, in.UserID).Scan(&userGroupID); err != nil {
			if err == pgx.ErrNoRows {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		// 已取消、或过期超过原地续费窗口（30 天）的订阅不给续 —— 那种情况应该
		// 走重新购买，因为权益版本、价格、节点分组可能都已经变了。窗口内的已过期
		// 订阅照常续，沿用原套餐版本，链接不变（规则 4）
		if !subscriptionAcceptsPaidChange(status, renewalClosed) {
			if status == "expired" {
				return ErrRenewalWindowClosed
			}
			return ErrSubNotRenewable
		}
		if err := ensureNoOpenSubscriptionOrder(ctx, tx, tenantID, in.SubscriptionID); err != nil {
			return err
		}

		priceID := in.PriceID
		if priceID == "" {
			if curPriceID == nil {
				return httpx.New(httpx.CodeConflict, "这条订阅没有关联价格，无法自动续费")
			}
			priceID = *curPriceID
		}

		var (
			currency      string
			unitAmount    int64
			interval      string
			intervalCount int16
			priceStatus   string
			priceGroupID  *string
			validFrom     *time.Time
			validUntil    *time.Time
		)
		if err := tx.QueryRow(ctx, `
			SELECT pr.currency::text, pr.unit_amount, pr.billing_interval,
			       pr.interval_count, pr.status, pr.user_group_id,
			       pr.valid_from, pr.valid_until
			  FROM prices pr
			  JOIN plans pl ON pl.product_id = pr.product_id AND pl.tenant_id = pr.tenant_id
			 WHERE pr.tenant_id = $1 AND pr.id = $2::uuid AND pl.id = $3::uuid
			   AND pr.currency IN ('CNY','USD')
			 FOR UPDATE OF pr`,
			tenantID, priceID, planID).Scan(&currency, &unitAmount,
			&interval, &intervalCount, &priceStatus, &priceGroupID,
			&validFrom, &validUntil); err != nil {
			if err == pgx.ErrNoRows {
				return ErrRenewPriceGone
			}
			return err
		}
		if priceStatus != "active" {
			return ErrRenewPriceGone
		}

		if priceGroupID != nil && (userGroupID == nil || *priceGroupID != *userGroupID) {
			return ErrRenewPriceGone
		}
		now := time.Now().UTC()
		if !catalogPriceCurrentlyValid(validFrom, validUntil, now) {
			return ErrRenewPriceGone
		}

		subtotal := unitAmount

		// 续费同样能用优惠券。券的适用范围按套餐判断，与新购一致
		coupon, err := applyCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode,
			planID, currency, subtotal)
		if err != nil {
			return err
		}
		var discount int64
		var renewalCouponID *string
		if coupon != nil {
			discount = coupon.Discount
			id := coupon.ID
			renewalCouponID = &id
		}
		// 人工续费赠送：全额减免，payable 归零后走下方零元单捕获直接履约（同新购人工单）
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
		var availableAccountID, holdAccountID, revenueAccountID string
		if balanceApplied > 0 {
			specs := []ledgerAccountSpec{
				{Key: "available", AccountType: AccountUserBalance,
					Currency: currency, UserID: &in.UserID},
				{Key: "hold", AccountType: AccountUserBalanceHold,
					Currency: currency, UserID: &in.UserID},
			}
			if payable == 0 {
				specs = append(specs, ledgerAccountSpec{
					Key: "revenue", AccountType: AccountPlatformRevenue,
					Currency: currency, OwnerRef: "main",
				})
			}
			accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, specs)
			if err != nil {
				return err
			}
			availableAccountID = accounts["available"]
			holdAccountID = accounts["hold"]
			revenueAccountID = accounts["revenue"]
			avail, err := Balance(ctx, tx, availableAccountID)
			if err != nil {
				return err
			}
			if avail < balanceApplied {
				return httpx.New(httpx.CodeConflict, "余额不足")
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
				 total_amount, balance_applied, payable_amount,
				 expires_at, coupon_id, subscription_id, idempotency_key_id,
				 manual_reason, created_by)
			VALUES ($1,$2,$3::uuid,'renewal','pending_payment',$4,
			        $5,$6,0,$7,$8,$9, now() + interval '30 minutes', $10, $11::uuid,
			        $12::uuid, $13, $14::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, currency,
			subtotal, discount, total, balanceApplied, payable,
			couponID(coupon), in.SubscriptionID, in.Claim.ID,
			nullIfEmpty(in.ManualReason), nullIfEmpty(in.ManualActor)).
			Scan(&orderID, &expiresAt); err != nil {
			return err
		}

		var reservationID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO order_reservations
				(tenant_id, order_id, user_id, expires_at)
			VALUES ($1, $2::uuid, $3::uuid, $4)
			RETURNING id::text`,
			tenantID, orderID, in.UserID, expiresAt,
		).Scan(&reservationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_reservation_events
				(tenant_id, reservation_id, order_id, from_state, to_state,
				 event_kind, business_request_id, actor_kind, actor_id)
			VALUES ($1, $2::uuid, $3::uuid, NULL, 'held',
			        'reserve', $4::uuid, 'user', $5::uuid)`,
			tenantID, reservationID, orderID, in.Claim.ID, in.UserID,
		); err != nil {
			return err
		}

		// 订单行同样要留快照：续费当时的价格与周期，日后对账靠它，
		// 而不是回头去读可能已经改过的 prices 表
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items
				(tenant_id, order_id, product_id, price_id, plan_id, plan_version_id,
				 snapshot_product_name, snapshot_plan_name, snapshot_plan_version,
				 snapshot_interval, snapshot_interval_count,
				 quantity, unit_amount, line_amount, currency)
			SELECT $1, $2::uuid, pl.product_id, $3::uuid, pl.id, pv.id,
			       COALESCE(pd.name, pl.name), pl.name, pv.version,
			       $4, $5, 1, $6, $6, $7
			  FROM plans pl
			  JOIN plan_versions pv ON pv.id = $8::uuid
			  LEFT JOIN products pd ON pd.id = pl.product_id
			 WHERE pl.tenant_id = $1 AND pl.id = $9::uuid`,
			tenantID, orderID, priceID, interval, intervalCount,
			unitAmount, currency, planVersionID, planID); err != nil {
			return fmt.Errorf("写续费订单行: %w", err)
		}

		if err := redeemCoupon(ctx, tx, tenantID, in.UserID, orderID,
			coupon, currency, reservationID); err != nil {
			return err
		}

		if balanceApplied > 0 {
			holdTxnID, err := Post(ctx, tx, tenantID, Posting{
				Kind: "balance_hold", Currency: currency,
				SourceType: "order", SourceID: &orderID,
				Memo: "renewal balance hold", ActorKind: "user", ActorID: &in.UserID,
				Entries: []Entry{
					{AccountID: availableAccountID, Direction: Debit, Amount: balanceApplied,
						Description: "renewal balance reserved"},
					{AccountID: holdAccountID, Direction: Credit, Amount: balanceApplied,
						Description: "renewal balance hold liability"},
				},
			})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO balance_holds
					(tenant_id, reservation_id, order_id, user_id, currency, amount,
					 available_account_id, hold_account_id, hold_txn_id)
				VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5, $6,
				        $7::uuid, $8::uuid, $9::uuid)`,
				tenantID, reservationID, orderID, in.UserID, currency, balanceApplied,
				availableAccountID, holdAccountID, holdTxnID,
			); err != nil {
				return err
			}
		}

		orderStatus := "pending_payment"
		if payable == 0 {
			if err := s.captureZeroPaySubscriptionOrder(ctx, tx, zeroPaySubscriptionCapture{
				Kind: "renewal", TenantID: tenantID, UserID: in.UserID, OrderID: orderID,
				SubscriptionID: in.SubscriptionID, ReservationID: reservationID,
				BusinessRequestID: in.Claim.ID, Currency: currency,
				SubtotalAmount: subtotal, DiscountAmount: discount,
				TotalAmount: total, BalanceApplied: balanceApplied,
				CouponID: renewalCouponID, HoldAccountID: holdAccountID,
				RevenueAccountID: revenueAccountID,
			}); err != nil {
				return err
			}
			orderStatus = "fulfilled"
		}

		if err := idempotencybind.BindResource(
			ctx, tx, in.Claim, "order", orderID,
		); err != nil {
			return err
		}

		// 人工续费「线下已收款」：订单与幂等绑定都已就位，在同一事务里按线下渠道
		// 结清，走与标记已支付相同的 settlePaymentTx（同 CreateOrder 的人工单）
		var settled *PaymentWebhookOutput
		if in.Offline != nil {
			settled = &PaymentWebhookOutput{}
			if err := s.settlePaymentTx(ctx, tx, tenantID, offlinePaymentInput(
				orderID, currency, payable, in.ManualActor, *in.Offline), settled); err != nil {
				return err
			}
			if !settled.Processed || settled.AlreadyHandled || settled.PaymentID == "" {
				return errors.New("offline settlement of a new manual renewal did not capture")
			}
			if settled.QuarantineKind != "" {
				return markPaidQuarantined(settled.QuarantineKind)
			}
			if err := tx.QueryRow(ctx, `SELECT status FROM orders
				WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, orderID).Scan(&orderStatus); err != nil {
				return err
			}
		}

		out = CreateOrderOutput{
			OrderID: orderID, OrderNo: orderNo, Currency: currency,
			DiscountAmount: discount, TotalAmount: total,
			BalanceApplied: balanceApplied, PayableAmount: payable,
			Status: orderStatus, settlement: settled,
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
			// 人工续费的审计与订单同一事务：没有审计的人工单等于没人负责
			entry := manualRenewalAudit(in, out, subtotal)
			entry.RequestID = httpx.RequestIDFrom(ctx)
			if err := audit.Write(ctx, tx, tenantID, entry); err != nil {
				return err
			}
		} else {
			userID := in.UserID
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "user", ActorID: &userID,
				Action: "subscription.renewal_created", ResourceType: "order",
				ResourceID: &orderID, APIDomain: "public", Outcome: "success",
				RequestID: httpx.RequestIDFrom(ctx),
				AfterDigest: map[string]any{
					"subscription_id": in.SubscriptionID, "order_no": orderNo,
					"total": total, "currency": currency,
				},
			}); err != nil {
				return err
			}
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

// zeroPaySubscriptionCapture 是挂在已有订阅上的零元单（续费 renewal、变更套餐
// upgrade）当场捕获所需的全部输入。
type zeroPaySubscriptionCapture struct {
	Kind              string
	TenantID          string
	UserID            string
	OrderID           string
	SubscriptionID    string
	ReservationID     string
	BusinessRequestID string
	Currency          string
	SubtotalAmount    int64
	DiscountAmount    int64
	TotalAmount       int64
	BalanceApplied    int64
	CouponID          *string
	HoldAccountID     string
	RevenueAccountID  string
	ProrationCredit   int64
}

// captureZeroPaySubscriptionOrder captures the complete held renewal or plan
// change graph and fulfils the already locked subscription before the create
// transaction commits.
func (s *Service) captureZeroPaySubscriptionOrder(ctx context.Context, tx pgx.Tx,
	in zeroPaySubscriptionCapture) error {
	if !subscriptionBoundOrderKind(in.Kind) {
		return fmt.Errorf("zero-pay subscription capture does not support order kind %q", in.Kind)
	}
	locked, err := lockOrderReservationGraph(ctx, tx, reservationLockRequest{
		TenantID: in.TenantID, OrderID: in.OrderID, UserID: in.UserID,
		Kind: in.Kind, Currency: in.Currency, CouponID: in.CouponID,
		SubtotalAmount: in.SubtotalAmount, DiscountAmount: in.DiscountAmount,
		TotalAmount: in.TotalAmount, PayableAmount: 0,
		BalanceAmount: in.BalanceApplied, ProrationCredit: in.ProrationCredit,
	})
	if err != nil {
		return err
	}

	var captureTxnID string
	if in.BalanceApplied > 0 {
		if in.BalanceApplied != in.TotalAmount || in.HoldAccountID == "" ||
			in.RevenueAccountID == "" {
			return errors.New("zero-pay " + in.Kind + " has an invalid balance hold")
		}
		captureTxnID, err = Post(ctx, tx, in.TenantID, Posting{
			Kind: "order_paid", Currency: in.Currency,
			SourceType: "order", SourceID: &in.OrderID,
			Memo:      "zero-pay " + in.Kind + " balance capture",
			ActorKind: "user", ActorID: &in.UserID,
			Entries: []Entry{
				{AccountID: in.HoldAccountID, Direction: Debit,
					Amount: in.BalanceApplied, Description: "capture " + in.Kind + " balance hold"},
				{AccountID: in.RevenueAccountID, Direction: Credit,
					Amount: in.TotalAmount, Description: in.Kind + " revenue"},
			},
		})
		if err != nil {
			return err
		}
	}
	if err := captureLockedReservation(ctx, tx, in.TenantID, in.OrderID,
		in.UserID, in.BusinessRequestID, locked, captureTxnID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders
		   SET status='paid', paid_amount=total_amount, paid_at=now()
		 WHERE tenant_id=$1 AND id=$2::uuid AND status='pending_payment'`,
		in.TenantID, in.OrderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("zero-pay " + in.Kind + " paid transition lost")
	}
	if in.Kind == "upgrade" {
		_, err = s.fulfillPlanChangeLocked(ctx, tx, in.TenantID, in.OrderID,
			in.UserID, in.SubscriptionID)
		return err
	}
	_, err = s.fulfillRenewalLocked(ctx, tx, in.TenantID, in.OrderID,
		in.UserID, in.SubscriptionID)
	return err
}

// subscriptionBoundOrderKind 是挂在已有订阅上、履约时原地改那条订阅的订单：
// 续费与变更套餐。它们的结算与创建都先锁订阅，再碰预留图与账本。
func subscriptionBoundOrderKind(kind string) bool {
	return kind == "renewal" || kind == "upgrade"
}

// subscriptionAcceptsPaidChange 是续费与变更套餐对订阅状态的唯一口径：建单时
// 校验一次，结算时锁住订阅后再校验一次——支付窗口里订阅可能被改成终态，履约
// 写 active 会被状态机拒绝，钱于是改走挂账（R117）。迁移 00124 的挂账守卫写着
// 同一组状态，renewal_contract_test.go 钉住两边一致。
//
// 生效中的四种状态之外，已过期但原地续费窗口没关（renewal_closed_at 为空，过期
// 不满 30 天）的订阅也收：续费、改套餐都在原订阅上做，链接不变（规则 4）。
// 窗口关了只能新购。已取消、暂停、待开通一律不收。
func subscriptionAcceptsPaidChange(status string, renewalClosed bool) bool {
	switch status {
	case "active", "trialing", "grace", "past_due":
		return true
	case "expired":
		return !renewalClosed
	}
	return false
}

// ErrRenewalWindowClosed 是过期超过原地续费窗口的订阅再来续费时的 409。
var ErrRenewalWindowClosed = httpx.New(httpx.CodeConflict,
	"这条订阅已过期超过 30 天，不能再原地续费，请重新购买（会换新的订阅链接）")

// lockOrderSubscriptionForSettlement establishes the shared lock order for
// renewal and plan change settlement:
// order -> subscription -> payment intents -> reservation graph -> ledger.
// The caller has already locked the order before entering this helper. The
// returned status and renewal-window flag are read under the lock, for the
// settlement-time recheck (subscriptionAcceptsPaidChange).
func lockOrderSubscriptionForSettlement(ctx context.Context, tx pgx.Tx,
	tenantID, orderID, userID string) (string, string, bool, error) {
	var subID string
	if err := tx.QueryRow(ctx, `
		SELECT subscription_id::text FROM orders
		 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		   AND kind IN ('renewal','upgrade')`,
		tenantID, orderID, userID).Scan(&subID); err != nil {
		return "", "", false, fmt.Errorf("读取订单关联的订阅: %w", err)
	}
	if subID == "" {
		return "", "", false, errors.New("续费或变更订单没有关联订阅")
	}
	var lockedID, status string
	var closed bool
	if err := tx.QueryRow(ctx, `
		SELECT id::text, status, renewal_closed_at IS NOT NULL FROM subscriptions
		 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		 FOR UPDATE`, tenantID, subID, userID).Scan(&lockedID, &status, &closed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, httpx.NotFoundOrForbidden()
		}
		return "", "", false, err
	}
	return lockedID, status, closed, nil
}

// manualRenewalAudit 是后台人工开单落成续费单时的审计（与订单同一事务）。
func manualRenewalAudit(in CreateRenewalInput, out CreateOrderOutput, subtotal int64) audit.Entry {
	actor := in.ManualActor
	orderID := out.OrderID
	digest := map[string]any{
		"order_no": out.OrderNo, "user_id": in.UserID,
		"subscription_id": in.SubscriptionID, "renewal": true,
		"reason": in.ManualReason, "status": out.Status,
		"settlement": in.ManualSettlement,
		"subtotal":   subtotal, "granted": out.DiscountAmount,
	}
	if out.settlement != nil {
		digest["reference"] = in.Offline.Reference
		digest["amount"] = out.PayableAmount
		digest["currency"] = out.Currency
		digest["payment_id"] = out.settlement.PaymentID
		digest["ledger_txn"] = out.settlement.LedgerTxnID
	}
	return audit.Entry{
		ActorKind: "admin", ActorID: &actor,
		Action: "order.manual_created", ResourceType: "order",
		ResourceID: &orderID, AfterDigest: digest,
		APIDomain: "admin",
	}
}
