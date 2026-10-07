package billing

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 渠道侧的支付状态，查单结果的 channel_status。
//
// 只有渠道答上话才有这三种之一；渠道查不了（停用、不支持、超时、报错）
// 一律以错误返回，不混进结果里——调用方据此区分「渠道说没付」与「没查成」。
const (
	ChannelPaid     = "paid"
	ChannelUnpaid   = "unpaid"
	ChannelNotFound = "not_found"
)

// channelQueryTimeout 是单次渠道查询的上限。查单是人点的或巡检排着队的，
// 渠道卡住不能把请求或整轮巡检一起拖住。
const channelQueryTimeout = 10 * time.Second

// OrderPaymentQuery 是一次主动查单的结果，json tag 即后台与门户
// POST v1/orders/{id}/query 的响应形状。
//
// Reconciled 与 AlreadyRecorded 互斥：前者表示这次查单把一笔此前没记上的钱
// 补记了（结清订单，或进了挂账）；后者表示渠道说的那笔钱此前已经入账
// （回调先到，或之前查过）。QuarantineKind 非空表示补记进了挂账而不是结清订单，
// 例如订单已取消或已过期之后才查到的钱（released_order）。
type OrderPaymentQuery struct {
	OrderID         string `json:"order_id"`
	OrderNo         string `json:"order_no"`
	ProviderCode    string `json:"provider_code"`
	ChannelStatus   string `json:"channel_status"`
	Reconciled      bool   `json:"reconciled"`
	AlreadyRecorded bool   `json:"already_recorded"`
	QuarantineKind  string `json:"quarantine_kind,omitempty"`
	OrderStatus     string `json:"order_status"`
}

// QueryOrderPayment 向订单发起过支付的渠道查单，查到已付就补记。
//
// ownerID 非空时只许查本人订单（门户），他人订单与不存在同一个 404；
// 后台传空串。订单从没发起过支付（没有任何支付意图）回 409。
//
// 用户中途换过渠道或方式时，订单名下会有多条意图，各有各的对外单号（易支付换方式时是
// 「订单号-序号」，见 payments.go）：按最近发起的在前逐个查，任一笔说已付就补记并返回。
// 没有一笔说已付时，返回最近一个答上话的结果；所有都没查成才返回错误。
func (s *PaymentService) QueryOrderPayment(ctx context.Context, tenantID, orderID, ownerID string) (*OrderPaymentQuery, error) {
	res, _, err := s.queryOrderPayment(ctx, tenantID, orderID, ownerID, nil)
	return res, err
}

// queryTarget 是查单前读到的订单号与对外单号；后台审计要在查单失败时也写清查的是谁。
type queryTarget struct {
	OrderNo   string
	Providers []string
	Refs      []queryRef
}

// queryRef 是一次要查的「渠道 + 对外单号」。
type queryRef struct {
	Code       string
	OutTradeNo string
}

// settleHook 在查单补记的结算事务里、结算之后执行（后台查单在这里写审计，与补记同生共死）。
type settleHook func(ctx context.Context, tx pgx.Tx, res *OrderPaymentQuery) error

// queryOrderPayment 是 QueryOrderPayment 的本体，另把查单对象带回来；
// 订单不存在或无权访问时 target 为 nil。hook 只在渠道说已付、走了结算事务时调用。
func (s *PaymentService) queryOrderPayment(ctx context.Context, tenantID, orderID, ownerID string,
	hook settleHook) (*OrderPaymentQuery, *queryTarget, error) {

	target, err := s.loadQueryTarget(ctx, tenantID, orderID, ownerID)
	if err != nil {
		var he *httpx.Error
		if target != nil && errors.As(err, &he) && he.Code == httpx.CodeConflict {
			// 订单在，只是从没发起过支付：照样算一次有对象的查单
			return nil, target, err
		}
		return nil, nil, err
	}

	var answer *OrderPaymentQuery
	var firstErr error
	for _, ref := range target.Refs {
		res, err := s.queryProvider(ctx, tenantID, orderID, target.OrderNo, ref, hook)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if res.ChannelStatus == ChannelPaid {
			return res, target, nil
		}
		if answer == nil {
			answer = res
		}
	}
	if answer != nil {
		return answer, target, nil
	}
	return nil, target, firstErr
}

// loadQueryTarget 读订单号与它发起过的每一笔支付（渠道 + 对外单号，最近发起的在前）。
//
// 对外单号取意图 action_payload 里记的 out_trade_no；在记它之前建的意图一律用的是订单号。
func (s *PaymentService) loadQueryTarget(ctx context.Context, tenantID, orderID, ownerID string) (*queryTarget, error) {
	if _, err := uuid.Parse(orderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var target queryTarget
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: ownerID}, func(tx pgx.Tx) error {
		var owner string
		err := tx.QueryRow(ctx, `
			SELECT order_no, user_id::text FROM orders
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, orderID).Scan(&target.OrderNo, &owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if ownerID != "" && owner != ownerID {
			return httpx.NotFoundOrForbidden()
		}
		rows, err := tx.Query(ctx, `
			SELECT pp.code, coalesce(pi.action_payload->>'out_trade_no', $3) AS out_trade_no
			  FROM payment_intents pi
			  JOIN payment_providers pp
			    ON pp.tenant_id = pi.tenant_id AND pp.id = pi.provider_id
			 WHERE pi.tenant_id = $1 AND pi.order_id = $2::uuid
			 GROUP BY pp.code, out_trade_no
			 ORDER BY max(pi.created_at) DESC, pp.code, out_trade_no`,
			tenantID, orderID, target.OrderNo)
		if err != nil {
			return err
		}
		target.Refs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (queryRef, error) {
			var r queryRef
			err := row.Scan(&r.Code, &r.OutTradeNo)
			return r, err
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, r := range target.Refs {
		if !slices.Contains(target.Providers, r.Code) {
			target.Providers = append(target.Providers, r.Code)
		}
	}
	if len(target.Refs) == 0 {
		return &target, httpx.New(httpx.CodeConflict, "该订单从未发起过支付，无法向渠道查单")
	}
	return &target, nil
}

// queryProvider 按一笔支付的对外单号向渠道查单；查到已付就补记，答上话的结果连同订单现状返回。
func (s *PaymentService) queryProvider(ctx context.Context, tenantID, orderID, orderNo string,
	ref queryRef, hook settleHook) (*OrderPaymentQuery, error) {

	prov, _, err := s.factory.Get(ctx, tenantID, ref.Code)
	if errors.Is(err, payment.ErrProviderDisabled) {
		return nil, httpx.New(httpx.CodeUnavailable, "该支付渠道已停用，无法向渠道查单").WithInternal(err)
	}
	if err != nil {
		return nil, err
	}

	qctx, cancel := context.WithTimeout(ctx, channelQueryTimeout)
	res, err := prov.QueryPayment(qctx, ref.OutTradeNo)
	cancel()
	if errors.Is(err, payment.ErrNotSupported) {
		return nil, httpx.New(httpx.CodeConflict, "该支付渠道不支持主动查单").WithInternal(err)
	}
	if err != nil {
		// 超时、网络、渠道报错：订单一概不动，原因只进日志
		return nil, httpx.New(httpx.CodeUnavailable, "渠道查单失败，请稍后再试").WithInternal(err)
	}

	out := &OrderPaymentQuery{OrderID: orderID, OrderNo: orderNo, ProviderCode: ref.Code}
	switch {
	case !res.Found:
		out.ChannelStatus = ChannelNotFound
	case res.Status != payment.StatusSucceeded:
		out.ChannelStatus = ChannelUnpaid
	default:
		out.ChannelStatus = ChannelPaid
		if err := s.reconcileQueried(ctx, tenantID, ref.Code, res, out, hook); err != nil {
			return nil, err
		}
		return out, nil
	}

	out.OrderStatus, err = s.orderStatus(ctx, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// reconcileQueried 把渠道确认的已付交给与回调同一条结算主链（settlePaymentTx），
// 在自己的事务里执行；hook（后台查单的审计）与结算在同一个事务里写。
//
// 按订单 id 定位，不按渠道回来的单号：换过方式的订单，对外单号是「订单号-序号」。
// 去重靠的是结算主链本身，不是事件号：事件号 PaymentRef + ":RECONCILED" 只让
// 「同一笔反复查单」在 payment_events 唯一约束上直接短路；真实回调晚到时事件号
// 不同，但它带着同一个渠道流水号，订单已结清，走进 quarantineUnexpectedPayment
// 后按 (provider_id, provider_payment_id) 认出已记过的那笔，回 AlreadyHandled，
// 不再记账也不再发权益。反过来回调先到、查单后到也是同一条路。
//
// 订单已取消或已过期时查到的钱照回调一样进挂账（released_order）；金额或币种
// 与订单不符照回调一样整笔拒绝（409），订单不动。
func (s *PaymentService) reconcileQueried(ctx context.Context, tenantID, code string,
	res *payment.QueryResult, out *OrderPaymentQuery, hook settleHook) error {

	if res.PaymentRef == "" {
		return httpx.New(httpx.CodeUnavailable, "渠道查单结果缺少支付流水号，无法补记")
	}
	in := PaymentWebhookInput{
		ProviderCode:      code,
		ProviderEventID:   res.PaymentRef + ":RECONCILED",
		ProviderPaymentID: res.PaymentRef,
		EventType:         "payment.succeeded",
		OrderID:           out.OrderID,
		Amount:            res.Amount,
		Currency:          res.Currency,
		RawPayload: map[string]any{
			"source": "active_query", "method": res.Method,
		},
		// 查询是我方主动发起、带商户密钥的请求，渠道地址受 SEC-007 约束，
		// 可信度不低于回调验签，故标记为已验证
		SignatureVerified: true,
	}
	var settled PaymentWebhookOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		settled = PaymentWebhookOutput{}
		if err := s.settle.settlePaymentTx(ctx, tx, tenantID, in, &settled); err != nil {
			return err
		}
		out.Reconciled = settled.Processed
		out.AlreadyRecorded = settled.AlreadyHandled
		out.QuarantineKind = settled.QuarantineKind
		if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, out.OrderID).Scan(&out.OrderStatus); err != nil {
			return err
		}
		if hook == nil {
			return nil
		}
		return hook(ctx, tx, out)
	})
	if err != nil {
		out.Reconciled, out.AlreadyRecorded, out.QuarantineKind, out.OrderStatus = false, false, "", ""
		var he *httpx.Error
		if errors.As(err, &he) {
			return he
		}
		return httpx.Internal(err)
	}
	// 事务已提交，这时通知才对应一个真实存在的变更（同 HandlePaymentWebhook）
	if settled.SubscriptionID != "" {
		s.settle.notifyUsersChanged(ctx, tenantID)
	}
	return nil
}

func (s *PaymentService) orderStatus(ctx context.Context, tenantID, orderID string) (string, error) {
	var status string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status FROM orders WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, orderID).Scan(&status)
	})
	return status, err
}
