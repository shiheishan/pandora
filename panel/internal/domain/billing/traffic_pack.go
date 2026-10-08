package billing

// 流量包挂在用户身上，永不过期、用完为止、可叠加（D-E-1）。
//
// 余额是一笔一笔的 traffic_pack_grants：每次购买、每张礼品卡各一笔，
// 扣量时按先到先扣逐笔累加 consumed_bytes。不合成一个数字，是因为
// 「我买的 100G 去哪了」要能逐笔对质 —— 哪一单、哪张卡、各用了多少。
//
// 订单形状：kind='addon'，一行订单项指向流量包（traffic_pack_id），
// 容量快照写在 snapshot_quotas 里；履约只认快照，不回查流量包当前的容量 ——
// 流量包改了容量，已经买了的人拿到的仍是他付钱那一刻的数。

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

// TrafficPack 是公开目录里的一个流量包。
type TrafficPack struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TrafficBytes int64  `json:"traffic_bytes"`
	Currency     string `json:"currency"`
	UnitAmount   int64  `json:"unit_amount"`
	Recommended  bool   `json:"recommended"`
}

// ListTrafficPacks 返回在售的流量包，按排序号。
func (s *Service) ListTrafficPacks(ctx context.Context, tenantID string) ([]TrafficPack, error) {
	out := []TrafficPack{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, traffic_bytes, currency::text, unit_amount, recommended
			  FROM traffic_packs
			 WHERE tenant_id = $1 AND status = 'active'
			 ORDER BY sort_order, created_at, id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p TrafficPack
			if err := rows.Scan(&p.ID, &p.Name, &p.TrafficBytes, &p.Currency,
				&p.UnitAmount, &p.Recommended); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

type CreateTrafficPackOrderInput struct {
	UserID string
	// SubscriptionID 是流量包加到哪一份（必填，必须是本人生效中的订阅）
	SubscriptionID string
	PackID         string
	UseBalance     int64
	CouponCode     string
	Claim          middleware.IdempotencyClaim
	// Expect 是确认时带回的报价，见 Expectation
	Expect *Expectation
}

// CreateTrafficPackOrder 建一张流量包订单（kind='addon'），加到 SubscriptionID 那一份上。
//
// 必须指定一份生效中的订阅（购买模型统一 Q5）：一份都没有时按原型提示「先续费或买个套餐」。
// 订单的 subscription_id 存这一份，履约时余额挂上去（00137 的守卫核对）。
// 幂等域与新购共用 order_create —— 数据库的订单/幂等对称绑定按 kind 映射
// 幂等域，addon 映射到它（迁移 00070），不必另开一个域去改绑定函数。
func (s *Service) CreateTrafficPackOrder(ctx context.Context, tenantID string,
	in CreateTrafficPackOrderInput) (*CreateOrderOutput, error) {

	if err := middleware.ValidateIdempotencyClaim(
		in.Claim, tenantID, in.UserID, CheckoutIdempotencyScope,
	); err != nil {
		return nil, fmt.Errorf("create traffic pack order: %w", err)
	}
	if _, err := uuid.Parse(in.PackID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if in.SubscriptionID == "" {
		return nil, httpx.Invalid(map[string]string{"subscription_id": "请选择加到哪一份"})
	}
	if _, err := uuid.Parse(in.SubscriptionID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}

	var out CreateOrderOutput
	scope := db.Scope{TenantID: tenantID, ActorID: in.UserID}
	err := s.pool.InTxSerializableRetry(ctx, scope, func(tx pgx.Tx) error {
		var name, currency string
		var trafficBytes, unitAmount int64
		err := tx.QueryRow(ctx, `
			SELECT name, traffic_bytes, currency::text, unit_amount
			  FROM traffic_packs
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'active'
			 FOR SHARE`, tenantID, in.PackID).
			Scan(&name, &trafficBytes, &currency, &unitAmount)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if err := lockLiveSubscriptionForPack(ctx, tx, tenantID, in.UserID, in.SubscriptionID); err != nil {
			return err
		}

		// 金额：先优惠券、再余额（顺序理由见 CreateOrder）。限定套餐的券对
		// 流量包不适用，planID 传空即可让 applyCoupon 按「不在适用范围」拒绝。
		subtotal := unitAmount
		coupon, err := applyCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode,
			"", currency, subtotal)
		if err != nil {
			return err
		}
		var discount int64
		if coupon != nil {
			discount = coupon.Discount
		}
		total := subtotal - discount
		// 余额经 purchase.ApplyBalance 收尾（最低付款额、Forced、SmallDue 免单）
		bal, err := balancePlan(ctx, tx, tenantID, in.UserID, currency, total, in.UseBalance, false)
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
				 idempotency_key_id, subscription_id)
			VALUES ($1, $2, $3::uuid, 'addon', 'pending_payment', $4,
			        $5, $6, 0, $7, $8, $9, now() + interval '30 minutes', $10, $11::uuid, $12::uuid)
			RETURNING id::text, expires_at`,
			tenantID, orderNo, in.UserID, currency, subtotal, discount, total,
			balanceApplied, payable, couponID(coupon), in.Claim.ID, in.SubscriptionID,
		).Scan(&orderID, &expiresAt); err != nil {
			return err
		}

		reservationID, err := insertHeldReservation(ctx, tx, tenantID, orderID,
			in.UserID, in.Claim.ID, expiresAt)
		if err != nil {
			return err
		}
		if err := redeemCoupon(ctx, tx, tenantID, in.UserID, orderID, coupon,
			currency, reservationID); err != nil {
			return err
		}

		snapshot := []map[string]any{{
			"metric": "traffic.bytes", "limit": trafficBytes,
			"unit": "bytes", "period": "total",
		}}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items
				(tenant_id, order_id, traffic_pack_id, snapshot_product_name,
				 snapshot_quotas, quantity, unit_amount, line_amount, currency)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, 1, $6, $6, $7)`,
			tenantID, orderID, in.PackID, name, snapshot, unitAmount, currency); err != nil {
			return err
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
				Kind: "addon", Currency: currency, TotalAmount: total,
				BalanceApplied: balanceApplied, HoldAccountID: holdAccounts.HoldID,
				Coupon: coupon,
			}); err != nil {
				return err
			}
			status = "fulfilled"
		}

		out = CreateOrderOutput{
			DiscountAmount: discount, OrderID: orderID, OrderNo: orderNo,
			Currency: currency, TotalAmount: total, BalanceApplied: balanceApplied,
			PayableAmount: payable, Status: status,
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
			Action: "order.created", ResourceType: "order", ResourceID: &orderID,
			AfterDigest: map[string]any{
				"order_no": orderNo, "total": total, "currency": currency,
				"traffic_pack_id": in.PackID, "traffic_bytes": trafficBytes,
				"subscription_id": in.SubscriptionID, "small_due_waived": bal.Waived,
			},
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		}); err != nil {
			return err
		}
		if err := plugin.EmitOrderCreated(ctx, tx, tenantID, orderID, orderNo,
			in.UserID, currency, total); err != nil {
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

// fulfillTrafficPackOrder 按订单项的容量快照发一笔余额，并把已支付的订单收尾为 fulfilled。
// 调用方负责订单已在本事务里转到 paid。
func fulfillTrafficPackOrder(ctx context.Context, tx pgx.Tx, tenantID, orderID,
	userID string) (string, error) {

	var bytes int64
	var subID *string
	if err := tx.QueryRow(ctx, `
		SELECT (oi.snapshot_quotas->0->>'limit')::bigint, o.subscription_id::text
		  FROM order_items oi
		  JOIN orders o ON o.tenant_id = oi.tenant_id AND o.id = oi.order_id
		 WHERE oi.tenant_id = $1 AND oi.order_id = $2::uuid
		   AND oi.traffic_pack_id IS NOT NULL
		   AND oi.snapshot_quotas->0->>'metric' = 'traffic.bytes'`,
		tenantID, orderID).Scan(&bytes, &subID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errors.New("addon order has no traffic pack snapshot")
		}
		return "", err
	}
	// 升级前下的在途单没有指定订阅：用户恰好有一份生效中的就挂到那份，否则留作未分配
	if subID == nil {
		var err error
		if subID, err = soleLiveSubscription(ctx, tx, tenantID, userID); err != nil {
			return "", err
		}
	}
	grantID, err := GrantTrafficPackTx(ctx, tx, tenantID, userID, subID, "order", orderID, bytes)
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'fulfilled', fulfilled_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'paid'`, tenantID, orderID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("addon order fulfilment transition lost")
	}
	return grantID, nil
}

// GrantTrafficPackTx 在调用方事务里给用户发一笔流量包余额，挂到 subscriptionID 那一份
// （为空表示还没加到任何一份）。source 与 source_id 唯一：同一单、同一张卡重复调用会撞
// 唯一约束而不是发两份。订阅属于同一用户由 00137 的约束触发器在提交时核对。
func GrantTrafficPackTx(ctx context.Context, tx pgx.Tx, tenantID, userID string,
	subscriptionID *string, source, sourceID string, bytes int64) (string, error) {

	if bytes <= 0 {
		return "", errors.New("traffic pack grant must be positive")
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO traffic_pack_grants
			(tenant_id, user_id, source, source_id, granted_bytes, subscription_id)
		VALUES ($1, $2::uuid, $3, $4::uuid, $5, $6::uuid)
		RETURNING id::text`, tenantID, userID, source, sourceID, bytes, subscriptionID).Scan(&id)
	return id, err
}

// TrafficPackGrant 是用户看到的一笔余额。
type TrafficPackGrant struct {
	ID string `json:"id"`
	// SubscriptionID 是这笔余额挂在哪一份上，null 表示还没加到任何一份
	SubscriptionID *string   `json:"subscription_id"`
	Source         string    `json:"source"`
	OrderID        *string   `json:"order_id"`
	GrantedBytes   int64     `json:"granted_bytes"`
	ConsumedBytes  int64     `json:"consumed_bytes"`
	RemainingBytes int64     `json:"remaining_bytes"`
	CreatedAt      time.Time `json:"created_at"`
}

type TrafficPackBalance struct {
	RemainingBytesTotal int64              `json:"remaining_bytes_total"`
	Packs               []TrafficPackGrant `json:"packs"`
}

// MyTrafficPacks 返回用户的流量包余额：总剩余与逐笔明细（最近的在前，最多 100 笔）。
func (s *Service) MyTrafficPacks(ctx context.Context, tenantID, userID string) (*TrafficPackBalance, error) {
	out := TrafficPackBalance{Packs: []TrafficPackGrant{}}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(granted_bytes - consumed_bytes), 0)::bigint
			  FROM traffic_pack_grants WHERE tenant_id = $1 AND user_id = $2::uuid`,
			tenantID, userID).Scan(&out.RemainingBytesTotal); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text, subscription_id::text, source,
			       CASE WHEN source = 'order' THEN source_id::text END,
			       granted_bytes, consumed_bytes, created_at
			  FROM traffic_pack_grants
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			 ORDER BY created_at DESC, id DESC LIMIT 100`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g TrafficPackGrant
			if err := rows.Scan(&g.ID, &g.SubscriptionID, &g.Source, &g.OrderID, &g.GrantedBytes,
				&g.ConsumedBytes, &g.CreatedAt); err != nil {
				return err
			}
			g.RemainingBytes = g.GrantedBytes - g.ConsumedBytes
			out.Packs = append(out.Packs, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// errPackNeedsLiveSubscription 是流量包要加到的那一份不在用（原型：先续费或买个套餐）。
var errPackNeedsLiveSubscription = httpx.New(httpx.CodeConflict,
	"流量包只能加到在用的套餐上，先续费或买个套餐")

// lockLiveSubscriptionForPack 核对流量包要加到的那一份是本人的、生效中的；FOR KEY SHARE 只防删，
// 不挡续费与扣量对订阅行的更新。
func lockLiveSubscriptionForPack(ctx context.Context, tx pgx.Tx, tenantID, userID, subID string) error {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
		 FOR KEY SHARE`, tenantID, subID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return err
	}
	// 生效中 = 可付费变更的状态里去掉「过期但窗口没关」（窗口按已关传入）
	if subscriptionAcceptsPaidChange(status, true) {
		return nil
	}
	return errPackNeedsLiveSubscription
}

// soleLiveSubscription 返回用户唯一一份生效中的订阅；没有或不止一份时为 nil（不替用户选）。
func soleLiveSubscription(ctx context.Context, tx pgx.Tx, tenantID, userID string) (*string, error) {
	rows, err := tx.Query(ctx, `
		SELECT s.id::text FROM subscriptions s
		 WHERE s.tenant_id = $1 AND s.user_id = $2::uuid
		   AND s.status IN ('active','trialing','grace','past_due')
		 LIMIT 2`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil || len(ids) != 1 {
		return nil, err
	}
	return &ids[0], nil
}
