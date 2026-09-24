// [INPUT]: 依赖 checkout.go 的 CreateOrder（人工单复用下单主路径）与 payments 的 HandlePaymentWebhook（线下收款合成一笔 offline 渠道回调），依赖 middleware 幂等声明、platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 CreateManualOrder、CreateManualOrderInput、ManualSettlement*、MarkOrderPaid、MarkOrderPaidInput、OfflineProviderCode
// [POS]: billing 的管理员订单动作：人工单（赠送当场履约，或建待支付单交给用户付）与标记线下已收款；两者都不另起炉灶，履约与记账与用户自己支付完全一致
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 管理员的两个订单动作（XBD-015）：
//
//   1. 人工单   —— 直接给用户开一张已履约的订单（赠送、补偿、线下成交）
//   2. 标记已支付 —— 用户线下付了钱（银行转账、当面收款），把待支付订单结掉
//
// 两个动作都不另起炉灶：
//   人工单复用 CreateOrder（全额减免 → payable 0 → 既有零元单捕获直接履约）；
//   标记已支付复用 HandlePaymentWebhook（合成一笔 offline 渠道的收款）。
//
// 这样订阅开通、配额、优惠券核销、佣金计提、账本分录的行为，
// 和用户自己在线支付完全一致 —— 不会出现"赠送的订阅少了个字段"这类问题。

// OfflineProviderCode 是线下收款用的渠道标识。
// 它在 payment_providers 里 accepting_new=false，用户结账时选不到，
// 只有管理员标记已支付时才会用到。
const OfflineProviderCode = "offline"

// 人工单的结算方式。
const (
	ManualSettlementGrant   = "grant"   // 赠送：全额减免、当场履约（缺省）
	ManualSettlementPending = "pending" // 待用户支付：建一张待支付单交给用户去付
)

type CreateManualOrderInput struct {
	UserID  string
	PlanID  string
	PriceID string
	Reason  string
	ActorID string
	// Settlement 为空按 grant。「线下已收款」（offline）与「从余额扣除」
	// （balance，D-C-3 未决）暂不接受：前者会让一次入账绕开标记已支付的
	// 近期重认证，要等人工单路由也挂上重认证再开。
	Settlement string
	Claim      middleware.IdempotencyClaim
}

func (s *Service) CreateManualOrder(ctx context.Context, tenantID string,
	in CreateManualOrderInput) (*CreateOrderOutput, error) {

	in.Reason = strings.TrimSpace(in.Reason)
	if tenantID == "" || in.UserID == "" || in.PlanID == "" || in.ActorID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant, actor, user and plan are required")
	}
	for _, id := range []string{in.UserID, in.PlanID, in.ActorID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, httpx.New(httpx.CodeBadRequest, "标识符格式不正确")
		}
	}
	// 库里的 CHECK 只要求 5 个字节，这里按字数卡：
	// 「补偿」两个汉字是 6 字节，能过 CHECK 却说明不了任何事。
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return nil, httpx.Invalid(map[string]string{
			"reason": "请写清开单原因，5 到 500 个字。这条会进审计，是日后对账的唯一依据"})
	}
	switch in.Settlement {
	case "":
		in.Settlement = ManualSettlementGrant
	case ManualSettlementGrant, ManualSettlementPending:
	case "offline", "balance":
		return nil, httpx.Invalid(map[string]string{
			"settlement": "暂不支持这种结算方式；线下已收款请先建待支付单，再在订单详情里标记已支付"})
	default:
		return nil, httpx.Invalid(map[string]string{"settlement": "结算方式只能是 grant 或 pending"})
	}

	out, err := s.CreateOrder(ctx, tenantID, CreateOrderInput{
		UserID: in.UserID, PlanID: in.PlanID, PriceID: in.PriceID, Claim: in.Claim,
		ManualGrant:  in.Settlement == ManualSettlementGrant,
		ManualReason: in.Reason, ManualActor: in.ActorID,
	})
	if err != nil {
		return nil, err
	}

	// 审计单独一笔事务：CreateOrder 已经提交，这里失败不该把订单回滚掉，
	// 但要留下痕迹 —— 没有审计的人工单等于没人负责。
	actor := in.ActorID
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "order.manual_created", ResourceType: "order",
				ResourceID: &out.OrderID,
				AfterDigest: map[string]any{
					"order_no": out.OrderNo, "user_id": in.UserID, "plan_id": in.PlanID,
					"reason": in.Reason, "status": out.Status, "settlement": in.Settlement,
					"subtotal": out.TotalAmount + out.DiscountAmount,
					"granted":  out.DiscountAmount,
				},
				APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			})
		}); err != nil {
		// 订单已经建好并履约了，这里只能如实上报。
		// 吞掉它等于留下一笔没人负责的赠送记录。
		return out, httpx.New(httpx.CodeInternal,
			"人工单 "+out.OrderNo+" 已创建，但审计写入失败，请立即联系运维核对："+err.Error())
	}
	return out, nil
}

type MarkOrderPaidInput struct {
	OrderID   string
	ActorID   string
	Reason    string
	Reference string // 线下凭证号：银行流水号、收据编号之类
}

// MarkOrderPaid 把一张待支付订单按「线下已收款」结清。
//
// 走的是和真实渠道回调同一条结算链路，所以订阅开通、优惠券核销、
// 余额解冻、佣金计提、账本分录一个不少。SignatureVerified 直接给 true ——
// 这条路的"签名"是管理员的 RBAC 权限加近期重认证，不是渠道密钥。
func (s *Service) MarkOrderPaid(ctx context.Context, tenantID string,
	in MarkOrderPaidInput) (*PaymentWebhookOutput, error) {

	in.Reason = strings.TrimSpace(in.Reason)
	in.Reference = strings.TrimSpace(in.Reference)
	if tenantID == "" || in.OrderID == "" || in.ActorID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "tenant, actor and order are required")
	}
	if _, err := uuid.Parse(in.OrderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return nil, httpx.Invalid(map[string]string{
			"reason": "请写清收款说明，5 到 500 个字"})
	}
	if in.Reference == "" || utf8.RuneCountInString(in.Reference) > 128 {
		return nil, httpx.Invalid(map[string]string{
			"reference": "请填写线下凭证号（银行流水号、收据编号等），最多 128 字"})
	}

	// 先读订单：金额必须由服务端从库里取，不能让调用方传 ——
	// 否则「标记已支付」就成了一个可以任意填金额的入账接口。
	var orderNo, status, currency string
	var payable int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT order_no, status, currency::text, payable_amount
			  FROM orders WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, in.OrderID).Scan(&orderNo, &status, &currency, &payable)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if status != "pending_payment" && status != "processing" {
		return nil, httpx.New(httpx.CodeConflict,
			"只有待支付的订单可以标记为已支付，当前状态："+status)
	}
	if payable <= 0 {
		return nil, httpx.New(httpx.CodeConflict, "这张订单不需要支付")
	}

	// provider_payment_id 用凭证号：渠道侧的唯一约束因此变成
	// 「同一张凭证不能入账两次」，是这个场景真正需要的幂等口径。
	out, err := s.HandlePaymentWebhook(ctx, tenantID, PaymentWebhookInput{
		ProviderCode:      OfflineProviderCode,
		ProviderEventID:   "offline:" + in.OrderID + ":" + in.Reference,
		ProviderPaymentID: "offline:" + in.Reference,
		EventType:         "payment.succeeded",
		OrderID:           in.OrderID,
		Amount:            payable,
		Currency:          currency,
		FeeAmount:         0,
		SignatureVerified: true,
		// payment_events.raw_payload 非空。真实渠道往里塞的是回调原文；
		// 线下收款没有回调，就把「谁在什么依据下确认了这笔钱」记进去 ——
		// 日后查这笔账时，这是唯一能追到人的线索。
		RawPayload: map[string]any{
			"source":    "admin_offline",
			"actor_id":  in.ActorID,
			"reference": in.Reference,
			"reason":    in.Reason,
		},
	})
	if err != nil {
		return nil, err
	}

	actor := in.ActorID
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "order.marked_paid", ResourceType: "order",
				ResourceID:   &in.OrderID,
				BeforeDigest: map[string]any{"status": status},
				AfterDigest: map[string]any{
					"order_no": orderNo, "amount": payable, "currency": currency,
					"reference": in.Reference, "reason": in.Reason,
					"payment_id": out.PaymentID, "ledger_txn": out.LedgerTxnID,
				},
				APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			})
		}); err != nil {
		return out, httpx.New(httpx.CodeInternal,
			"订单 "+orderNo+" 已入账，但审计写入失败，请立即联系运维核对："+err.Error())
	}
	return out, nil
}
