// [INPUT]: 依赖 reservations.go 的预留图加锁与捕获、ledger.go 的记账、unexpected_payment.go 的挂账隔离、renewal.go / plan_change.go 的订阅锁与履约、provision.go 的开订阅，依赖 platform/db、platform/audit、platform/httpx
// [OUTPUT]: 对外提供 PaymentWebhookInput / PaymentWebhookOutput、Service.HandlePaymentWebhook；包内提供 settlePaymentTx、postOrderPaid、fulfillOrder
// [POS]: billing 的结算主链，整条放在一个文件：HandlePaymentWebhook 开事务调 settlePaymentTx（mark-paid 与人工单线下收款在各自事务里复用它），锁序为 支付事件判重 → 订单 → 续费 / 变更单的订阅（复核订阅状态，不收即隔离进挂账，R117）→ 支付意图 → 预留图 → 账本科目，捕获预留后按 kind 分派履约，审计后强制延迟约束
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
	// QuarantineKind 非空表示这笔钱没有结算订单，而是按这个 case_kind 进了挂账
	// （unexpected_payment.go）。渠道回执照样成功；标记已付据此回 409。
	QuarantineKind string `json:"-"`
}

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
		return httpx.New(httpx.CodeBadRequest, "缺少支付事件号或支付流水号")
	}
	if in.Amount <= 0 || in.FeeAmount < 0 || in.FeeAmount > in.Amount {
		return httpx.New(httpx.CodeBadRequest, "支付金额或手续费不正确")
	}

	var providerID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM payment_providers
		 WHERE tenant_id=$1 AND code=$2`, tenantID, in.ProviderCode).Scan(&providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.New(httpx.CodeNotFound, "支付渠道不存在")
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
		return httpx.New(httpx.CodeBadRequest, "支付回调缺少订单号")
	}
	err = tx.QueryRow(ctx, query, tenantID, identifier).Scan(
		&orderID, &userID, &status, &currency, &subtotalAmount,
		&discountAmount, &taxAmount, &payable, &balanceApplied, &totalAmount,
		&orderKind, &businessRequestID, &couponID, &idempotencyKeyID, &prorationCredit)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.New(httpx.CodeNotFound, "订单不存在")
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
	recordedWhilePending := false
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
			// 未结的订单上已有这笔收款，只可能是订阅不收时隔离进挂账的那笔（R117），
			// 换了事件号重投：交给下面的隔离分支按重放处理
			if !subscriptionBoundOrderKind(orderKind) {
				return errors.New("provider payment exists before order reached a terminal state")
			}
			recordedWhilePending = true
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
		return httpx.New(httpx.CodeConflict, "该订单当前状态不可支付")
	}
	if in.Currency != currency || in.Amount != payable {
		return httpx.New(httpx.CodeConflict, "支付币种或金额与订单不一致")
	}

	// Renewal and plan-change settlement must acquire the existing
	// subscription before any reservation child or ledger-account lock.
	// Their creation uses the same subscription -> coupon -> ledger order,
	// closing the cross-flow deadlock cycle without weakening new-order or
	// top-up settlement.
	var renewalSubscriptionID string
	if subscriptionBoundOrderKind(orderKind) {
		var subscriptionStatus string
		renewalSubscriptionID, subscriptionStatus, err = lockOrderSubscriptionForSettlement(
			ctx, tx, tenantID, orderID, userID,
		)
		if err != nil {
			return err
		}
		// 建单时校验过订阅状态，但支付窗口里订阅可能已被改成终态：照常履约会写
		// active、被状态机拒绝，整笔结算回滚，连收款证据都留不下。钱已经到了，
		// 按建单同一口径复核，不合格就隔离进挂账，订单与订阅都不动；订单之后
		// 照常过期或被取消，释放时退回余额冻结（R117）。
		if recordedWhilePending || !subscriptionAcceptsPaidChange(subscriptionStatus) {
			quarantined, err := s.quarantineUnexpectedPayment(ctx, tx,
				tenantID, eventID, providerID, orderID, userID, status,
				in.ProviderCode, "ineligible_subscription", in)
			if err != nil {
				return err
			}
			*out = *quarantined
			return nil
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
