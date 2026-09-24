// [INPUT]: 依赖 reservations.go 的预留图与科目锁、ledger.go 的记账、traffic_reset.go 的 LogTrafficReset、middleware 幂等声明、platform/db
// [OUTPUT]: 对外提供 CreateRenewal、RollQuotaPeriods；包内提供 fulfillRenewal / fulfillRenewalLocked，以及续费与变更套餐（plan_change.go）共用的 captureZeroPaySubscriptionOrder / lockOrderSubscriptionForSettlement
// [POS]: billing 的续费：在原订阅上延长周期、重置 cycle 配额；流量包余额挂用户，续费不碰（D-E-1）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
}

// CreateRenewal 为一条已有订阅建续费订单。
func (s *Service) CreateRenewal(ctx context.Context, tenantID string,
	in CreateRenewalInput) (*CreateOrderOutput, error) {
	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, in.UserID, RenewalIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("create renewal: %w", err)
	}

	var out CreateOrderOutput
	err := s.pool.InTxSerializable(ctx, dbScope(tenantID, in.UserID), func(tx pgx.Tx) error {
		var (
			planID, planVersionID string
			curPriceID            *string
			status                string
			periodEnd             *time.Time
		)
		if err := tx.QueryRow(ctx, `
			SELECT plan_id::text, plan_version_id::text, price_id::text,
			       status, current_period_end
			  FROM subscriptions
			 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
			 FOR UPDATE`,
			tenantID, in.SubscriptionID, in.UserID).Scan(
			&planID, &planVersionID, &curPriceID, &status, &periodEnd); err != nil {
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

		// 已取消或已结束的订阅不给续 —— 那种情况应该走重新购买，
		// 因为权益版本、价格、节点分组可能都已经变了
		switch status {
		case "active", "trialing", "grace", "past_due":
		default:
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
		total := subtotal - discount

		balanceApplied := in.UseBalance
		if balanceApplied < 0 {
			balanceApplied = 0
		}
		if balanceApplied > total {
			balanceApplied = total
		}
		payable := total - balanceApplied
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
				 expires_at, coupon_id, subscription_id, idempotency_key_id)
			VALUES ($1,$2,$3::uuid,'renewal','pending_payment',$4,
			        $5,$6,0,$7,$8,$9, now() + interval '30 minutes', $10, $11::uuid,
			        $12::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, currency,
			subtotal, discount, total, balanceApplied, payable,
			couponID(coupon), in.SubscriptionID, in.Claim.ID).
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

		out = CreateOrderOutput{
			OrderID: orderID, OrderNo: orderNo, Currency: currency,
			DiscountAmount: discount, TotalAmount: total,
			BalanceApplied: balanceApplied, PayableAmount: payable,
			Status: orderStatus,
		}
		prepared, err := httpx.PrepareJSON(http.StatusCreated, out)
		if err != nil {
			return err
		}
		out.prepared = prepared
		if err := idempotencybind.BindResource(
			ctx, tx, in.Claim, "order", orderID,
		); err != nil {
			return err
		}
		if err := idempotencybind.CompleteSuccessJSON(
			ctx, tx, in.Claim, "order", orderID, prepared,
		); err != nil {
			return err
		}

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

// lockOrderSubscriptionForSettlement establishes the shared lock order for
// renewal and plan change settlement:
// order -> subscription -> payment intents -> reservation graph -> ledger.
// The caller has already locked the order before entering this helper.
func lockOrderSubscriptionForSettlement(ctx context.Context, tx pgx.Tx,
	tenantID, orderID, userID string) (string, error) {
	var subID string
	if err := tx.QueryRow(ctx, `
		SELECT subscription_id::text FROM orders
		 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		   AND kind IN ('renewal','upgrade')`,
		tenantID, orderID, userID).Scan(&subID); err != nil {
		return "", fmt.Errorf("读取订单关联的订阅: %w", err)
	}
	if subID == "" {
		return "", errors.New("续费或变更订单没有关联订阅")
	}
	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM subscriptions
		 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		 FOR UPDATE`, tenantID, subID, userID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", httpx.NotFoundOrForbidden()
		}
		return "", err
	}
	return lockedID, nil
}

// fulfillRenewal 在支付成功后延长订阅周期并按策略重置配额。
// Callers must lock the subscription before any renewal ledger-account lock.
func (s *Service) fulfillRenewal(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, userID string) (string, error) {
	subID, err := lockOrderSubscriptionForSettlement(ctx, tx, tenantID, orderID, userID)
	if err != nil {
		return "", err
	}
	return s.fulfillRenewalLocked(ctx, tx, tenantID, orderID, userID, subID)
}

func (s *Service) fulfillRenewalLocked(ctx context.Context, tx pgx.Tx, tenantID,
	orderID, userID, subID string) (string, error) {

	if subID == "" {
		return "", errors.New("续费订单没有关联订阅")
	}

	var (
		interval      string
		intervalCount int16
		planVersionID string
		priceID       *string
	)
	if err := tx.QueryRow(ctx, `
		SELECT snapshot_interval, snapshot_interval_count,
		       plan_version_id::text, price_id::text
		  FROM order_items
		 WHERE tenant_id = $1 AND order_id = $2::uuid
		 ORDER BY created_at LIMIT 1`,
		tenantID, orderID).Scan(&interval, &intervalCount,
		&planVersionID, &priceID); err != nil {
		return "", fmt.Errorf("读取续费订单行: %w", err)
	}

	var oldEnd *time.Time
	var oldStatus string
	if err := tx.QueryRow(ctx, `
		SELECT current_period_end, status FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id=$3::uuid`,
		tenantID, subID, userID).Scan(&oldEnd, &oldStatus); err != nil {
		return "", err
	}

	// 从当前周期末往后延，而不是从今天。
	//
	// 提前三天续费的人，那三天是他已经买过的 —— 从今天重算等于把它们吞掉。
	// 但已经过期的订阅要从现在开始算，否则续一个月只补回过去的日子，
	// 用户付了钱却发现还是过期状态。
	now := time.Now().UTC()
	base := now
	if oldEnd != nil && oldEnd.After(now) {
		base = *oldEnd
	}
	newEnd := addInterval(base, interval, int(intervalCount))

	if _, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET status = 'active',
		       current_period_start = CASE WHEN status IN ('expired','past_due','grace')
		                                   THEN $3 ELSE current_period_start END,
		       current_period_end = $4,
		       plan_version_id = $5::uuid,
		       price_id = COALESCE($6::uuid, price_id),
		       grace_end = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`,
		tenantID, subID, now, newEnd, planVersionID, priceID); err != nil {
		return "", fmt.Errorf("延长订阅周期: %w", err)
	}

	// 只有跟随订阅周期的 cycle 配额在续费时清零。day/month 有自己的
	// RollQuotaPeriods 边界；在这里把它们改成订阅周期末，会吞掉尚未结束
	// 的日/月额度。total 也是订阅存续期总量，始终保留。
	// 先把清零前的用量取出来 —— UPDATE 之后就再也读不到了，
	// 而「续费时你已经用了多少」正是用户最常问的那个数字。
	resetRows, err := tx.Query(ctx, `
		SELECT metric, consumed FROM quota_balances
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid AND period = 'cycle'`,
		tenantID, subID)
	if err != nil {
		return "", err
	}
	type consumedSnapshot struct {
		metric   string
		consumed int64
	}
	var beforeReset []consumedSnapshot
	for resetRows.Next() {
		var snap consumedSnapshot
		if err := resetRows.Scan(&snap.metric, &snap.consumed); err != nil {
			resetRows.Close()
			return "", err
		}
		beforeReset = append(beforeReset, snap)
	}
	resetRows.Close()
	if err := resetRows.Err(); err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE quota_balances
		   SET consumed = 0,
		       period_start = $3,
		       period_end = $4,
		       notified_thresholds = '{}',
		       overage_applied_at = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid
		   AND period = 'cycle'`,
		tenantID, subID, now, newEnd); err != nil {
		return "", fmt.Errorf("重置周期配额: %w", err)
	}

	for _, snap := range beforeReset {
		if err := LogTrafficReset(ctx, tx, tenantID, subID, userID,
			snap.metric, "renewal", snap.consumed, nil, ""); err != nil {
			return "", fmt.Errorf("记录续费重置: %w", err)
		}
	}

	// 套餐版本可能在这次续费里变了（换了价格档），配额上限要跟着走。
	// metric 在同一版本下允许同时存在 cycle/day/month/total，必须把
	// period 也作为连接键；只按 metric 会让 PostgreSQL 从多行中任取一条。
	// day/month/total 只更新基础上限，已用量和周期边界保持不动。
	if _, err := tx.Exec(ctx, `
		UPDATE quota_balances qb
		   SET limit_value = qd.limit_value,
		       granted = COALESCE(qd.limit_value, 0)
		  FROM quota_definitions qd
		 WHERE qb.tenant_id = $1 AND qb.subscription_id = $2::uuid
		   AND qd.plan_version_id = $3::uuid
		   AND qd.metric = qb.metric AND qd.period = qb.period`,
		tenantID, subID, planVersionID); err != nil {
		return "", fmt.Errorf("更新配额上限: %w", err)
	}

	// 凭据有效期跟着周期走。token 本身不换 ——
	// 换了等于让用户所有设备重新导入一次订阅
	if _, err := tx.Exec(ctx, `
		UPDATE subscription_credentials
		   SET expires_at = $3
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid
		   AND status = 'active'`,
		tenantID, subID, newEnd); err != nil {
		return "", fmt.Errorf("延长凭据有效期: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status,
			 actor_kind, order_id, payload)
		VALUES ($1,$2::uuid,'renewed',$3,'active','payment',$4::uuid,$5)`,
		tenantID, subID, oldStatus, orderID,
		map[string]any{"period_end": newEnd, "previous_end": oldEnd}); err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, orderID); err != nil {
		return "", err
	}
	return subID, nil
}

// RollQuotaPeriods 把周期已过的配额滚到下一个周期。
//
// 只处理 period 为 day / month 的配额 —— 那类的周期独立于订阅周期
// （比如年付套餐但流量按月给）。period='cycle' 的跟着订阅走，
// 由续费负责重置；period='total' 永不重置。
//
// 订阅本身已经过期的不滚：那种情况该做的是停服，不是给他发新一轮流量。
func (s *Service) RollQuotaPeriods(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 三段 CTE：先把到期的行连同「清零前用了多少」快照下来，
		// 再更新，最后拿快照写日志。
		//
		// 不能只靠 UPDATE ... RETURNING —— 它返回的是更新后的值，
		// 也就是 0。而日志存在的全部意义就是留住清零前的那个数字。
		tag, err := tx.Exec(ctx, `
			WITH due AS (
				SELECT qb.id, qb.subscription_id, s.user_id, qb.metric, qb.consumed
				  FROM quota_balances qb
				  JOIN subscriptions s
				    ON s.tenant_id = qb.tenant_id AND s.id = qb.subscription_id
				 WHERE qb.tenant_id = $1
				   AND qb.period IN ('day','month')
				   AND qb.period_end IS NOT NULL
				   AND qb.period_end <= now()
				   AND s.status IN ('active','trialing','grace')
				   AND (s.current_period_end IS NULL OR s.current_period_end > now())
			), rolled AS (
				UPDATE quota_balances qb
				   SET consumed = 0,
				       period_start = qb.period_end,
				       period_end = CASE qb.period
				                      WHEN 'day'   THEN qb.period_end + interval '1 day'
				                      WHEN 'month' THEN qb.period_end + interval '1 month'
				                    END,
				       notified_thresholds = '{}',
				       overage_applied_at = NULL,
				       updated_at = now()
				  FROM due d
				 WHERE qb.id = d.id
				RETURNING qb.id
			)
			INSERT INTO traffic_reset_logs
				(tenant_id, subscription_id, user_id, metric, reason,
				 consumed_before, consumed_after)
			SELECT $1, d.subscription_id, d.user_id, d.metric, 'cycle_roll', d.consumed, 0
			  FROM due d`,
			tenantID)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		return nil
	})
	return n, err
}
